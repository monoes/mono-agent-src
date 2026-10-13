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

// forkACall is one request a fake server saw.
type forkACall struct {
	Method, Path, Query string
	Header              http.Header
	Body                map[string]interface{}
}

// forkAServer records requests and answers via respond (method+" "+path → status, body).
type forkAServer struct {
	mu    sync.Mutex
	calls []forkACall
	srv   *httptest.Server
}

func newForkAServer(t *testing.T, respond func(c forkACall) (int, string)) *forkAServer {
	t.Helper()
	s := &forkAServer{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c := forkACall{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Header: r.Header.Clone()}
		if len(b) > 0 {
			if err := json.Unmarshal(b, &c.Body); err != nil {
				t.Errorf("request body is not a JSON object: %s", b)
			}
		}
		s.mu.Lock()
		s.calls = append(s.calls, c)
		s.mu.Unlock()
		status, body := respond(c)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *forkAServer) all() []forkACall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]forkACall(nil), s.calls...)
}

func mustJSON(t *testing.T, v interface{}) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestQdrantUpsertCreatesCollectionAndMapsIDs(t *testing.T) {
	s := newForkAServer(t, func(c forkACall) (int, string) {
		if c.Method == "GET" {
			return 404, `{"status":{"error":"Not found"}}`
		}
		return 200, `{"result":true}`
	})
	be, err := newQdrant(storeTarget{cfg: map[string]interface{}{"url": s.srv.URL + "/", "api_key": "k1", "distance": "dot"}, collection: "docs"})
	if err != nil {
		t.Fatal(err)
	}
	docs := []Document{
		{ID: "6f1c7b0e-0000-4000-8000-000000000001", Content: "a", Metadata: map[string]interface{}{"src": "x"}, Vector: []float64{1, 2}},
		{ID: "42", Content: "b", Vector: []float64{3, 4}},
		{ID: "doc-7", Content: "c", Vector: []float64{5, 6}},
	}
	if err := be.Upsert(context.Background(), docs); err != nil {
		t.Fatal(err)
	}
	// second upsert must not re-check the collection
	if err := be.Upsert(context.Background(), docs[:1]); err != nil {
		t.Fatal(err)
	}
	calls := s.all()
	if len(calls) != 4 {
		t.Fatalf("want 4 calls, got %d: %+v", len(calls), calls)
	}
	if calls[0].Method != "GET" || calls[0].Path != "/collections/docs" || calls[0].Header.Get("api-key") != "k1" {
		t.Errorf("check call = %+v", calls[0])
	}
	if calls[1].Method != "PUT" || calls[1].Path != "/collections/docs" ||
		mustJSON(t, calls[1].Body) != `{"vectors":{"distance":"Dot","size":2}}` {
		t.Errorf("create call = %s %s %v", calls[1].Method, calls[1].Path, calls[1].Body)
	}
	up := calls[2]
	if up.Method != "PUT" || up.Path != "/collections/docs/points" || up.Query != "wait=true" {
		t.Errorf("upsert call = %s %s?%s", up.Method, up.Path, up.Query)
	}
	pts := up.Body["points"].([]interface{})
	p0, p1, p2 := pts[0].(map[string]interface{}), pts[1].(map[string]interface{}), pts[2].(map[string]interface{})
	if p0["id"] != docs[0].ID || asMap(p0["payload"])["content"] != "a" || asMap(asMap(p0["payload"])["metadata"])["src"] != "x" {
		t.Errorf("point 0 = %v", p0)
	}
	if _, has := asMap(p0["payload"])["doc_id"]; has {
		t.Error("uuid id should not get doc_id")
	}
	if p1["id"] != float64(42) {
		t.Errorf("uint id kept as number, got %v", p1["id"])
	}
	wantID, _ := storageID("doc-7", true)
	if p2["id"] != wantID || asMap(p2["payload"])["doc_id"] != "doc-7" {
		t.Errorf("point 2 = %v", p2)
	}
	if mustJSON(t, p2["vector"]) != "[5,6]" {
		t.Errorf("vector = %v", p2["vector"])
	}
}

func TestQdrantUpsertNoCreate(t *testing.T) {
	s := newForkAServer(t, func(c forkACall) (int, string) { return 404, `{}` })
	be, _ := newQdrant(storeTarget{cfg: map[string]interface{}{"url": s.srv.URL, "create_collection": false}, collection: "docs"})
	err := be.Upsert(context.Background(), []Document{{ID: "1", Vector: []float64{1}}})
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("err = %v", err)
	}
}

