package vector

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

func azureCfg(url string, extra map[string]interface{}) map[string]interface{} {
	cfg := map[string]interface{}{"endpoint": url, "api_key": "admin-key"}
	for k, v := range extra {
		cfg[k] = v
	}
	return cfg
}

func openAzure(t *testing.T, cfg map[string]interface{}) *azureSearchBackend {
	t.Helper()
	be, err := newAzureSearch(storeTarget{cfg: cfg, collection: "n8n-vectorstore"})
	if err != nil {
		t.Fatalf("newAzureSearch: %v", err)
	}
	return be.(*azureSearchBackend)
}

func TestAzureSearchUpsertCreatesIndexAndFlattensMetadata(t *testing.T) {
	s := newForkAServer(t, func(c forkACall) (int, string) {
		if c.Method == "GET" {
			return 404, `{"error":{"message":"no index"}}`
		}
		if strings.HasSuffix(c.Path, "/docs/index") {
			return 200, `{"value":[{"key":"a","status":true}]}`
		}
		return 201, `{}`
	})
	be := openAzure(t, azureCfg(s.srv.URL, map[string]interface{}{"metadata_keys": "source,lang"}))
	docs := []Document{{ID: "a", Content: "alpha", Vector: []float64{0.5, 1},
		Metadata: map[string]interface{}{"source": "a.pdf", "lang": "en", "page": 3}}}
	if err := be.Upsert(context.Background(), docs); err != nil {
		t.Fatal(err)
	}
	calls := s.all()
	if len(calls) != 3 {
		t.Fatalf("calls = %+v", calls)
	}
	for _, c := range calls {
		if c.Header.Get("api-key") != "admin-key" || c.Query != "api-version=2024-07-01" || !strings.HasPrefix(c.Path, "/indexes/n8n-vectorstore") {
			t.Errorf("%s %s?%s api-key=%q", c.Method, c.Path, c.Query, c.Header.Get("api-key"))
		}
	}
	create := calls[1]
	if create.Method != "PUT" {
		t.Fatalf("second call %s, want PUT index", create.Method)
	}
	fields := create.Body["fields"].([]interface{})
	vec := fields[2].(map[string]interface{})
	if vec["name"] != "content_vector" || vec["dimensions"] != 2.0 || vec["vectorSearchProfile"] != "vector-search-profile" {
		t.Errorf("vector field = %v", vec)
	}
	doc := calls[2].Body["value"].([]interface{})[0].(map[string]interface{})
	wantMeta := map[string]interface{}{"source": "a.pdf", "attributes": []interface{}{
		map[string]interface{}{"key": "source", "value": "a.pdf"},
		map[string]interface{}{"key": "lang", "value": "en"},
	}}
	if doc["@search.action"] != "mergeOrUpload" || doc["id"] != "a" || doc["content"] != "alpha" || !reflect.DeepEqual(doc["metadata"], wantMeta) {
		t.Errorf("document = %v", doc)
	}

	if err := be.Upsert(context.Background(), []Document{{ID: "bad id/1", Vector: []float64{1}}}); err == nil ||
		!strings.Contains(err.Error(), "not a valid Azure AI Search key") {
		t.Errorf("invalid key: %v", err)
	}
}

func TestAzureSearchIndexFailureNamesTheDocument(t *testing.T) {
	s := newForkAServer(t, func(c forkACall) (int, string) {
		if c.Method == "GET" {
			return 200, `{}`
		}
		return 207, `{"value":[{"key":"a","status":false,"errorMessage":"too large"}]}`
	})
	err := openAzure(t, azureCfg(s.srv.URL, nil)).Upsert(context.Background(), []Document{{ID: "a", Vector: []float64{1}}})
	if err == nil || !strings.Contains(err.Error(), `document "a": too large`) {
		t.Fatalf("err = %v", err)
	}
}

