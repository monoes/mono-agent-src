package vector

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

// fbReq is one request captured by fbServer.
type fbReq struct {
	Method, Path, Query string
	Header              http.Header
	Body                interface{}
}

// fbServer records every request and answers via respond (path → status, body).
type fbServer struct {
	mu   sync.Mutex
	reqs []fbReq
	srv  *httptest.Server
}

func newFBServer(t *testing.T, respond func(r *http.Request, body interface{}) (int, string)) *fbServer {
	t.Helper()
	s := &fbServer{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body interface{}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Errorf("request body is not JSON: %s", raw)
			}
		}
		s.mu.Lock()
		s.reqs = append(s.reqs, fbReq{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone(), body})
		s.mu.Unlock()
		code, resp := respond(r, body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = io.WriteString(w, resp)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *fbServer) requests() []fbReq {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]fbReq(nil), s.reqs...)
}

func fbJSON(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// fbEqualJSON compares got (decoded JSON) with want (a JSON literal).
func fbEqualJSON(t *testing.T, label string, got interface{}, want string) {
	t.Helper()
	var w interface{}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("%s: bad want JSON: %v", label, err)
	}
	if fbJSON(got) != fbJSON(w) {
		t.Errorf("%s:\n got  %s\n want %s", label, fbJSON(got), fbJSON(w))
	}
}

func milvusFor(t *testing.T, url string, extra map[string]interface{}) *milvusBackend {
	t.Helper()
	cfg := map[string]interface{}{"url": url, "token": "tok"}
	for k, v := range extra {
		cfg[k] = v
	}
	be, err := newMilvus(storeTarget{cfg: cfg, collection: "docs"})
	if err != nil {
		t.Fatal(err)
	}
	return be.(*milvusBackend)
}

func TestMilvusUpsertCreatesCollection(t *testing.T) {
	s := newFBServer(t, func(r *http.Request, _ interface{}) (int, string) {
		if r.URL.Path == "/v2/vectordb/collections/has" {
			return 200, `{"code":0,"data":{"has":false}}`
		}
		return 200, `{"code":0,"data":{}}`
	})
	b := milvusFor(t, s.srv.URL, map[string]interface{}{"db_name": "kb"})
	docs := []Document{{ID: "a", Content: "hello", Metadata: map[string]interface{}{"src": "x"}, Vector: []float64{1, 2}}}
	if err := b.Upsert(context.Background(), docs); err != nil {
		t.Fatal(err)
	}
	reqs := s.requests()
	if len(reqs) != 3 {
		t.Fatalf("want 3 requests, got %d", len(reqs))
	}
	for _, r := range reqs {
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("%s %s auth=%q", r.Method, r.Path, r.Header.Get("Authorization"))
		}
	}
	fbEqualJSON(t, "has", reqs[0].Body, `{"collectionName":"docs","dbName":"kb"}`)
	if reqs[1].Path != "/v2/vectordb/collections/create" {
		t.Fatalf("path %s", reqs[1].Path)
	}
	fbEqualJSON(t, "create", reqs[1].Body, `{"autoID":false,"collectionName":"docs","dbName":"kb","dimension":2,"idType":"VarChar","metricType":"COSINE","params":{"max_length":"512"},"primaryFieldName":"id","vectorFieldName":"vector"}`)
	if reqs[2].Path != "/v2/vectordb/entities/upsert" {
		t.Fatalf("path %s", reqs[2].Path)
	}
	fbEqualJSON(t, "upsert", reqs[2].Body, `{"collectionName":"docs","dbName":"kb","data":[{"id":"a","metadata":{"src":"x"},"text":"hello","vector":[1,2]}]}`)
}

func TestMilvusUsernamePasswordAuthAndExistingCollection(t *testing.T) {
	s := newFBServer(t, func(r *http.Request, _ interface{}) (int, string) {
		return 200, `{"code":0,"data":{"has":true}}`
	})
	be, _ := newMilvus(storeTarget{cfg: map[string]interface{}{"baseUrl": s.srv.URL, "username": "u", "password": "p"}, collection: "docs"})
	if err := be.Upsert(context.Background(), []Document{{ID: "a", Vector: []float64{1}}}); err != nil {
		t.Fatal(err)
	}
	reqs := s.requests()
	if len(reqs) != 2 || reqs[1].Path != "/v2/vectordb/entities/upsert" {
		t.Fatalf("unexpected requests %+v", reqs)
	}
	if got := reqs[0].Header.Get("Authorization"); got != "Bearer u:p" {
		t.Errorf("auth %q", got)
	}
}

