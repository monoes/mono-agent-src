package apipkg

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// confluenceTree answers the Confluence page.get walk: page 1 has two
// pages of descendants; page 10 sits at depth 10, so it is re-rooted.
func confluenceTree(w http.ResponseWriter, r *http.Request) {
	p, q := r.URL.Path, r.URL.Query()
	switch {
	case p == "/ex/confluence/c1/wiki/api/v2/pages/1":
		fmt.Fprint(w, `{"id":"1","title":"root"}`)
	case p == "/ex/confluence/c1/wiki/api/v2/pages/1/descendants" && q.Get("cursor") == "":
		fmt.Fprint(w, `{"results":[
		  {"id":"2","type":"page","depth":1,"status":"current"},
		  {"id":"3","type":"whiteboard","depth":1},
		  {"id":"4","type":"page","depth":2,"status":"draft"}],
		  "_links":{"next":"/wiki/api/v2/pages/1/descendants?cursor=n1&limit=250&depth=10"}}`)
	case p == "/ex/confluence/c1/wiki/api/v2/pages/1/descendants":
		fmt.Fprint(w, `{"results":[
		  {"id":"10","type":"page","depth":10,"status":"archived"},
		  {"id":"11","type":"folder","depth":10},
		  {"id":"2","type":"page","depth":1}],"_links":{}}`)
	case p == "/ex/confluence/c1/wiki/api/v2/pages/10/descendants":
		fmt.Fprint(w, `{"results":[{"id":"12","type":"page","depth":1}]}`)
	case p == "/ex/confluence/c1/wiki/api/v2/pages/11/descendants":
		fmt.Fprint(w, `{"results":[{"id":"13","type":"page","depth":1,"status":"current"}]}`)
	case p == "/ex/confluence/c1/wiki/api/v2/pages":
		var out []string
		for _, id := range strings.Split(q.Get("id"), ",") {
			if id != "12" { // a page the caller cannot read is dropped
				out = append(out, fmt.Sprintf(`{"id":%q,"body":%q}`, id, q.Get("body-format")))
			}
		}
		fmt.Fprintf(w, `{"results":[%s]}`, strings.Join(out, ","))
	default:
		http.Error(w, "unexpected "+r.URL.String(), http.StatusNotFound)
	}
}

