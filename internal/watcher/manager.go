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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
)

type Manager struct {
	log *log.Logger

	dynamicClient dynamic.Interface
	discovery     discovery.DiscoveryInterface
	store         *store.Store

	excludeGVR map[string]struct{}
	excludeNS  map[string]struct{}

	resyncPeriod time.Duration

	mu           sync.Mutex
	started      map[string]*informerEntry
	crdIDsByName map[string]map[string]struct{}
	ready        atomic.Bool
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
	dynamicClient dynamic.Interface,
	discoveryClient discovery.DiscoveryInterface,
	s *store.Store,
	excludeGVR map[string]struct{},
	excludeNS map[string]struct{},
	resyncPeriod time.Duration,
) *Manager {
	return &Manager{
		log:           logger,
		dynamicClient: dynamicClient,
		discovery:     discoveryClient,
		store:         s,
		excludeGVR:    excludeGVR,
		excludeNS:     excludeNS,
		resyncPeriod:  resyncPeriod,
		started:       make(map[string]*informerEntry),
		crdIDsByName:  make(map[string]map[string]struct{}),
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

	crdInformer := dynamicinformer.NewFilteredDynamicInformer(
		m.dynamicClient,
		crdGVR,
		metav1.NamespaceAll,
		m.resyncPeriod,
		cache.Indexers{},
		nil,
	).Informer()

	_, err := crdInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			m.reconcileCRD(ctx, obj)
		},
		UpdateFunc: func(_ interface{}, newObj interface{}) {
			m.reconcileCRD(ctx, newObj)
		},
		DeleteFunc: func(obj interface{}) {
			m.handleCRDDelete(obj)
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
	if err != nil {
		if discovery.IsGroupDiscoveryFailedError(err) {
			m.log.Printf("partial discovery failure: %v", err)
		} else {
			return fmt.Errorf("discover resources: %w", err)
		}
	}

	started := 0
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
			if err := m.startInformer(ctx, gvr, gvk, id); err != nil {
				m.log.Printf("start informer %s failed: %v", id, err)
				continue
			}
			started++
		}
	}

	if started > 0 {
		m.log.Printf("started %d new informers", started)
	}
	return nil
}

func (m *Manager) reconcileCRD(ctx context.Context, obj interface{}) {
	name, targets, ok := m.crdTargets(obj)
	if !ok {
		return
	}

	current := m.getCRDIDs(name)
	next := make(map[string]struct{}, len(targets))

	for id, target := range targets {
		if err := m.startInformer(ctx, target.gvr, target.gvk, id); err != nil {
			m.log.Printf("start informer %s for CRD %s failed: %v", id, name, err)
			continue
		}
		next[id] = struct{}{}
	}

	for id := range current {
		if _, keep := next[id]; keep {
			continue
		}
		m.stopInformer(id)
	}

	m.setCRDIDs(name, next)
}

func (m *Manager) handleCRDDelete(obj interface{}) {
	name := crdName(obj)
	if name == "" {
		return
	}
	for id := range m.getCRDIDs(name) {
		m.stopInformer(id)
	}
	m.clearCRDIDs(name)
}

func (m *Manager) crdTargets(obj interface{}) (string, map[string]informerTarget, bool) {
	u, ok := toUnstructured(obj)
	if !ok {
		return "", nil, false
	}

	crdName := u.GetName()
	if crdName == "" {
		return "", nil, false
	}

	targets := make(map[string]informerTarget)

	scope, _, _ := unstructured.NestedString(u.Object, "spec", "scope")
	if scope != "Namespaced" {
		return crdName, targets, true
	}

	group, _, _ := unstructured.NestedString(u.Object, "spec", "group")
	plural, _, _ := unstructured.NestedString(u.Object, "spec", "names", "plural")
	kind, _, _ := unstructured.NestedString(u.Object, "spec", "names", "kind")
	if group == "" || plural == "" || kind == "" {
		return crdName, targets, true
	}

	versions, found, _ := unstructured.NestedSlice(u.Object, "spec", "versions")
	if found && len(versions) > 0 {
		for _, item := range versions {
			versionEntry, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			version, _ := versionEntry["name"].(string)
			if version == "" {
				continue
			}
			if servedRaw, exists := versionEntry["served"]; exists {
				served, ok := servedRaw.(bool)
				if !ok || !served {
					continue
				}
			}
			m.addCRDTarget(targets, group, version, plural, kind)
		}
		return crdName, targets, true
	}

	legacyVersion, _, _ := unstructured.NestedString(u.Object, "spec", "version")
	if legacyVersion != "" {
		m.addCRDTarget(targets, group, legacyVersion, plural, kind)
	}

	return crdName, targets, true
}

