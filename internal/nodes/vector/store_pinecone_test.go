package vector

import (
	"context"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

func TestPineconeDiscoversHostAndUpserts(t *testing.T) {
	var data *forkAServer
	control := newForkAServer(t, func(c forkACall) (int, string) {
		return 200, `{"name":"kb","host":"` + data.srv.URL + `"}`
	})
	data = newForkAServer(t, func(c forkACall) (int, string) { return 200, `{"upsertedCount":1}` })
	be, err := newPinecone(context.Background(), storeTarget{
		cfg:        map[string]interface{}{"api_key": "pk", "control_url": control.srv.URL},
		collection: "kb", namespace: "ns1",
	})
	if err != nil {
		t.Fatal(err)
	}
	cc := control.all()[0]
	if cc.Method != "GET" || cc.Path != "/indexes/kb" || cc.Header.Get("Api-Key") != "pk" || cc.Header.Get("X-Pinecone-API-Version") != "2025-04" {
		t.Errorf("describe = %+v", cc)
	}
	err = be.Upsert(context.Background(), []Document{{ID: "a", Content: "hello", Metadata: map[string]interface{}{"src": "f"}, Vector: []float64{1, 2}}})
	if err != nil {
		t.Fatal(err)
	}
	c := data.all()[0]
	if c.Method != "POST" || c.Path != "/vectors/upsert" || c.Header.Get("Api-Key") != "pk" {
		t.Errorf("upsert = %s %s", c.Method, c.Path)
	}
	if got := mustJSON(t, c.Body); got != `{"namespace":"ns1","vectors":[{"id":"a","metadata":{"src":"f","text":"hello"},"values":[1,2]}]}` {
		t.Errorf("body = %s", got)
	}
}

func TestPineconeIndexHostBare(t *testing.T) {
	be, err := newPinecone(context.Background(), storeTarget{cfg: map[string]interface{}{"api_key": "pk", "index_host": "kb-abc.svc.pinecone.io/"}, collection: "kb"})
	if err != nil {
		t.Fatal(err)
	}
	if h := be.(*pineconeBackend).host; h != "https://kb-abc.svc.pinecone.io" {
		t.Errorf("host = %s", h)
	}
	if _, err := newPinecone(context.Background(), storeTarget{cfg: map[string]interface{}{}, collection: "kb"}); err == nil || !strings.Contains(err.Error(), "api_key") {
		t.Errorf("err = %v", err)
	}
}

func TestPineconeSearch(t *testing.T) {
	s := newForkAServer(t, func(c forkACall) (int, string) {
		return 200, `{"matches":[{"id":"a","score":0.8,"metadata":{"body":"hi","src":"f"}}]}`
	})
	be, _ := newPinecone(context.Background(), storeTarget{cfg: map[string]interface{}{"api_key": "pk", "index_host": s.srv.URL, "text_key": "body"}, collection: "kb", namespace: "n"})
	docs, err := be.Search(context.Background(), []float64{0.5}, 5, map[string]interface{}{"src": map[string]interface{}{"$eq": "f"}})
	if err != nil {
		t.Fatal(err)
	}
	c := s.all()[0]
	if c.Path != "/query" || mustJSON(t, c.Body) != `{"filter":{"src":{"$eq":"f"}},"includeMetadata":true,"includeValues":false,"namespace":"n","topK":5,"vector":[0.5]}` {
		t.Errorf("query = %s %s", c.Path, mustJSON(t, c.Body))
	}
	if len(docs) != 1 || docs[0].Content != "hi" || docs[0].Score != 0.8 || mustJSON(t, docs[0].Metadata) != `{"src":"f"}` {
		t.Errorf("docs = %+v", docs)
	}
}

func TestPineconeDeleteVariantsAndError(t *testing.T) {
	fail := false
	s := newForkAServer(t, func(c forkACall) (int, string) {
		if fail {
			return 403, `forbidden`
		}
		return 200, `{}`
	})
	be, _ := newPinecone(context.Background(), storeTarget{cfg: map[string]interface{}{"api_key": "pk", "index_host": s.srv.URL}, collection: "kb", namespace: "n"})
	ctx := context.Background()
	if _, err := be.Delete(ctx, []string{"a", "b"}, nil, false); err != nil {
		t.Fatal(err)
	}
	if _, err := be.Delete(ctx, nil, map[string]interface{}{"src": "f"}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := be.Delete(ctx, nil, nil, true); err != nil {
		t.Fatal(err)
	}
	want := []string{`{"ids":["a","b"],"namespace":"n"}`, `{"filter":{"src":"f"},"namespace":"n"}`, `{"deleteAll":true,"namespace":"n"}`}
	for i, c := range s.all() {
		if c.Method != "POST" || c.Path != "/vectors/delete" || mustJSON(t, c.Body) != want[i] {
			t.Errorf("delete %d = %s %s %s", i, c.Method, c.Path, mustJSON(t, c.Body))
		}
	}
	fail = true
	if _, err := be.Delete(ctx, []string{"a"}, nil, false); err == nil || !strings.Contains(err.Error(), "HTTP 403") {
		t.Errorf("err = %v", err)
	}
}

func TestPineconeStoreNodeEndToEnd(t *testing.T) {
	s := newForkAServer(t, func(c forkACall) (int, string) {
		if c.Path == "/query" {
			return 200, `{"matches":[{"id":"d1","score":0.6,"metadata":{"text":"doc","k":"v"}}]}`
		}
		return 200, `{}`
	})
	node := &StoreNode{}
	cfg := map[string]interface{}{"provider": "pinecone", "mode": "insert", "index": "kb", "namespace": "n", "api_key": "pk", "index_host": s.srv.URL}
	in := workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(map[string]interface{}{"id": "d1", "text": "doc", "embedding": []interface{}{0.1, 0.2}})}}
	if _, err := node.Execute(context.Background(), in, cfg); err != nil {
		t.Fatal(err)
	}
	cfg["mode"] = "retrieve"
	cfg["query_vector"] = "[0.1,0.2]"
	out, err := node.Execute(context.Background(), workflow.NodeInput{}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	j := out[0].Items[0].JSON
	if j["id"] != "d1" || j["pageContent"] != "doc" || asMap(j["metadata"])["k"] != "v" {
		t.Errorf("search item = %v", j)
	}
}
