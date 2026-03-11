package watcher

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
)

func TestCRDName_Tombstone(t *testing.T) {
	crd := &metav1.PartialObjectMetadata{
		ObjectMeta: metav1.ObjectMeta{
			Name: "widgets.example.io",
		},
	}

	name := crdName(cache.DeletedFinalStateUnknown{Obj: crd})
	if name != "widgets.example.io" {
		t.Fatalf("unexpected tombstone name: %s", name)
	}
}

func TestSupportsWatchAndList(t *testing.T) {
	if !supportsWatchAndList(metav1.Verbs{"get", "list", "watch"}) {
		t.Fatal("expected list/watch verbs to be supported")
	}
	if supportsWatchAndList(metav1.Verbs{"get", "list"}) {
		t.Fatal("expected missing watch verb to be unsupported")
	}
	if supportsWatchAndList(metav1.Verbs{"get", "watch"}) {
		t.Fatal("expected missing list verb to be unsupported")
	}
}
