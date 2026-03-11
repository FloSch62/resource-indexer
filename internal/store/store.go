package store

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Record is one line item in both text and JSON outputs.
type Record struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
	Group      string `json:"group"`
	Version    string `json:"version"`
}

// Line returns the canonical plain-text format:
// <namespace>/<apiVersion>/<kind>/<name>
func (r Record) Line() string {
	return fmt.Sprintf("%s/%s/%s/%s", r.Namespace, r.APIVersion, r.Kind, r.Name)
}

// Filters limit snapshot output.
type Filters struct {
	Namespace string
	Group     string
	Version   string
	Kind      string
	Limit     int
	Offset    int
}

type Store struct {
	mu      sync.RWMutex
	records map[string]Record
}

func New() *Store {
	return &Store{records: make(map[string]Record)}
}

func (s *Store) Upsert(key string, rec Record) {
	s.mu.Lock()
	s.records[key] = rec
	s.mu.Unlock()
}

func (s *Store) Delete(key string) {
	s.mu.Lock()
	delete(s.records, key)
	s.mu.Unlock()
}

func (s *Store) Snapshot(f Filters) []Record {
	s.mu.RLock()
	items := make([]Record, 0, len(s.records))
	for _, rec := range s.records {
		if !matches(rec, f) {
			continue
		}
		items = append(items, rec)
	}
	s.mu.RUnlock()

	sort.Slice(items, func(i, j int) bool {
		if items[i].Namespace != items[j].Namespace {
			return items[i].Namespace < items[j].Namespace
		}
		if items[i].APIVersion != items[j].APIVersion {
			return items[i].APIVersion < items[j].APIVersion
		}
		if items[i].Kind != items[j].Kind {
			return items[i].Kind < items[j].Kind
		}
		return items[i].Name < items[j].Name
	})

	if f.Offset > 0 {
		if f.Offset >= len(items) {
			return []Record{}
		}
		items = items[f.Offset:]
	}
	if f.Limit > 0 && f.Limit < len(items) {
		items = items[:f.Limit]
	}
	return items
}

func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.records)
}

func matches(rec Record, f Filters) bool {
	if f.Namespace != "" && rec.Namespace != f.Namespace {
		return false
	}
	if f.Group != "" && rec.Group != f.Group {
		return false
	}
	if f.Version != "" && rec.Version != f.Version {
		return false
	}
	if f.Kind != "" && !strings.EqualFold(rec.Kind, f.Kind) {
		return false
	}
	return true
}