func TestAzureSearchQueryTypes(t *testing.T) {
	result := `{"value":[{"id":"a","content":"alpha","@search.score":0.9,"@search.rerankerScore":2.5,
		"metadata":{"source":"a.pdf","attributes":[{"key":"lang","value":"en"}]}}]}`
	cases := []struct {
		queryType string
		want      map[string]interface{}
		score     float64
	}{
		{"vector", map[string]interface{}{}, 2.5},
		{"hybrid", map[string]interface{}{"search": "apples"}, 2.5},
		{"semanticHybrid", map[string]interface{}{"search": "apples", "queryType": "semantic", "semanticConfiguration": "sem"}, 2.5},
	}
	for _, c := range cases {
		t.Run(c.queryType, func(t *testing.T) {
			s := newForkAServer(t, func(forkACall) (int, string) { return 200, result })
			be := openAzure(t, azureCfg(s.srv.URL, map[string]interface{}{"query_type": c.queryType, "semantic_configuration": "sem"}))
			hits, err := be.SearchText(context.Background(), "apples", []float64{1, 0}, 3, "metadata/source eq 'a.pdf'")
			if err != nil {
				t.Fatal(err)
			}
			body := s.all()[0].Body
			if s.all()[0].Path != "/indexes/n8n-vectorstore/docs/search" || body["filter"] != "metadata/source eq 'a.pdf'" || body["top"] != 3.0 {
				t.Errorf("request = %s %v", s.all()[0].Path, body)
			}
			vq := body["vectorQueries"].([]interface{})[0].(map[string]interface{})
			if vq["fields"] != "content_vector" || vq["k"] != 3.0 || vq["kind"] != "vector" {
				t.Errorf("vector query = %v", vq)
			}
			for _, k := range []string{"search", "queryType", "semanticConfiguration"} {
				if body[k] != c.want[k] {
					t.Errorf("%s = %v, want %v", k, body[k], c.want[k])
				}
			}
			want := []Document{{ID: "a", Content: "alpha", Score: c.score, Metadata: map[string]interface{}{"source": "a.pdf", "lang": "en"}}}
			if !reflect.DeepEqual(hits, want) {
				t.Errorf("hits = %+v", hits)
			}
		})
	}
	be := openAzure(t, azureCfg("http://unused", nil))
	if _, err := be.Search(context.Background(), []float64{1}, 1, map[string]interface{}{"a": 1}); err == nil || !strings.Contains(err.Error(), "OData") {
		t.Errorf("object filter: %v", err)
	}
	if _, err := newAzureSearch(storeTarget{cfg: azureCfg("http://x", map[string]interface{}{"query_type": "keyword"}), collection: "i"}); err == nil {
		t.Error("unknown query_type accepted")
	}
	if _, err := newAzureSearch(storeTarget{cfg: map[string]interface{}{"endpoint": "http://x"}, collection: "i"}); err == nil || !strings.Contains(err.Error(), "api_key") {
		t.Errorf("missing key: %v", err)
	}
}

func TestAzureSearchDeleteVariants(t *testing.T) {
	searches := 0
	s := newForkAServer(t, func(c forkACall) (int, string) {
		switch {
		case strings.HasSuffix(c.Path, "/docs/search"):
			searches++
			// The deleted documents still show up once (indexing delay).
			if searches <= 2 {
				return 200, `{"value":[{"id":"x"},{"id":"y"}]}`
			}
			return 200, `{"value":[]}`
		case strings.HasSuffix(c.Path, "/docs/index"):
			return 200, `{"value":[]}`
		}
		return 204, ``
	})
	be := openAzure(t, azureCfg(s.srv.URL, nil))
	n, err := be.Delete(context.Background(), []string{"a"}, "metadata/source eq 'old'", false)
	if err != nil || n != 3 {
		t.Fatalf("delete = %d, %v", n, err)
	}
	calls := s.all()
	if len(calls) != 4 {
		t.Fatalf("calls = %+v", calls)
	}
	del := calls[0].Body["value"].([]interface{})[0].(map[string]interface{})
	if del["@search.action"] != "delete" || del["id"] != "a" {
		t.Errorf("delete by id = %v", calls[0].Body)
	}
	if calls[1].Body["filter"] != "metadata/source eq 'old'" || calls[1].Body["select"] != "id" {
		t.Errorf("filter search = %v", calls[1].Body)
	}
	if got := len(calls[2].Body["value"].([]interface{})); got != 2 {
		t.Errorf("filter delete sent %d keys", got)
	}

	s2 := newForkAServer(t, func(forkACall) (int, string) { return 404, `` })
	if _, err := openAzure(t, azureCfg(s2.srv.URL, nil)).Delete(context.Background(), nil, nil, true); err != nil {
		t.Fatalf("clear missing index: %v", err)
	}
	if c := s2.all()[0]; c.Method != "DELETE" || c.Path != "/indexes/n8n-vectorstore" {
		t.Errorf("clear = %s %s", c.Method, c.Path)
	}
}

func TestAzureSearchStoreNodeVectorQuery(t *testing.T) {
	s := newForkAServer(t, func(forkACall) (int, string) {
		return 200, `{"value":[{"id":"a","content":"alpha","@search.score":0.8}]}`
	})
	out, err := (&StoreNode{}).Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"provider": "azure_ai_search", "mode": "load", "index": "docs", "endpoint": s.srv.URL, "api_key": "k",
		"query_vector": "[1,0]", "top_k": 1,
	})
	if err != nil || len(out[0].Items) != 1 || out[0].Items[0].JSON["score"] != 0.8 {
		t.Fatalf("search: %v %+v", err, out)
	}
	if body := s.all()[0].Body; body["search"] != nil {
		t.Errorf("a vector query sent search text: %v", body)
	}
}