func TestMilvusSearch(t *testing.T) {
	s := newFBServer(t, func(r *http.Request, _ interface{}) (int, string) {
		return 200, `{"code":0,"data":[{"id":"a","distance":0.9,"text":"hello","metadata":{"src":"x"}}]}`
	})
	b := milvusFor(t, s.srv.URL, nil)
	hits, err := b.Search(context.Background(), []float64{0.1, 0.2}, 3, `metadata["src"] == "x"`)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != "a" || hits[0].Content != "hello" || hits[0].Score != 0.9 || hits[0].Metadata["src"] != "x" {
		t.Fatalf("hits %+v", hits)
	}
	r := s.requests()[0]
	if r.Path != "/v2/vectordb/entities/search" {
		t.Fatalf("path %s", r.Path)
	}
	fbEqualJSON(t, "search", r.Body, `{"annsField":"vector","collectionName":"docs","data":[[0.1,0.2]],"filter":"metadata[\"src\"] == \"x\"","limit":3,"outputFields":["text","metadata"]}`)

	if _, err := b.Search(context.Background(), []float64{1}, 1, map[string]interface{}{"a": 1}); err == nil || !strings.Contains(err.Error(), "boolean expression") {
		t.Errorf("map filter: want expression error, got %v", err)
	}
}

func TestMilvusDeleteVariants(t *testing.T) {
	s := newFBServer(t, func(r *http.Request, _ interface{}) (int, string) {
		if r.URL.Path == "/v2/vectordb/entities/delete" {
			return 200, `{"code":0,"data":{"deleteCount":2}}`
		}
		return 200, `{"code":0,"data":{}}`
	})
	b := milvusFor(t, s.srv.URL, nil)
	n, err := b.Delete(context.Background(), []string{"a", `b"c`}, `x > 1`, false)
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	n, err = b.Delete(context.Background(), nil, nil, true)
	if err != nil || n != -1 {
		t.Fatalf("all: n=%d err=%v", n, err)
	}
	reqs := s.requests()
	fbEqualJSON(t, "delete", reqs[0].Body, `{"collectionName":"docs","filter":"id in [\"a\",\"b\\\"c\"] and (x \u003e 1)"}`)
	if reqs[1].Path != "/v2/vectordb/collections/drop" {
		t.Errorf("drop path %s", reqs[1].Path)
	}
	fbEqualJSON(t, "drop", reqs[1].Body, `{"collectionName":"docs"}`)
	if _, err := b.Delete(context.Background(), nil, nil, false); err == nil {
		t.Error("want error with no ids and no filter")
	}
}

func TestMilvusErrors(t *testing.T) {
	s := newFBServer(t, func(r *http.Request, _ interface{}) (int, string) {
		if r.URL.Path == "/v2/vectordb/entities/search" {
			return 200, `{"code":1100,"message":"collection not found"}`
		}
		return 500, `boom`
	})
	b := milvusFor(t, s.srv.URL, nil)
	if _, err := b.Search(context.Background(), []float64{1}, 1, nil); err == nil || !strings.Contains(err.Error(), "code 1100: collection not found") {
		t.Errorf("code error: %v", err)
	}
	if err := b.Upsert(context.Background(), []Document{{ID: "a", Vector: []float64{1}}}); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("http error: %v", err)
	}
}

func TestMilvusStoreNodeEndToEnd(t *testing.T) {
	s := newFBServer(t, func(r *http.Request, _ interface{}) (int, string) {
		switch r.URL.Path {
		case "/v2/vectordb/collections/has":
			return 200, `{"code":0,"data":{"has":true}}`
		case "/v2/vectordb/entities/search":
			return 200, `{"code":0,"data":[{"id":"d1","distance":0.5,"text":"t","metadata":{}}]}`
		}
		return 200, `{"code":0,"data":{}}`
	})
	n := &StoreNode{}
	in := workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(map[string]interface{}{
		"id": "d1", "pageContent": "t", "embedding": []interface{}{1.0, 0.0},
	})}}
	out, err := n.Execute(context.Background(), in, map[string]interface{}{
		"provider": "milvus", "mode": "insert", "collection": "docs", "url": s.srv.URL,
	})
	if err != nil || len(out[0].Items) != 1 || out[0].Items[0].JSON["id"] != "d1" {
		t.Fatalf("insert: %v %+v", err, out)
	}
	out, err = n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"provider": "milvus", "mode": "search", "collection": "docs", "url": s.srv.URL, "query_vector": []interface{}{1.0, 0.0},
	})
	if err != nil || len(out[0].Items) != 1 || out[0].Items[0].JSON["score"] != 0.5 {
		t.Fatalf("search: %v %+v", err, out)
	}
}