func TestConfluenceIncludeDescendantsWalk(t *testing.T) {
	p := catalogPkg(t, "confluence")
	seen := pagedServer(t, func(_ int, r *http.Request, w http.ResponseWriter) { confluenceTree(w, r) })
	params := map[string]any{"resource": "page", "operation": "get", "site": "c1", "page": "1",
		"bodyFormat": "plainText", "includeDescendants": true}
	items, err := runPackageOp(p, params, graphCred, nil)
	if err != nil {
		t.Fatal(err)
	}
	var ids []any
	for _, it := range items {
		ids = append(ids, it.JSON["id"])
	}
	// Root first, then pages in walk order; the whiteboard, folder and
	// draft are not pages to return, 12 was not readable.
	if want := []any{"1", "2", "10", "13"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	if items[1].JSON["body"] != "atlas_doc_format" {
		t.Errorf("hydrated page body format = %v", items[1].JSON["body"])
	}
	var got []string
	for _, r := range *seen {
		got = append(got, r.URL.Path+"?"+r.URL.RawQuery)
	}
	want := []string{
		"/ex/confluence/c1/wiki/api/v2/pages/1?body-format=atlas_doc_format",
		"/ex/confluence/c1/wiki/api/v2/pages/1/descendants?depth=10&limit=250",
		"/ex/confluence/c1/wiki/api/v2/pages/1/descendants?cursor=n1&limit=250&depth=10",
		"/ex/confluence/c1/wiki/api/v2/pages/10/descendants?depth=10&limit=250",
		"/ex/confluence/c1/wiki/api/v2/pages/11/descendants?depth=10&limit=250",
		"/ex/confluence/c1/wiki/api/v2/pages?body-format=atlas_doc_format&id=2%2C10%2C12%2C13&limit=250",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("requests:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, r := range *seen {
		if r.Header.Get("Authorization") != "Bearer t" {
			t.Errorf("%s: no auth", r.URL.Path)
		}
	}
}

func TestConfluenceWalkMaxPagesAndOff(t *testing.T) {
	p := catalogPkg(t, "confluence")
	seen := pagedServer(t, func(_ int, r *http.Request, w http.ResponseWriter) { confluenceTree(w, r) })
	params := map[string]any{"resource": "page", "operation": "get", "site": "c1", "page": "1",
		"includeDescendants": true, "maxPages": 2}
	items, err := runPackageOp(p, params, graphCred, nil)
	if err != nil || len(items) != 2 || items[1].JSON["id"] != "2" || items[1].JSON["body"] != "storage" {
		t.Fatalf("maxPages 2: %v, %v", items, err)
	}
	// The cap is reached on the first page of descendants: no cursor
	// request, one hydration request.
	if len(*seen) != 3 {
		t.Fatalf("maxPages 2 sent %d requests", len(*seen))
	}
	*seen = nil
	params["maxPages"] = 1
	if items, err = runPackageOp(p, params, graphCred, nil); err != nil || len(items) != 1 || len(*seen) != 1 {
		t.Fatalf("maxPages 1: %d items, %d requests, %v", len(items), len(*seen), err)
	}
	*seen = nil
	delete(params, "includeDescendants")
	if items, err = runPackageOp(p, params, graphCred, nil); err != nil || len(items) != 1 || len(*seen) != 1 {
		t.Fatalf("walk off: %d items, %d requests, %v", len(items), len(*seen), err)
	}
}

func TestWalkWithoutPaginationOrFetch(t *testing.T) {
	p := mustParse(t, pkgJSON(`{"resource":"","operation":"tree","title":"T","method":"GET","path":"/nodes/{{ p.id }}",
	  "fields":[{"key":"id","label":"ID","type":"text","required":true}],
	  "output":{"walk":{"start":"{{ res.children }}","request":{"method":"GET","path":"/nodes/{{ el }}/children"},
	    "results_path":"items","id":"{{ el.key }}","descend":"{{ el.leaf | ne:'true' }}"}}}`))
	pagedServer(t, func(_ int, r *http.Request, w http.ResponseWriter) {
		switch r.URL.Path {
		case "/v1/nodes/r":
			fmt.Fprint(w, `{"key":"r","children":["a","b"]}`)
		case "/v1/nodes/a/children":
			fmt.Fprint(w, `{"items":[{"key":"c","leaf":false},{"key":"b","leaf":true}]}`)
		case "/v1/nodes/b/children":
			fmt.Fprint(w, `{"items":[{"key":"d","leaf":true}]}`)
		case "/v1/nodes/c/children":
			fmt.Fprint(w, `{"items":[{"key":"a","leaf":false},{"key":"e","leaf":true}]}`)
		default:
			http.Error(w, r.URL.Path, http.StatusNotFound)
		}
	})
	items, err := runPackageOp(p, map[string]any{"operation": "tree", "id": "r"}, cred, nil)
	if err != nil {
		t.Fatal(err)
	}
	var keys []any
	for _, it := range items {
		keys = append(keys, it.JSON["key"])
	}
	// Start ids are seen already, so b and a are not repeated.
	if want := []any{"r", "c", "d", "e"}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
}

func TestWalkValidation(t *testing.T) {
	_, err := Parse([]byte(pkgJSON(`{"resource":"","operation":"tree","title":"T","method":"GET","path":"/n",
	  "output":{"walk":{"start":"","request":{"method":"GET","path":"/x/{{ p.nope }}"},
	    "fetch":{"method":"GET","path":"/y","chunk":-1}}}}`)), "demo")
	if err == nil {
		t.Fatal("bad walk validated")
	}
	for _, want := range []string{"start is required", "fetch.chunk must not be negative", `"nope"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	_, err = Parse([]byte(pkgJSON(`{"resource":"","operation":"tree","title":"T","method":"GET","path":"/n",
	  "output":{"walk":{"start":"{{ res.id }}","request":{"method":"GET","path":"/x","when":"x"}}}}`)), "demo")
	if err == nil || !strings.Contains(err.Error(), `unknown key "when"`) {
		t.Errorf("schema errors: %v", err)
	}
}