func (m *Manager) addCRDTarget(targets map[string]informerTarget, group, version, plural, kind string) {
	gvr := schema.GroupVersionResource{
		Group:    group,
		Version:  version,
		Resource: plural,
	}
	id := gvrID(gvr)
	if _, blocked := m.excludeGVR[id]; blocked {
		return
	}

	targets[id] = informerTarget{
		gvr: gvr,
		gvk: schema.GroupVersionKind{
			Group:   group,
			Version: version,
			Kind:    kind,
		},
	}
}

func crdName(obj interface{}) string {
	u, ok := toUnstructured(obj)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			return ""
		}
		u, ok = toUnstructured(tombstone.Obj)
		if !ok {
			return ""
		}
	}
	return u.GetName()
}

func (m *Manager) getCRDIDs(name string) map[string]struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()

	existing := m.crdIDsByName[name]
	out := make(map[string]struct{}, len(existing))
	for id := range existing {
		out[id] = struct{}{}
	}
	return out
}

func (m *Manager) setCRDIDs(name string, ids map[string]struct{}) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(ids) == 0 {
		delete(m.crdIDsByName, name)
		return
	}
	copied := make(map[string]struct{}, len(ids))
	for id := range ids {
		copied[id] = struct{}{}
	}
	m.crdIDsByName[name] = copied
}

func (m *Manager) clearCRDIDs(name string) {
	m.mu.Lock()
	delete(m.crdIDsByName, name)
	m.mu.Unlock()
}

func (m *Manager) stopAllInformers() {
	m.mu.Lock()
	entries := make([]*informerEntry, 0, len(m.started))
	for id, entry := range m.started {
		delete(m.started, id)
		entries = append(entries, entry)
	}
	m.crdIDsByName = make(map[string]map[string]struct{})
	m.mu.Unlock()

	for _, entry := range entries {
		entry.cancel()
	}
}

func (m *Manager) startInformer(ctx context.Context, gvr schema.GroupVersionResource, gvk schema.GroupVersionKind, id string) error {
	childCtx, cancel := context.WithCancel(ctx)
	genericInformer := dynamicinformer.NewFilteredDynamicInformer(
		m.dynamicClient,
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
		return fmt.Errorf("register handlers: %w", err)
	}

	m.mu.Lock()
	if _, exists := m.started[id]; exists {
		m.mu.Unlock()
		cancel()
		return nil
	}
	m.started[id] = &informerEntry{
		cancel: cancel,
	}
	m.mu.Unlock()

	go informer.Run(childCtx.Done())
	if !cache.WaitForCacheSync(childCtx.Done(), informer.HasSynced) {
		m.stopInformer(id)
		return fmt.Errorf("cache sync timeout")
	}
	return nil
}

func (m *Manager) stopInformer(id string) {
	m.mu.Lock()
	entry, ok := m.started[id]
	if ok {
		delete(m.started, id)
	}
	for crdName, ids := range m.crdIDsByName {
		delete(ids, id)
		if len(ids) == 0 {
			delete(m.crdIDsByName, crdName)
		}
	}
	m.mu.Unlock()

	if ok {
		entry.cancel()
	}
}

func (m *Manager) handleUpsert(obj interface{}, gvk schema.GroupVersionKind) {
	u, ok := toUnstructured(obj)
	if !ok {
		return
	}
	if _, blocked := m.excludeNS[u.GetNamespace()]; blocked {
		return
	}
	if u.GetNamespace() == "" {
		return
	}

	apiVersion := u.GetAPIVersion()
	if apiVersion == "" {
		if gvk.Group == "" {
			apiVersion = gvk.Version
		} else {
			apiVersion = gvk.Group + "/" + gvk.Version
		}
	}
	kind := u.GetKind()
	if kind == "" {
		kind = gvk.Kind
	}

	m.store.Upsert(objectKey(apiVersion, kind, u.GetNamespace(), u.GetName()), store.Record{
		APIVersion: apiVersion,
		Kind:       kind,
		Namespace:  u.GetNamespace(),
		Name:       u.GetName(),
		Group:      gvk.Group,
		Version:    gvk.Version,
	})
}

func (m *Manager) handleDelete(obj interface{}, gvk schema.GroupVersionKind) {
	u, ok := toUnstructured(obj)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			return
		}
		u, ok = toUnstructured(tombstone.Obj)
		if !ok {
			return
		}
	}
	if u.GetNamespace() == "" {
		return
	}

	apiVersion := u.GetAPIVersion()
	if apiVersion == "" {
		if gvk.Group == "" {
			apiVersion = gvk.Version
		} else {
			apiVersion = gvk.Group + "/" + gvk.Version
		}
	}
	kind := u.GetKind()
	if kind == "" {
		kind = gvk.Kind
	}
	m.store.Delete(objectKey(apiVersion, kind, u.GetNamespace(), u.GetName()))
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

func toUnstructured(obj interface{}) (*unstructured.Unstructured, bool) {
	u, ok := obj.(*unstructured.Unstructured)
	return u, ok
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
