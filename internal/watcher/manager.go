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

	resyncPeriod    time.Duration
	discoveryPeriod time.Duration

	mu       sync.Mutex
	started  map[string]cache.SharedIndexInformer
	kindByID map[string]schema.GroupVersionKind
	ready    atomic.Bool
}

func New(
	logger *log.Logger,
	dynamicClient dynamic.Interface,
	discoveryClient discovery.DiscoveryInterface,
	s *store.Store,
	excludeGVR map[string]struct{},
	excludeNS map[string]struct{},
	resyncPeriod time.Duration,
	discoveryPeriod time.Duration,
) *Manager {
	return &Manager{
		log:             logger,
		dynamicClient:   dynamicClient,
		discovery:       discoveryClient,
		store:           s,
		excludeGVR:      excludeGVR,
		excludeNS:       excludeNS,
		resyncPeriod:    resyncPeriod,
		discoveryPeriod: discoveryPeriod,
		started:         make(map[string]cache.SharedIndexInformer),
		kindByID:        make(map[string]schema.GroupVersionKind),
	}
}

func (m *Manager) Ready() bool {
	return m.ready.Load()
}

func (m *Manager) Start(ctx context.Context) error {
	if err := m.syncResources(ctx); err != nil {
		return err
	}
	m.ready.Store(true)

	ticker := time.NewTicker(m.discoveryPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := m.syncResources(ctx); err != nil {
				m.log.Printf("discovery sync failed: %v", err)
			}
		}
	}
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
			if !supportsWatchAndList(apiRes.Verbs) {
				continue
			}
			gvr := schema.GroupVersionResource{Group: gv.Group, Version: gv.Version, Resource: apiRes.Name}
			id := gvrID(gvr)
			if _, blocked := m.excludeGVR[id]; blocked {
				continue
			}
			if m.hasInformer(id) {
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

func (m *Manager) hasInformer(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.started[id]
	return ok
}

func (m *Manager) startInformer(ctx context.Context, gvr schema.GroupVersionResource, gvk schema.GroupVersionKind, id string) error {
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
		return fmt.Errorf("register handlers: %w", err)
	}

	m.mu.Lock()
	m.started[id] = informer
	m.kindByID[id] = gvk
	m.mu.Unlock()

	go informer.Run(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), informer.HasSynced) {
		return fmt.Errorf("cache sync timeout")
	}
	return nil
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
