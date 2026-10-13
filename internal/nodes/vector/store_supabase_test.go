package vector

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

func supabaseResponder(r *http.Request, _ interface{}) (int, string) {
	switch {
	case strings.HasPrefix(r.URL.Path, "/rest/v1/rpc/"):
		return 200, `[{"id":"a","content":"hello","metadata":{"src":"x"},"similarity":0.8}]`
	case r.Method == "DELETE":
		return 200, `[{"id":"a"},{"id":"b"}]`
	}
	return 201, ``
}

func supabaseFor(t *testing.T, srvURL string) backend {
	t.Helper()
	be, err := newSupabase(storeTarget{cfg: map[string]interface{}{"url": srvURL, "api_key": "srk"}, collection: "documents"})
	if err != nil {
		t.Fatal(err)
	}
	return be
}

func checkSupabaseAuth(t *testing.T, r fbReq) {
	t.Helper()
	if r.Header.Get("apikey") != "srk" || r.Header.Get("Authorization") != "Bearer srk" {
		t.Errorf("%s %s: apikey=%q auth=%q", r.Method, r.Path, r.Header.Get("apikey"), r.Header.Get("Authorization"))
	}
}

func TestSupabaseRequiredFields(t *testing.T) {
	if _, err := newSupabase(storeTarget{cfg: map[string]interface{}{"api_key": "k"}, collection: "d"}); err == nil || !strings.Contains(err.Error(), "url") {
		t.Errorf("missing url: %v", err)
	}
	if _, err := newSupabase(storeTarget{cfg: map[string]interface{}{"host": "https://x"}, collection: "d"}); err == nil || !strings.Contains(err.Error(), "api_key") {
		t.Errorf("missing key: %v", err)
	}
	if _, err := newSupabase(storeTarget{cfg: map[string]interface{}{"host": "https://x", "serviceRole": "k"}, collection: "d"}); err != nil {
		t.Errorf("n8n field names: %v", err)
	}
}

func TestSupabaseUpsert(t *testing.T) {
	s := newFBServer(t, supabaseResponder)
	be := supabaseFor(t, s.srv.URL)
	err := be.Upsert(context.Background(), []Document{{ID: "a", Content: "hello", Vector: []float64{1, 2}, Metadata: map[string]interface{}{"src": "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	r := s.requests()[0]
	checkSupabaseAuth(t, r)
	if r.Method != "POST" || r.Path != "/rest/v1/documents" {
		t.Errorf("%s %s", r.Method, r.Path)
	}
	if got := r.Header.Get("Prefer"); got != "resolution=merge-duplicates,return=minimal" {
		t.Errorf("Prefer %q", got)
	}
	fbEqualJSON(t, "upsert", r.Body, `[{"content":"hello","embedding":[1,2],"id":"a","metadata":{"src":"x"}}]`)
}

func TestSupabaseSearch(t *testing.T) {
	s := newFBServer(t, supabaseResponder)
	be, _ := newSupabase(storeTarget{cfg: map[string]interface{}{"url": s.srv.URL, "api_key": "srk", "query_name": "match_kb"}, collection: "documents"})
	hits, err := be.Search(context.Background(), []float64{0.5}, 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != "a" || hits[0].Content != "hello" || hits[0].Score != 0.8 || hits[0].Metadata["src"] != "x" {
		t.Fatalf("hits %+v", hits)
	}
	r := s.requests()[0]
	checkSupabaseAuth(t, r)
	if r.Method != "POST" || r.Path != "/rest/v1/rpc/match_kb" {
		t.Errorf("%s %s", r.Method, r.Path)
	}
	fbEqualJSON(t, "rpc", r.Body, `{"filter":{},"match_count":3,"query_embedding":[0.5]}`)

	if _, err := be.Search(context.Background(), []float64{1}, 1, map[string]interface{}{"src": "x"}); err != nil {
		t.Fatal(err)
	}
	fbEqualJSON(t, "rpc filter", s.requests()[1].Body, `{"filter":{"src":"x"},"match_count":1,"query_embedding":[1]}`)
}

func TestSupabaseDeleteVariants(t *testing.T) {
	s := newFBServer(t, supabaseResponder)
	be := supabaseFor(t, s.srv.URL)
	ctx := context.Background()
	if n, err := be.Delete(ctx, []string{"a", `b"c`}, nil, false); err != nil || n != 2 {
		t.Fatalf("ids: n=%d err=%v", n, err)
	}
	if _, err := be.Delete(ctx, nil, map[string]interface{}{"src": "x"}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := be.Delete(ctx, nil, nil, true); err != nil {
		t.Fatal(err)
	}
	reqs := s.requests()
	wantQ := []url.Values{
		{"id": {`in.("a","b\"c")`}},
		{"metadata": {`cs.{"src":"x"}`}},
		{"id": {"not.is.null"}},
	}
	for i, r := range reqs {
		checkSupabaseAuth(t, r)
		if r.Method != "DELETE" || r.Path != "/rest/v1/documents" || r.Header.Get("Prefer") != "return=representation" {
			t.Errorf("req %d: %s %s Prefer=%q", i, r.Method, r.Path, r.Header.Get("Prefer"))
		}
		q, _ := url.ParseQuery(r.Query)
		if q.Encode() != wantQ[i].Encode() {
			t.Errorf("req %d query %q want %q", i, q.Encode(), wantQ[i].Encode())
		}
	}
	if _, err := be.Delete(ctx, nil, "src = x", false); err == nil {
		t.Error("string filter should fail")
	}
}

func TestSupabaseHTTPError(t *testing.T) {
	s := newFBServer(t, func(r *http.Request, _ interface{}) (int, string) {
		return 404, `{"message":"Could not find the function public.match_documents"}`
	})
	_, err := supabaseFor(t, s.srv.URL).Search(context.Background(), []float64{1}, 1, nil)
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") || !strings.Contains(err.Error(), "match_documents") {
		t.Fatalf("got %v", err)
	}
}

func TestSupabaseStoreNodeEndToEnd(t *testing.T) {
	s := newFBServer(t, supabaseResponder)
	n := &StoreNode{}
	base := map[string]interface{}{"provider": "supabase", "collection": "documents", "url": s.srv.URL, "api_key": "srk"}
	cfg := copyMap(base)
	cfg["mode"] = "insert"
	in := workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(map[string]interface{}{
		"id": "a", "pageContent": "hello", "embedding": []interface{}{1.0},
	})}}
	out, err := n.Execute(context.Background(), in, cfg)
	if err != nil || len(out[0].Items) != 1 || out[0].Items[0].JSON["id"] != "a" {
		t.Fatalf("insert: %v %+v", err, out)
	}
	cfg = copyMap(base)
	cfg["mode"] = "search"
	cfg["query_vector"] = []interface{}{1.0}
	out, err = n.Execute(context.Background(), workflow.NodeInput{}, cfg)
	if err != nil || len(out[0].Items) != 1 || out[0].Items[0].JSON["score"] != 0.8 {
		t.Fatalf("search: %v %+v", err, out)
	}
}
