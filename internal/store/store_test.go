package store

import (
	"testing"
)

func TestSnapshotFiltersAndPagination(t *testing.T) {
	s := New()
	s.Upsert("a", Record{Group: "apps", Version: "v1", APIVersion: "apps/v1", Kind: "Deployment", Namespace: "ns1", Name: "a"})
	s.Upsert("b", Record{Group: "", Version: "v1", APIVersion: "v1", Kind: "Pod", Namespace: "ns1", Name: "b"})
	s.Upsert("c", Record{Group: "apps", Version: "v1", APIVersion: "apps/v1", Kind: "Deployment", Namespace: "ns2", Name: "c"})

	items := s.Snapshot(Filters{Namespace: "ns1", Group: "apps", Kind: "Deployment"})
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Name != "a" {
		t.Fatalf("expected item a, got %s", items[0].Name)
	}

	items = s.Snapshot(Filters{Limit: 1, Offset: 1})
	if len(items) != 1 {
		t.Fatalf("expected 1 paged item, got %d", len(items))
	}
}

func TestDelete(t *testing.T) {
	s := New()
	s.Upsert("x", Record{Name: "x"})
	s.Delete("x")
	if s.Count() != 0 {
		t.Fatalf("expected empty store after delete")
	}
}

func TestLineFormat(t *testing.T) {
	r := Record{
		Namespace:  "default",
		APIVersion: "core.eda.nokia.com/v1",
		Kind:       "NodeProfile",
		Name:       "sros-ghcr-25.7.r2",
		Labels: map[string]string{
			"app":                  "indexer",
			"app.kubernetes.io/id": "abc123",
		},
	}
	got := r.Line()
	want := "default/core.eda.nokia.com/v1/NodeProfile/sros-ghcr-25.7.r2 labels={\"app\":\"indexer\",\"app.kubernetes.io/id\":\"abc123\"}"
	if got != want {
		t.Fatalf("unexpected line format: got %q want %q", got, want)
	}
}
