package vector

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

const chromaColls = "/api/v2/tenants/default_tenant/databases/default_database/collections"

func chromaResponder(r *http.Request, _ interface{}) (int, string) {
	switch {
	case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/collections"):
		return 200, `{"id":"c-1","name":"docs"}`
	case strings.HasSuffix(r.URL.Path, "/query"):
		return 200, `{"ids":[["a","b"]],"documents":[["hello",null]],"metadatas":[[{"src":"x"},null]],"distances":[[0.25,0.5]]}`
	}
	return 200, `{}`
}

func TestChromaUpsert(t *testing.T) {
	s := newFBServer(t, chromaResponder)
	be, _ := newChroma(storeTarget{cfg: map[string]interface{}{"url": s.srv.URL, "api_key": "k"}, collection: "docs"})
	docs := []Document{
		{ID: "a", Content: "hello", Vector: []float64{1, 2}, Metadata: map[string]interface{}{"src": "x", "tags": []interface{}{"p"}, "n": nil}},
		{ID: "b", Content: "bye", Vector: []float64{3, 4}},
	}
	if err := be.Upsert(context.Background(), docs); err != nil {
		t.Fatal(err)
	}
	if err := be.Upsert(context.Background(), docs[:1]); err != nil {
		t.Fatal(err)
	}
	reqs := s.requests()
	if len(reqs) != 3 {
		t.Fatalf("want collection lookup once + 2 upserts, got %d", len(reqs))
	}
	for _, r := range reqs {
		if r.Header.Get("x-chroma-token") != "k" || r.Method != "POST" {
			t.Errorf("%s %s token=%q", r.Method, r.Path, r.Header.Get("x-chroma-token"))
		}
	}
	if reqs[0].Path != chromaColls {
		t.Errorf("collection path %s", reqs[0].Path)
	}
	fbEqualJSON(t, "get_or_create", reqs[0].Body, `{"get_or_create":true,"metadata":{"hnsw:space":"cosine"},"name":"docs"}`)
	if reqs[1].Path != chromaColls+"/c-1/upsert" {
		t.Errorf("upsert path %s", reqs[1].Path)
	}
	fbEqualJSON(t, "upsert", reqs[1].Body, `{"documents":["hello","bye"],"embeddings":[[1,2],[3,4]],"ids":["a","b"],"metadatas":[{"src":"x","tags":"[\"p\"]"},{}]}`)
}

func TestChromaSearch(t *testing.T) {
	s := newFBServer(t, chromaResponder)
	be, _ := newChroma(storeTarget{cfg: map[string]interface{}{"url": s.srv.URL, "tenant": "t1"}, collection: "docs", namespace: "db1"})
	hits, err := be.Search(context.Background(), []float64{0.5}, 2, map[string]interface{}{"src": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 || hits[0].ID != "a" || hits[0].Content != "hello" || hits[0].Score != 0.75 || hits[0].Metadata["src"] != "x" || hits[1].Score != 0.5 {
		t.Fatalf("hits %+v", hits)
	}
	reqs := s.requests()
	if reqs[0].Path != "/api/v2/tenants/t1/databases/db1/collections" || reqs[1].Path != "/api/v2/tenants/t1/databases/db1/collections/c-1/query" {
		t.Fatalf("paths %s %s", reqs[0].Path, reqs[1].Path)
	}
	fbEqualJSON(t, "query", reqs[1].Body, `{"include":["documents","metadatas","distances"],"n_results":2,"query_embeddings":[[0.5]],"where":{"src":"x"}}`)
	if _, err := be.Search(context.Background(), []float64{1}, 1, "x"); err == nil {
		t.Error("string filter should fail")
	}
}

func TestChromaDeleteVariants(t *testing.T) {
	s := newFBServer(t, func(r *http.Request, b interface{}) (int, string) {
		if r.Method == "DELETE" {
			return 404, `{"error":"not found"}`
		}
		return chromaResponder(r, b)
	})
	be, _ := newChroma(storeTarget{cfg: map[string]interface{}{"url": s.srv.URL}, collection: "docs"})
	if n, err := be.Delete(context.Background(), []string{"a"}, map[string]interface{}{"src": "x"}, false); err != nil || n != -1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if _, err := be.Delete(context.Background(), nil, nil, true); err != nil {
		t.Fatalf("delete all with 404 should succeed: %v", err)
	}
	reqs := s.requests()
	if reqs[1].Path != chromaColls+"/c-1/delete" {
		t.Errorf("delete path %s", reqs[1].Path)
	}
	fbEqualJSON(t, "delete", reqs[1].Body, `{"ids":["a"],"where":{"src":"x"}}`)
	if reqs[2].Method != "DELETE" || reqs[2].Path != chromaColls+"/docs" {
		t.Errorf("drop %s %s", reqs[2].Method, reqs[2].Path)
	}
}

func TestChromaHTTPError(t *testing.T) {
	s := newFBServer(t, func(r *http.Request, _ interface{}) (int, string) { return 401, `{"error":"unauthorized"}` })
	be, _ := newChroma(storeTarget{cfg: map[string]interface{}{"url": s.srv.URL}, collection: "docs"})
	err := be.Upsert(context.Background(), []Document{{ID: "a", Vector: []float64{1}}})
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") || !strings.Contains(err.Error(), `collection "docs"`) {
		t.Fatalf("got %v", err)
	}
}

func TestChromaStoreNodeEndToEnd(t *testing.T) {
	s := newFBServer(t, chromaResponder)
	n := &StoreNode{}
	in := workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(map[string]interface{}{
		"pageContent": "hello", "embedding": []interface{}{1.0, 2.0},
	})}}
	out, err := n.Execute(context.Background(), in, map[string]interface{}{
		"provider": "chroma", "mode": "insert", "collection": "docs", "url": s.srv.URL,
	})
	if err != nil || len(out[0].Items) != 1 || out[0].Items[0].JSON["id"] == "" {
		t.Fatalf("insert: %v %+v", err, out)
	}
	out, err = n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"provider": "chroma", "mode": "search", "collection": "docs", "url": s.srv.URL, "query_vector": "[1,2]", "top_k": 2,
	})
	if err != nil || len(out[0].Items) != 2 || out[0].Items[0].JSON["pageContent"] != "hello" {
		t.Fatalf("search: %v %+v", err, out)
	}
}
