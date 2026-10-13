package vector

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

func memRun(t *testing.T, cfg map[string]interface{}, items ...workflow.Item) ([]workflow.Item, error) {
	t.Helper()
	out, err := (&StoreNode{}).Execute(context.Background(), workflow.NodeInput{Items: items}, cfg)
	if err != nil {
		return nil, err
	}
	return out[0].Items, nil
}

func vecItem(id, text string, vec []interface{}, meta map[string]interface{}) workflow.Item {
	j := map[string]interface{}{"pageContent": text, "embedding": vec}
	if id != "" {
		j["id"] = id
	}
	if meta != nil {
		j["metadata"] = meta
	}
	return workflow.NewItem(j)
}

func TestMemoryStoreInsertSearchUpdateDelete(t *testing.T) {
	key := t.Name()
	base := map[string]interface{}{"provider": "memory", "memory_key": key}
	with := func(kv ...interface{}) map[string]interface{} {
		m := copyMap(base)
		for i := 0; i < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	}
	ins, err := memRun(t, with("mode", "insert"),
		vecItem("a", "apples", []interface{}{1.0, 0.0}, map[string]interface{}{"kind": "fruit"}),
		vecItem("b", "bolts", []interface{}{0.0, 1.0}, map[string]interface{}{"kind": "hardware"}),
		vecItem("", "apricots", []interface{}{0.9, 0.1}, map[string]interface{}{"kind": "fruit"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(ins) != 3 || ins[2].JSON["id"] == "" {
		t.Fatalf("insert output %v", ins)
	}

	hits, err := memRun(t, with("mode", "search", "query_vector", []interface{}{1.0, 0.0}, "top_k", 2.0))
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 || hits[0].JSON["id"] != "a" || hits[1].JSON["pageContent"] != "apricots" || hits[0].JSON["rank"] != 1 {
		t.Fatalf("hits %v", hits)
	}
	if s := hits[0].JSON["score"].(float64); s < 0.999 {
		t.Fatalf("score %v", s)
	}

	// n8n's "load" mode name is accepted; filter narrows by metadata.
	hits, _ = memRun(t, with("mode", "load", "query_vector", "[1,0]", "filter", `{"kind":"hardware"}`))
	if len(hits) != 1 || hits[0].JSON["id"] != "b" {
		t.Fatalf("filtered hits %v", hits)
	}

	if _, err := memRun(t, with("mode", "update"), vecItem("", "x", []interface{}{1.0, 1.0}, nil)); err == nil ||
		!strings.Contains(err.Error(), `update needs a document id in field "id"`) {
		t.Fatalf("update without id: %v", err)
	}
	if _, err := memRun(t, with("mode", "update"), vecItem("b", "bolts v2", []interface{}{0.0, 1.0}, nil)); err != nil {
		t.Fatal(err)
	}
	hits, _ = memRun(t, with("query_vector", []interface{}{0.0, 1.0}, "top_k", 1.0))
	if hits[0].JSON["pageContent"] != "bolts v2" {
		t.Fatalf("after update %v", hits)
	}

	del, err := memRun(t, with("mode", "delete", "filter", map[string]interface{}{"kind": "fruit"}))
	if err != nil || del[0].JSON["deleted"] != 2 {
		t.Fatalf("delete by filter %v %v", del, err)
	}
	del, _ = memRun(t, with("mode", "delete"), workflow.NewItem(map[string]interface{}{"id": "b"}))
	if del[0].JSON["deleted"] != 1 {
		t.Fatalf("delete by item id %v", del)
	}
	if _, err := memRun(t, with("mode", "delete")); err == nil || !strings.Contains(err.Error(), "nothing to delete") {
		t.Fatalf("empty delete: %v", err)
	}
}

func TestMemoryStoreClearAndDimensionMismatch(t *testing.T) {
	cfg := map[string]interface{}{"provider": "memory", "memory_key": t.Name(), "mode": "insert"}
	memRun(t, cfg, vecItem("a", "x", []interface{}{1.0, 0.0}, nil))
	cfg["clear"] = true
	memRun(t, cfg, vecItem("b", "y", []interface{}{1.0, 0.0, 0.0}, nil))
	hits, err := memRun(t, map[string]interface{}{"provider": "memory", "memory_key": t.Name(), "query_vector": []interface{}{1.0, 0.0, 0.0}})
	if err != nil || len(hits) != 1 || hits[0].JSON["id"] != "b" {
		t.Fatalf("clear did not empty the store: %v %v", hits, err)
	}
	_, err = memRun(t, map[string]interface{}{"provider": "memory", "memory_key": t.Name(), "query_vector": []interface{}{1.0}})
	if err == nil || !strings.Contains(err.Error(), "query vector has 1 dimensions") {
		t.Fatalf("mismatch: %v", err)
	}
}

// TestStoreNestedEmbedding: items without vectors are embedded through the
// inline embedding block (documents) and a text query is embedded as a query.
func TestStoreNestedEmbedding(t *testing.T) {
	var inputTypes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var b map[string]interface{}
		json.Unmarshal(raw, &b)
		inputTypes = append(inputTypes, b["input_type"].(string))
		var vecs [][]float64
		for _, s := range b["texts"].([]interface{}) {
			if strings.HasPrefix(s.(string), "a") {
				vecs = append(vecs, []float64{1, 0})
			} else {
				vecs = append(vecs, []float64{0, 1})
			}
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"embeddings": map[string]interface{}{"float": vecs}})
	}))
	defer srv.Close()
	emb := map[string]interface{}{"provider": "cohere", "api_key": "k", "base_url": srv.URL}
	cfg := map[string]interface{}{"provider": "memory", "memory_key": t.Name(), "mode": "insert", "embedding": emb}
	if _, err := memRun(t, cfg, workflow.NewItem(map[string]interface{}{"text": "apple"}), workflow.NewItem(map[string]interface{}{"text": "bolt"})); err != nil {
		t.Fatal(err)
	}
	hits, err := memRun(t, map[string]interface{}{"provider": "memory", "memory_key": t.Name(), "mode": "retrieve-as-tool", "top_k": "1", "embedding": emb},
		workflow.NewItem(map[string]interface{}{"chatInput": "bolts please"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].JSON["pageContent"] != "bolt" || hits[0].JSON["query"] != "bolts please" {
		t.Fatalf("hits %v", hits)
	}
	if strings.Join(inputTypes, ",") != "search_document,search_query" {
		t.Fatalf("input types %v", inputTypes)
	}
	_, err = memRun(t, map[string]interface{}{"provider": "memory", "mode": "insert"}, workflow.NewItem(map[string]interface{}{"text": "x"}))
	if err == nil || !strings.Contains(err.Error(), `item 0 has no vector in field "embedding" and no embedding provider is configured`) {
		t.Fatalf("no embedder: %v", err)
	}
}

func TestStoreConfigErrors(t *testing.T) {
	for _, c := range []struct {
		cfg  map[string]interface{}
		want string
	}{
		{map[string]interface{}{}, "provider is required"},
		{map[string]interface{}{"provider": "faiss", "collection": "c"}, `provider "faiss" is not supported`},
		{map[string]interface{}{"provider": "qdrant"}, "collection is required for provider qdrant"},
		{map[string]interface{}{"provider": "memory", "mode": "upsertish"}, `mode "upsertish" is not supported`},
		{map[string]interface{}{"provider": "memory", "top_k": -1.0}, "top_k must not be negative"},
		{map[string]interface{}{"provider": "memory", "embedding": map[string]interface{}{"provider": "cohere"}}, "embedding.api_key is required"},
		{map[string]interface{}{"provider": "memory", "query": "hi"}, "a text query needs an embedding provider"},
	} {
		_, err := memRun(t, c.cfg)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: err %v, want %q", c.cfg, err, c.want)
		}
	}
}

func TestStoreQueryTemplateIsPerItem(t *testing.T) {
	if got := (&StoreNode{}).PerItemConfigFields(); !reflect.DeepEqual(got, []string{"query"}) {
		t.Fatalf("per-item fields %v", got)
	}
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var b map[string]interface{}
		json.Unmarshal(raw, &b)
		seen = append(seen, b["input"].([]interface{})[0].(string))
		json.NewEncoder(w).Encode(map[string]interface{}{"embeddings": [][]float64{{1, 0}}})
	}))
	defer srv.Close()
	memRun(t, map[string]interface{}{"provider": "memory", "memory_key": t.Name(), "mode": "insert"}, vecItem("a", "x", []interface{}{1.0, 0.0}, nil))
	_, err := memRun(t, map[string]interface{}{"provider": "memory", "memory_key": t.Name(), "query": "about {{ $json.topic }}",
		"embedding": map[string]interface{}{"provider": "ollama", "base_url": srv.URL}},
		workflow.NewItem(map[string]interface{}{"topic": "cats"}), workflow.NewItem(map[string]interface{}{"topic": "dogs"}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(seen, "|") != "about cats|about dogs" {
		t.Fatalf("queries %v", seen)
	}
}
