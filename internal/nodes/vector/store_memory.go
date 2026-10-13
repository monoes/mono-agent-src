package vector

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"sort"
	"sync"
)

// The memory provider keeps documents in a process-wide map keyed by
// memory_key. It lives as long as the process: documents inserted by one
// workflow run are visible to later runs inside the same daemon / app
// process, and are gone after a restart. One-shot CLI runs
// ("monoagentcli workflow run") start empty every time.
var memStores = struct {
	sync.Mutex
	m map[string]*memStore
}{m: map[string]*memStore{}}

type memStore struct {
	mu   sync.RWMutex
	docs map[string]Document
	ids  []string // insertion order, for stable ties
}

type memBackend struct{ s *memStore }

func newMemory(t storeTarget) backend {
	memStores.Lock()
	defer memStores.Unlock()
	s, ok := memStores.m[t.collection]
	if !ok {
		s = &memStore{docs: map[string]Document{}}
		memStores.m[t.collection] = s
	}
	return &memBackend{s: s}
}

func (b *memBackend) Close() error { return nil }

func (b *memBackend) Upsert(_ context.Context, docs []Document) error {
	b.s.mu.Lock()
	defer b.s.mu.Unlock()
	for _, d := range docs {
		if _, exists := b.s.docs[d.ID]; !exists {
			b.s.ids = append(b.s.ids, d.ID)
		}
		d.Metadata = copyMap(d.Metadata)
		b.s.docs[d.ID] = d
	}
	return nil
}

// Search ranks by cosine similarity (1 = identical direction). A filter is
// an object of metadata key → required value; every pair must match.
func (b *memBackend) Search(_ context.Context, vector []float64, topK int, filter interface{}) ([]Document, error) {
	want, err := memFilter(filter)
	if err != nil {
		return nil, err
	}
	b.s.mu.RLock()
	defer b.s.mu.RUnlock()
	var hits []Document
	for _, id := range b.s.ids {
		d, ok := b.s.docs[id]
		if !ok || !metaMatches(d.Metadata, want) {
			continue
		}
		if len(d.Vector) != len(vector) {
			return nil, fmt.Errorf("query vector has %d dimensions, stored document %q has %d", len(vector), id, len(d.Vector))
		}
		d.Score = cosine(vector, d.Vector)
		hits = append(hits, d)
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > topK {
		hits = hits[:topK]
	}
	return hits, nil
}

func (b *memBackend) Delete(_ context.Context, ids []string, filter interface{}, all bool) (int, error) {
	want, err := memFilter(filter)
	if err != nil {
		return 0, err
	}
	b.s.mu.Lock()
	defer b.s.mu.Unlock()
	drop := map[string]bool{}
	for _, id := range ids {
		if _, ok := b.s.docs[id]; ok {
			drop[id] = true
		}
	}
	if all || (filter != nil && len(ids) == 0) {
		for id, d := range b.s.docs {
			if all || metaMatches(d.Metadata, want) {
				drop[id] = true
			}
		}
	}
	kept := b.s.ids[:0]
	for _, id := range b.s.ids {
		if drop[id] {
			delete(b.s.docs, id)
			continue
		}
		kept = append(kept, id)
	}
	b.s.ids = kept
	return len(drop), nil
}

func memFilter(filter interface{}) (map[string]interface{}, error) {
	if filter == nil {
		return nil, nil
	}
	m, ok := filter.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("memory filter must be a JSON object of metadata key → value, got %T", filter)
	}
	return m, nil
}

func metaMatches(meta, want map[string]interface{}) bool {
	for k, v := range want {
		if !reflect.DeepEqual(meta[k], v) {
			return false
		}
	}
	return true
}

func cosine(a, b []float64) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
