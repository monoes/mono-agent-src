package vector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

const dbxDirect = `{"name":"main.rag.docs","primary_key":"id","index_type":"DIRECT_ACCESS",
	"direct_access_index_spec":{"embedding_vector_columns":[{"name":"vec","embedding_dimension":2}],
	"schema_json":"{\"id\":\"string\",\"body\":\"string\",\"vec\":\"array<float>\",\"source\":\"string\"}"}}`

const dbxManaged = `{"name":"main.rag.docs","primary_key":"id","index_type":"DELTA_SYNC",
	"delta_sync_index_spec":{"embedding_source_columns":[{"name":"body"}],"columns_to_sync":["id","body","source"]}}`

func dbxServer(t *testing.T, describe string, query string) *forkAServer {
	return newForkAServer(t, func(c forkACall) (int, string) {
		switch {
		case c.Method == "GET":
			return 200, describe
		case strings.HasSuffix(c.Path, "/query"):
			return 200, query
		}
		return 200, `{"status":"SUCCESS","result":{"success_row_count":1}}`
	})
}

func openDBX(t *testing.T, url string, extra map[string]interface{}) *databricksBackend {
	t.Helper()
	cfg := map[string]interface{}{"host": url, "access_token": "dapi-1"}
	for k, v := range extra {
		cfg[k] = v
	}
	be, err := newDatabricks(storeTarget{cfg: cfg, collection: "main.rag.docs"})
	if err != nil {
		t.Fatalf("newDatabricks: %v", err)
	}
	return be.(*databricksBackend)
}

func TestDatabricksUpsertKeepsSchemaColumns(t *testing.T) {
	s := dbxServer(t, dbxDirect, "")
	be := openDBX(t, s.srv.URL, map[string]interface{}{"content_column": "body"})
	docs := []Document{{ID: "a", Content: "alpha", Vector: []float64{1, 0}, Metadata: map[string]interface{}{"source": "s.pdf", "loc": 3}}}
	if err := be.Upsert(context.Background(), docs); err != nil {
		t.Fatal(err)
	}
	calls := s.all()
	if len(calls) != 2 || calls[0].Path != "/api/2.0/vector-search/indexes/main.rag.docs" ||
		calls[1].Method != "POST" || calls[1].Path != "/api/2.0/vector-search/indexes/main.rag.docs/upsert-data" {
		t.Fatalf("calls = %+v", calls)
	}
	if calls[1].Header.Get("Authorization") != "Bearer dapi-1" {
		t.Errorf("auth = %q", calls[1].Header.Get("Authorization"))
	}
	var rows []map[string]interface{}
	if err := json.Unmarshal([]byte(calls[1].Body["inputs_json"].(string)), &rows); err != nil {
		t.Fatal(err)
	}
	want := []map[string]interface{}{{"id": "a", "body": "alpha", "vec": []interface{}{1.0, 0.0}, "source": "s.pdf"}}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("rows = %v", rows)
	}
}

func TestDatabricksWriteErrors(t *testing.T) {
	s := dbxServer(t, dbxManaged, "")
	if err := openDBX(t, s.srv.URL, nil).Upsert(context.Background(), []Document{{ID: "a"}}); err == nil || !strings.Contains(err.Error(), "Direct Access") {
		t.Errorf("managed index upsert: %v", err)
	}
	s2 := newForkAServer(t, func(c forkACall) (int, string) {
		if c.Method == "GET" {
			return 200, dbxDirect
		}
		return 200, `{"status":"PARTIAL_SUCCESS","result":{"failed_primary_keys":["a"]}}`
	})
	err := openDBX(t, s2.srv.URL, map[string]interface{}{"content_column": "body"}).Upsert(context.Background(), []Document{{ID: "a", Vector: []float64{1}}})
	if err == nil || !strings.Contains(err.Error(), "failed primary keys: a") {
		t.Errorf("partial upsert: %v", err)
	}
	if err := openDBX(t, s2.srv.URL, nil).Upsert(context.Background(), []Document{{ID: "a"}}); err == nil || !strings.Contains(err.Error(), "content_column") {
		t.Errorf("no content column: %v", err)
	}
	if _, err := openDBX(t, s2.srv.URL, nil).Delete(context.Background(), nil, nil, true); err == nil || !strings.Contains(err.Error(), "primary key only") {
		t.Errorf("delete all: %v", err)
	}
}

