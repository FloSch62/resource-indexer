package watcher

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"resource-list/internal/store"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/metadata/metadatainformer"
	"k8s.io/client-go/tools/cache"
)

type Manager struct {
	log *log.Logger

	metadataClient metadata.Interface
	discovery      discovery.DiscoveryInterface
	store          *store.Store

	excludeGVR map[string]struct{}
	excludeNS  map[string]struct{}

	resyncPeriod time.Duration

	mu      sync.Mutex
	started map[string]*informerEntry
	ready   atomic.Bool
}

type informerEntry struct {
	cancel context.CancelFunc
}

type informerTarget struct {
	gvr schema.GroupVersionResource
	gvk schema.GroupVersionKind
}

func New(
	logger *log.Logger,
	metadataClient metadata.Interface,
	discoveryClient discovery.DiscoveryInterface,
	s *store.Store,
	excludeGVR map[string]struct{},
	excludeNS map[string]struct{},
	resyncPeriod time.Duration,
) *Manager {
	return &Manager{
		log:            logger,
		metadataClient: metadataClient,
		discovery:      discoveryClient,
		store:          s,
		excludeGVR:     excludeGVR,
		excludeNS:      excludeNS,
		resyncPeriod:   resyncPeriod,
		started:        make(map[string]*informerEntry),
	}
}

func (m *Manager) Ready() bool {
	return m.ready.Load()
}

func (m *Manager) Start(ctx context.Context) error {
	if err := m.startCRDWatcher(ctx); err != nil {
		return err
	}
	if err := m.syncResources(ctx); err != nil {
		return err
	}
	m.ready.Store(true)

	<-ctx.Done()
	m.stopAllInformers()
	return nil
}

func (m *Manager) startCRDWatcher(ctx context.Context) error {
	crdGVR := schema.GroupVersionResource{
		Group:    "apiextensions.k8s.io",
		Version:  "v1",
		Resource: "customresourcedefinitions",
	}

	crdInformer := metadatainformer.NewFilteredMetadataInformer(
		m.metadataClient,
		crdGVR,
		metav1.NamespaceAll,
		m.resyncPeriod,
		cache.Indexers{},
		nil,
	).Informer()

	_, err := crdInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(_ interface{}) {
			if err := m.syncResources(ctx); err != nil {
				m.log.Printf("sync after CRD add failed: %v", err)
			}
		},
		UpdateFunc: func(_ interface{}, _ interface{}) {
			if err := m.syncResources(ctx); err != nil {
				m.log.Printf("sync after CRD update failed: %v", err)
			}
		},
		DeleteFunc: func(obj interface{}) {
			name := crdName(obj)
			if name == "" {
				return
			}
			if err := m.syncResources(ctx); err != nil {
				m.log.Printf("sync after CRD delete %s failed: %v", name, err)
			}
		},
	})
	if err != nil {
		return fmt.Errorf("register CRD handlers: %w", err)
	}

	go crdInformer.Run(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), crdInformer.HasSynced) {
		return fmt.Errorf("CRD informer cache sync timeout")
	}
	return nil
}

func (m *Manager) syncResources(ctx context.Context) error {
	lists, err := m.discovery.ServerPreferredNamespacedResources()
	partialDiscovery := false
	if err != nil {
		if discovery.IsGroupDiscoveryFailedError(err) {
			partialDiscovery = true
			m.log.Printf("partial discovery failure: %v", err)
		} else {
			return fmt.Errorf("discover resources: %w", err)
		}
	}

	targets := make(map[string]informerTarget)
	for _, list := range lists {
		gv, parseErr := schema.ParseGroupVersion(list.GroupVersion)
		if parseErr != nil {
			continue
		}
		for _, apiRes := range list.APIResources {
			if strings.Contains(apiRes.Name, "/") {
				continue
			}
			if !apiRes.Namespaced {
				continue
			}
			if !supportsWatchAndList(apiRes.Verbs) {
				continue
			}
			gvr := schema.GroupVersionResource{Group: gv.Group, Version: gv.Version, Resource: apiRes.Name}
			id := gvrID(gvr)
			if _, blocked := m.excludeGVR[id]; blocked {
				continue
			}
			gvk := schema.GroupVersionKind{Group: gv.Group, Version: gv.Version, Kind: apiRes.Kind}
			targets[id] = informerTarget{
				gvr: gvr,
				gvk: gvk,
			}
		}
	}

	started := 0
	for id, target := range targets {
		startedNew, err := m.startInformer(ctx, target.gvr, target.gvk, id)
		if err != nil {
				m.log.Printf("start informer %s failed: %v", id, err)
			continue
		}
		if startedNew {
			started++
		}
	}

	if !partialDiscovery {
		m.stopMissingInformers(targets)
	}

	if started > 0 {
		m.log.Printf("started %d new informers", started)
	}
	return nil
}

func crdName(obj interface{}) string {
	meta, ok := toPartialMetadata(obj)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			return ""
		}
		meta, ok = toPartialMetadata(tombstone.Obj)
		if !ok {
			return ""
		}
	}
	return meta.GetName()
}

