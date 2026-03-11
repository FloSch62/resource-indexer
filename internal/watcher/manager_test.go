package watcher

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/cache"
)

func TestCRDTargets_NamespacedServedVersions(t *testing.T) {
	m := &Manager{
		excludeGVR: map[string]struct{}{
			"example.io/v1alpha1/widgets": {},
		},
	}

	crd := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"metadata": map[string]interface{}{
				"name": "widgets.example.io",
			},
			"spec": map[string]interface{}{
				"group": "example.io",
				"scope": "Namespaced",
				"names": map[string]interface{}{
					"plural": "widgets",
					"kind":   "Widget",
				},
				"versions": []interface{}{
					map[string]interface{}{
						"name":   "v1",
						"served": true,
					},
					map[string]interface{}{
						"name":   "v1beta1",
						"served": false,
					},
					map[string]interface{}{
						"name":   "v1alpha1",
						"served": true,
					},
				},
			},
		},
	}

	name, targets, ok := m.crdTargets(crd)
	if !ok {
		t.Fatal("expected parse to succeed")
	}
	if name != "widgets.example.io" {
		t.Fatalf("unexpected CRD name: %s", name)
	}
	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(targets))
	}

	target, exists := targets["example.io/v1/widgets"]
	if !exists {
		t.Fatalf("expected v1 target to exist: %#v", targets)
	}
	if target.gvk.Kind != "Widget" {
		t.Fatalf("unexpected kind: %s", target.gvk.Kind)
	}
}

func TestCRDTargets_ClusterScoped(t *testing.T) {
	m := &Manager{excludeGVR: map[string]struct{}{}}
	crd := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"metadata": map[string]interface{}{
				"name": "clusters.example.io",
			},
			"spec": map[string]interface{}{
				"group": "example.io",
				"scope": "Cluster",
				"names": map[string]interface{}{
					"plural": "clusters",
					"kind":   "ClusterThing",
				},
				"versions": []interface{}{
					map[string]interface{}{
						"name":   "v1",
						"served": true,
					},
				},
			},
		},
	}

	_, targets, ok := m.crdTargets(crd)
	if !ok {
		t.Fatal("expected parse to succeed")
	}
	if len(targets) != 0 {
		t.Fatalf("expected no targets for cluster scoped CRD, got %d", len(targets))
	}
}

func TestCRDName_Tombstone(t *testing.T) {
	crd := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"metadata": map[string]interface{}{
				"name": "widgets.example.io",
			},
		},
	}

	name := crdName(cache.DeletedFinalStateUnknown{Obj: crd})
	if name != "widgets.example.io" {
		t.Fatalf("unexpected tombstone name: %s", name)
	}
}