func TestDatabricksQuery(t *testing.T) {
	result := `{"manifest":{"columns":[{"name":"id"},{"name":"body"},{"name":"source"},{"name":"score"}]},
		"result":{"data_array":[["a","alpha","s.pdf",0.91]]}}`
	want := []Document{{ID: "a", Content: "alpha", Score: 0.91, Metadata: map[string]interface{}{"source": "s.pdf"}}}

	// Self-managed vectors, hybrid: vector plus text.
	s := dbxServer(t, dbxDirect, result)
	be := openDBX(t, s.srv.URL, map[string]interface{}{"content_column": "body", "search_mode": "HYBRID"})
	hits, err := be.SearchText(context.Background(), "apples", []float64{1, 0}, 2, map[string]interface{}{"source": "s.pdf"})
	if err != nil || !reflect.DeepEqual(hits, want) {
		t.Fatalf("hits = %+v, %v", hits, err)
	}
	body := s.all()[1].Body
	wantBody := map[string]interface{}{"num_results": 2.0, "query_type": "HYBRID", "query_vector": []interface{}{1.0, 0.0},
		"query_text": "apples", "columns": []interface{}{"id", "body", "source"}, "filters_json": `{"source":"s.pdf"}`}
	if !reflect.DeepEqual(body, wantBody) {
		t.Errorf("query body = %v", body)
	}

	// Managed embeddings through StoreNode: no embedding provider needed.
	s2 := dbxServer(t, dbxManaged, result)
	out, err := (&StoreNode{}).Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"provider": "databricks", "mode": "load", "collection": "main.rag.docs", "host": s2.srv.URL, "access_token": "t",
		"query": "apples", "top_k": 1,
	})
	if err != nil || len(out[0].Items) != 1 || out[0].Items[0].JSON["pageContent"] != "alpha" {
		t.Fatalf("managed search: %v %+v", err, out)
	}
	if b := s2.all()[1].Body; b["query_text"] != "apples" || b["query_vector"] != nil || b["query_type"] != "ANN" {
		t.Errorf("managed body = %v", b)
	}

	// A self-managed index without an embedder cannot take text.
	s3 := dbxServer(t, dbxDirect, result)
	_, err = openDBX(t, s3.srv.URL, map[string]interface{}{"content_column": "body"}).SearchText(context.Background(), "q", nil, 1, nil)
	if err == nil || !strings.Contains(err.Error(), "embedding.provider") {
		t.Errorf("self index without vector: %v", err)
	}
}

func TestDatabricksDeleteAndServicePrincipal(t *testing.T) {
	var tokenForm string
	var tokenAuth bool
	var got *http.Request
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oidc/v1/token" {
			r.ParseForm()
			tokenForm = r.PostForm.Encode()
			u, p, ok := r.BasicAuth()
			tokenAuth = ok && u == "cid" && p == "sec"
			w.Write([]byte(`{"access_token":"sp-token","token_type":"Bearer"}`))
			return
		}
		got = r.Clone(context.Background())
		w.Write([]byte(`{"status":"SUCCESS"}`))
	}))
	defer api.Close()
	be, err := newDatabricks(storeTarget{cfg: map[string]interface{}{"host": api.URL, "client_id": "cid", "client_secret": "sec"}, collection: "main.rag.docs"})
	if err != nil {
		t.Fatal(err)
	}
	n, err := be.Delete(context.Background(), []string{"a", "b"}, nil, false)
	if err != nil || n != 2 {
		t.Fatalf("delete = %d, %v", n, err)
	}
	if !tokenAuth || tokenForm != "grant_type=client_credentials&scope=all-apis" {
		t.Errorf("token request: auth %v form %q", tokenAuth, tokenForm)
	}
	if got.Method != "DELETE" || got.URL.Path != "/api/2.0/vector-search/indexes/main.rag.docs/delete-data" ||
		got.URL.RawQuery != "primary_keys=a&primary_keys=b" || got.Header.Get("Authorization") != "Bearer sp-token" {
		t.Errorf("delete request = %s %s?%s auth %q", got.Method, got.URL.Path, got.URL.RawQuery, got.Header.Get("Authorization"))
	}
	if _, err := newDatabricks(storeTarget{cfg: map[string]interface{}{"host": "http://x"}, collection: "c"}); err == nil {
		t.Error("no credentials accepted")
	}
}