func TestQdrantSearch(t *testing.T) {
	s := newForkAServer(t, func(c forkACall) (int, string) {
		return 200, `{"result":[{"id":"u1","score":0.9,"payload":{"content":"hello","metadata":{"p":1},"doc_id":"orig"}},{"id":7,"score":0.5,"payload":{"content":"bye"}}]}`
	})
	be, _ := newQdrant(storeTarget{cfg: map[string]interface{}{"url": s.srv.URL}, collection: "docs"})
	filter := map[string]interface{}{"must": []interface{}{map[string]interface{}{"key": "metadata.p", "match": map[string]interface{}{"value": 1}}}}
	docs, err := be.Search(context.Background(), []float64{0.1, 0.2}, 3, filter)
	if err != nil {
		t.Fatal(err)
	}
	c := s.all()[0]
	if c.Method != "POST" || c.Path != "/collections/docs/points/search" {
		t.Errorf("call = %s %s", c.Method, c.Path)
	}
	if mustJSON(t, c.Body) != `{"filter":{"must":[{"key":"metadata.p","match":{"value":1}}]},"limit":3,"vector":[0.1,0.2],"with_payload":true}` {
		t.Errorf("body = %s", mustJSON(t, c.Body))
	}
	if len(docs) != 2 || docs[0].ID != "orig" || docs[0].Content != "hello" || docs[0].Score != 0.9 || docs[0].Metadata["p"] != float64(1) {
		t.Errorf("doc0 = %+v", docs[0])
	}
	if docs[1].ID != "7" || docs[1].Content != "bye" {
		t.Errorf("doc1 = %+v", docs[1])
	}
}

func TestQdrantDeleteVariants(t *testing.T) {
	s := newForkAServer(t, func(c forkACall) (int, string) {
		if c.Method == "DELETE" {
			return 404, `{}`
		}
		return 200, `{"result":{}}`
	})
	be, _ := newQdrant(storeTarget{cfg: map[string]interface{}{"url": s.srv.URL}, collection: "docs"})
	ctx := context.Background()
	if n, err := be.Delete(ctx, []string{"abc", "5"}, nil, false); err != nil || n != -1 {
		t.Fatal(n, err)
	}
	if _, err := be.Delete(ctx, nil, map[string]interface{}{"must": []interface{}{}}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := be.Delete(ctx, nil, nil, true); err != nil {
		t.Fatalf("404 on drop must be fine: %v", err)
	}
	calls := s.all()
	mapped, _ := storageID("abc", true)
	if calls[0].Path != "/collections/docs/points/delete" || calls[0].Query != "wait=true" ||
		mustJSON(t, calls[0].Body) != `{"points":["`+mapped.(string)+`",5]}` {
		t.Errorf("ids delete = %s %s", calls[0].Path, mustJSON(t, calls[0].Body))
	}
	if mustJSON(t, calls[1].Body) != `{"filter":{"must":[]}}` {
		t.Errorf("filter delete = %s", mustJSON(t, calls[1].Body))
	}
	if calls[2].Method != "DELETE" || calls[2].Path != "/collections/docs" {
		t.Errorf("drop = %s %s", calls[2].Method, calls[2].Path)
	}
}

func TestQdrantErrorStatusAndMissingURL(t *testing.T) {
	s := newForkAServer(t, func(c forkACall) (int, string) { return 500, `boom` })
	be, _ := newQdrant(storeTarget{cfg: map[string]interface{}{"url": s.srv.URL}, collection: "docs"})
	if _, err := be.Search(context.Background(), []float64{1}, 1, nil); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("err = %v", err)
	}
	if _, err := newQdrant(storeTarget{cfg: map[string]interface{}{}, collection: "docs"}); err == nil || !strings.Contains(err.Error(), "url") {
		t.Fatalf("err = %v", err)
	}
}

func TestQdrantStoreNodeEndToEnd(t *testing.T) {
	s := newForkAServer(t, func(c forkACall) (int, string) {
		if strings.HasSuffix(c.Path, "/search") {
			return 200, `{"result":[{"id":"x","score":0.7,"payload":{"content":"found","metadata":{},"doc_id":"d1"}}]}`
		}
		return 200, `{"result":true}`
	})
	node := &StoreNode{}
	in := workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(map[string]interface{}{
		"id": "d1", "pageContent": "found", "embedding": []interface{}{1.0, 0.0},
	})}}
	out, err := node.Execute(context.Background(), in, map[string]interface{}{
		"provider": "qdrant", "mode": "insert", "collection": "kb", "url": s.srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := out[0].Items[0].JSON["id"]; got != "d1" {
		t.Errorf("insert output id = %v", got)
	}
	out, err = node.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"provider": "qdrant", "mode": "search", "collection": "kb", "url": s.srv.URL, "query_vector": []interface{}{1.0, 0.0}, "top_k": 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	j := out[0].Items[0].JSON
	if j["id"] != "d1" || j["pageContent"] != "found" || j["score"] != 0.7 {
		t.Errorf("search item = %v", j)
	}
	last := s.all()[len(s.all())-1]
	if last.Body["limit"] != float64(2) {
		t.Errorf("top_k not passed: %v", last.Body)
	}
}
