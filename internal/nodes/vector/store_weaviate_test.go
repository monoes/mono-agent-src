package vector

import (
	"context"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

func TestWeaviateUpsert(t *testing.T) {
	s := newForkAServer(t, func(c forkACall) (int, string) { return 200, `[{"id":"x","result":{}}]` })
	be, err := newWeaviate(storeTarget{cfg: map[string]interface{}{"url": s.srv.URL, "api_key": "wk"}, collection: "docs"})
	if err != nil {
		t.Fatal(err)
	}
	err = be.Upsert(context.Background(), []Document{{
		ID: "d1", Content: "hello", Vector: []float64{1, 2},
		Metadata: map[string]interface{}{"source": "f.md", "tags": []interface{}{"a"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	c := s.all()[0]
	if c.Method != "POST" || c.Path != "/v1/batch/objects" || c.Header.Get("Authorization") != "Bearer wk" {
		t.Errorf("call = %s %s %s", c.Method, c.Path, c.Header.Get("Authorization"))
	}
	sid, _ := storageID("d1", false)
	want := `{"objects":[{"class":"Docs","id":"` + sid.(string) + `","properties":{"doc_id":"d1","source":"f.md","tags":"[\"a\"]","text":"hello"},"vector":[1,2]}]}`
	if got := mustJSON(t, c.Body); got != want {
		t.Errorf("body =\n%s\nwant\n%s", got, want)
	}
}

func TestWeaviateUpsertObjectError(t *testing.T) {
	s := newForkAServer(t, func(c forkACall) (int, string) {
		return 200, `[{"id":"x","result":{"errors":{"error":[{"message":"vector length mismatch"}]}}}]`
	})
	be, _ := newWeaviate(storeTarget{cfg: map[string]interface{}{"url": s.srv.URL}, collection: "Docs"})
	err := be.Upsert(context.Background(), []Document{{ID: "d1", Vector: []float64{1}}})
	if err == nil || !strings.Contains(err.Error(), "vector length mismatch") {
		t.Fatalf("err = %v", err)
	}
}

func TestWeaviateSearch(t *testing.T) {
	s := newForkAServer(t, func(c forkACall) (int, string) {
		return 200, `{"data":{"Get":{"Docs":[{"text":"hello","doc_id":"d1","source":"f.md","_additional":{"id":"u1","distance":0.25}}]}}}`
	})
	be, _ := newWeaviate(storeTarget{cfg: map[string]interface{}{"url": s.srv.URL, "metadata_keys": "source"}, collection: "docs"})
	docs, err := be.Search(context.Background(), []float64{0.5, 1}, 2, `{path:["source"], operator:Equal, valueText:"f.md"}`)
	if err != nil {
		t.Fatal(err)
	}
	c := s.all()[0]
	wantQ := `{ Get { Docs(nearVector:{vector:[0.5,1]} limit:2 where: {path:["source"], operator:Equal, valueText:"f.md"}) { text doc_id source _additional { id distance } } } }`
	if c.Path != "/v1/graphql" || c.Body["query"] != wantQ {
		t.Errorf("query =\n%v\nwant\n%s", c.Body["query"], wantQ)
	}
	if len(docs) != 1 || docs[0].ID != "d1" || docs[0].Content != "hello" || docs[0].Score != 0.75 || docs[0].Metadata["source"] != "f.md" {
		t.Errorf("docs = %+v", docs)
	}
	if _, err := be.Search(context.Background(), []float64{1}, 1, map[string]interface{}{"a": 1}); err == nil || !strings.Contains(err.Error(), "GraphQL") {
		t.Errorf("map filter err = %v", err)
	}
}

func TestWeaviateSearchGraphQLErrorAndStatus(t *testing.T) {
	status := 200
	s := newForkAServer(t, func(c forkACall) (int, string) {
		return status, `{"errors":[{"message":"Cannot query field"}]}`
	})
	be, _ := newWeaviate(storeTarget{cfg: map[string]interface{}{"url": s.srv.URL}, collection: "Docs"})
	if _, err := be.Search(context.Background(), []float64{1}, 1, nil); err == nil || !strings.Contains(err.Error(), "Cannot query field") {
		t.Errorf("err = %v", err)
	}
	status = 401
	if _, err := be.Search(context.Background(), []float64{1}, 1, nil); err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Errorf("err = %v", err)
	}
	if _, err := newWeaviate(storeTarget{cfg: map[string]interface{}{}, collection: "Docs"}); err == nil || !strings.Contains(err.Error(), "url") {
		t.Errorf("err = %v", err)
	}
}

func TestWeaviateDeleteVariants(t *testing.T) {
	s := newForkAServer(t, func(c forkACall) (int, string) {
		switch {
		case c.Method == "DELETE" && strings.HasPrefix(c.Path, "/v1/objects/"):
			if strings.HasSuffix(c.Path, "/00000000-0000-4000-8000-000000000002") {
				return 404, `{}`
			}
			return 204, ``
		case c.Path == "/v1/batch/objects":
			return 200, `{"results":{"matches":3,"successful":3}}`
		}
		return 200, `{}`
	})
	be, _ := newWeaviate(storeTarget{cfg: map[string]interface{}{"url": s.srv.URL}, collection: "docs"})
	ctx := context.Background()
	n, err := be.Delete(ctx, []string{"00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000002"}, nil, false)
	if err != nil || n != 1 {
		t.Fatalf("ids delete n=%d err=%v", n, err)
	}
	where := map[string]interface{}{"path": []interface{}{"source"}, "operator": "Equal", "valueText": "f"}
	if n, err = be.Delete(ctx, nil, where, false); err != nil || n != 3 {
		t.Fatalf("filter delete n=%d err=%v", n, err)
	}
	if n, err = be.Delete(ctx, nil, nil, true); err != nil || n != -1 {
		t.Fatalf("all delete n=%d err=%v", n, err)
	}
	if _, err = be.Delete(ctx, nil, "path:x", false); err == nil {
		t.Error("string filter on delete should fail")
	}
	calls := s.all()
	if calls[0].Path != "/v1/objects/Docs/00000000-0000-4000-8000-000000000001" {
		t.Errorf("delete path = %s", calls[0].Path)
	}
	if calls[2].Method != "DELETE" || calls[2].Path != "/v1/batch/objects" ||
		mustJSON(t, calls[2].Body) != `{"match":{"class":"Docs","where":{"operator":"Equal","path":["source"],"valueText":"f"}}}` {
		t.Errorf("batch delete = %s %s", calls[2].Path, mustJSON(t, calls[2].Body))
	}
	if calls[3].Method != "DELETE" || calls[3].Path != "/v1/schema/Docs" {
		t.Errorf("drop = %s %s", calls[3].Method, calls[3].Path)
	}
}

func TestWeaviateStoreNodeEndToEnd(t *testing.T) {
	s := newForkAServer(t, func(c forkACall) (int, string) {
		if c.Path == "/v1/graphql" {
			return 200, `{"data":{"Get":{"Kb":[{"text":"doc","doc_id":"d1","_additional":{"id":"u","distance":0.1}}]}}}`
		}
		return 200, `[]`
	})
	node := &StoreNode{}
	cfg := map[string]interface{}{"provider": "weaviate", "mode": "insert", "collection": "kb", "url": s.srv.URL}
	in := workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(map[string]interface{}{"id": "d1", "pageContent": "doc", "embedding": []interface{}{0.3}})}}
	if _, err := node.Execute(context.Background(), in, cfg); err != nil {
		t.Fatal(err)
	}
	cfg["mode"] = "search"
	cfg["query_vector"] = []interface{}{0.3}
	out, err := node.Execute(context.Background(), workflow.NodeInput{}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	j := out[0].Items[0].JSON
	if j["id"] != "d1" || j["pageContent"] != "doc" {
		t.Errorf("search item = %v", j)
	}
}