func (m *Manager) stopAllInformers() {
	m.mu.Lock()
	entries := make([]*informerEntry, 0, len(m.started))
	for id, entry := range m.started {
		delete(m.started, id)
		entries = append(entries, entry)
	}
	m.mu.Unlock()

	for _, entry := range entries {
		entry.cancel()
	}
}

func (m *Manager) startInformer(ctx context.Context, gvr schema.GroupVersionResource, gvk schema.GroupVersionKind, id string) (bool, error) {
	m.mu.Lock()
	if _, exists := m.started[id]; exists {
		m.mu.Unlock()
		return false, nil
	}
	m.mu.Unlock()

	childCtx, cancel := context.WithCancel(ctx)
	genericInformer := metadatainformer.NewFilteredMetadataInformer(
		m.metadataClient,
		gvr,
		metav1.NamespaceAll,
		m.resyncPeriod,
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
		nil,
	)
	informer := genericInformer.Informer()
	_, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			m.handleUpsert(obj, gvk)
		},
		UpdateFunc: func(_ interface{}, newObj interface{}) {
			m.handleUpsert(newObj, gvk)
		},
		DeleteFunc: func(obj interface{}) {
			m.handleDelete(obj, gvk)
		},
	})
	if err != nil {
		cancel()
		return false, fmt.Errorf("register handlers: %w", err)
	}

	m.mu.Lock()
	if _, exists := m.started[id]; exists {
		m.mu.Unlock()
		cancel()
		return false, nil
	}
	m.started[id] = &informerEntry{
		cancel: cancel,
	}
	m.mu.Unlock()

	go informer.Run(childCtx.Done())
	if !cache.WaitForCacheSync(childCtx.Done(), informer.HasSynced) {
		m.stopInformer(id)
		return false, fmt.Errorf("cache sync timeout")
	}
	return true, nil
}

func (m *Manager) stopMissingInformers(targets map[string]informerTarget) {
	m.mu.Lock()
	toStop := make([]string, 0)
	for id := range m.started {
		if _, keep := targets[id]; !keep {
			toStop = append(toStop, id)
		}
	}
	m.mu.Unlock()

	for _, id := range toStop {
		m.stopInformer(id)
	}
}

func (m *Manager) stopInformer(id string) {
	m.mu.Lock()
	entry, ok := m.started[id]
	if ok {
		delete(m.started, id)
	}
	m.mu.Unlock()

	if ok {
		entry.cancel()
	}
}

func (m *Manager) handleUpsert(obj interface{}, gvk schema.GroupVersionKind) {
	meta, ok := toPartialMetadata(obj)
	if !ok {
		return
	}
	if _, blocked := m.excludeNS[meta.GetNamespace()]; blocked {
		return
	}
	if meta.GetNamespace() == "" {
		return
	}

	apiVersion := meta.GetAPIVersion()
	if apiVersion == "" {
		if gvk.Group == "" {
			apiVersion = gvk.Version
		} else {
			apiVersion = gvk.Group + "/" + gvk.Version
		}
	}
	kind := meta.GetKind()
	if kind == "" {
		kind = gvk.Kind
	}

	m.store.Upsert(objectKey(apiVersion, kind, meta.GetNamespace(), meta.GetName()), store.Record{
		APIVersion: apiVersion,
		Kind:       kind,
		Namespace:  meta.GetNamespace(),
		Name:       meta.GetName(),
		Labels:     copyLabels(meta.GetLabels()),
		Group:      gvk.Group,
		Version:    gvk.Version,
	})
}

func (m *Manager) handleDelete(obj interface{}, gvk schema.GroupVersionKind) {
	meta, ok := toPartialMetadata(obj)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			return
		}
		meta, ok = toPartialMetadata(tombstone.Obj)
		if !ok {
			return
		}
	}
	if meta.GetNamespace() == "" {
		return
	}

	apiVersion := meta.GetAPIVersion()
	if apiVersion == "" {
		if gvk.Group == "" {
			apiVersion = gvk.Version
		} else {
			apiVersion = gvk.Group + "/" + gvk.Version
		}
	}
	kind := meta.GetKind()
	if kind == "" {
		kind = gvk.Kind
	}
	m.store.Delete(objectKey(apiVersion, kind, meta.GetNamespace(), meta.GetName()))
}

func supportsWatchAndList(verbs metav1.Verbs) bool {
	hasList := false
	hasWatch := false
	for _, verb := range verbs {
		if verb == "list" {
			hasList = true
		}
		if verb == "watch" {
			hasWatch = true
		}
	}
	return hasList && hasWatch
}

func toPartialMetadata(obj interface{}) (*metav1.PartialObjectMetadata, bool) {
	meta, ok := obj.(*metav1.PartialObjectMetadata)
	return meta, ok
}

func objectKey(apiVersion, kind, ns, name string) string {
	return apiVersion + "|" + kind + "|" + ns + "|" + name
}

func gvrID(gvr schema.GroupVersionResource) string {
	if gvr.Group == "" {
		return fmt.Sprintf("%s/%s", gvr.Version, gvr.Resource)
	}
	return fmt.Sprintf("%s/%s/%s", gvr.Group, gvr.Version, gvr.Resource)
}

func copyLabels(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
