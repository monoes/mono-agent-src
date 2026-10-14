package apipkg

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestHeaderMergeAndTemplatedNames(t *testing.T) {
	p := mustParse(t, pkgJSON(`{"resource":"","operation":"put","title":"Put","method":"PUT","path":"/blob",
	  "fields":[{"key":"options","label":"Options","type":"collection","default":{},"options":["metadata","tags","hdr","hdrValue"]}],
	  "headers":{
	    "$merge":"{{ p.options.metadata | kv:'fieldName,fieldValue,x-ms-meta-' }}",
	    "x-ms-meta-fixed":"explicit",
	    "x-ms-tags":"{{ p.options.tags | kv:'tagName,tagValue' | urlencode }}",
	    "{{ p.options.hdr }}":"{{ p.options.hdrValue }}"
	  }}`))
	seen := pagedServer(t, func(_ int, _ *http.Request, w http.ResponseWriter) { fmt.Fprint(w, `{}`) })
	opts := map[string]any{
		"metadata": map[string]any{"metadataValues": []any{
			map[string]any{"fieldName": "owner", "fieldValue": "ada"},
			map[string]any{"fieldName": "fixed", "fieldValue": "merged"},
			map[string]any{"fieldName": "", "fieldValue": "skipped"},
		}},
		"tags": map[string]any{"tagValues": []any{
			map[string]any{"tagName": "project", "tagValue": "a b"},
			map[string]any{"tagName": "env", "tagValue": "x&y=z"},
		}},
		"hdr": "X-Custom", "hdrValue": "v",
	}
	if _, err := runPackageOp(p, map[string]any{"operation": "put", "options": opts}, cred, nil); err != nil {
		t.Fatal(err)
	}
	h := (*seen)[0].Header
	for k, want := range map[string]string{
		"X-Ms-Meta-Owner": "ada",
		"X-Ms-Meta-Fixed": "explicit", // explicit keys override $merge
		"X-Ms-Tags":       "env=x%26y%3Dz&project=a%20b",
		"X-Custom":        "v",
	} {
		if got := h.Get(k); got != want {
			t.Errorf("header %s = %q, want %q", k, got, want)
		}
	}
	if _, ok := h["X-Ms-Meta-"]; ok {
		t.Error("a metadata row with an empty name must not send a header")
	}

	// Nothing set: no metadata, no tags, and the templated name renders
	// empty so that header is skipped.
	*seen = nil
	if _, err := runPackageOp(p, map[string]any{"operation": "put"}, cred, nil); err != nil {
		t.Fatal(err)
	}
	for k := range (*seen)[0].Header {
		if strings.HasPrefix(strings.ToLower(k), "x-ms-") && k != "X-Ms-Meta-Fixed" {
			t.Errorf("unexpected header %s", k)
		}
	}

	// $merge of a non-object is an error that names it.
	bad := mustParse(t, pkgJSON(`{"resource":"","operation":"put","title":"Put","method":"PUT","path":"/b",
	  "fields":[{"key":"m","label":"M","type":"text"}],"headers":{"$merge":"{{ p.m }}"}}`))
	if _, err := runPackageOp(bad, map[string]any{"operation": "put", "m": "x"}, cred, nil); err == nil || !strings.Contains(err.Error(), "headers.$merge") {
		t.Errorf("non-object $merge: %v", err)
	}
}

func TestHeaderTemplatesValidated(t *testing.T) {
	_, err := Parse([]byte(pkgJSON(`{"resource":"","operation":"put","title":"Put","method":"PUT","path":"/b",
	  "headers":{"$merge":"{{ p.nope }}","{{ p.alsoNope }}":"v"}}`)), "test")
	if err == nil || !strings.Contains(err.Error(), `"nope"`) || !strings.Contains(err.Error(), `"alsoNope"`) {
		t.Errorf("unknown params in header $merge / name must fail validation: %v", err)
	}
}

func TestLenAndURLEncodeFilters(t *testing.T) {
	sc := Scope{P: map[string]any{
		"rows": `[[1,2],[3,4],[5,6]]`, "list": []any{1, 2}, "obj": map[string]any{"a": 1}, "s": "héllo",
	}}
	cases := []struct {
		in   string
		want any
	}{
		{"{{ p.rows | fromjson | len }}", 3},
		{"{{ p.list | len }}", 2},
		{"{{ p.obj | len }}", 1},
		{"{{ p.s | len }}", 5},
		{"{{ p.none | len }}", nil},
		{"{{ p.none | len | default:1 }}", 1.0},
		{"{{ p.list | len | add:10 }}", 12},
		{"{{ p.obj | urlencode }}", "a=1"},
		{"{{ p.s | urlencode }}", "h%C3%A9llo"},
	}
	for _, c := range cases {
		got, err := renderString(c.in, sc)
		if err != nil || fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("%s = %#v, %v; want %#v", c.in, got, err, c.want)
		}
	}
}

func TestMatchAfterTo(t *testing.T) {
	p := mustParse(t, pkgJSON(`{"resource":"","operation":"lookup","title":"Lookup","method":"GET","path":"/rows",
	  "fields":[{"key":"lookupColumn","label":"Column","type":"text","required":true},{"key":"lookupValue","label":"Value","type":"text","required":true},
	    {"key":"options","label":"Options","type":"collection","default":{},"options":["returnAllMatches"]}],
	  "output":{"path":"value","to":{"$merge":"{{ el.values.0 | zip:res.columns }}"},"match_after_to":true,
	    "match":{"rows":"{{ p | pick:'lookupColumn,lookupValue' }}","column":"lookupColumn","value":"lookupValue",
	      "first":"{{ p.options.returnAllMatches | ne:'true' }}"}}}`))
	pagedServer(t, func(_ int, _ *http.Request, w http.ResponseWriter) {
		fmt.Fprint(w, `{"columns":["name","team"],"value":[{"values":[["ada","x"]]},{"values":[["bob","y"]]},{"values":[["cy","x"]]}]}`)
	})
	items, err := runPackageOp(p, map[string]any{"operation": "lookup", "lookupColumn": "team", "lookupValue": "x"}, cred, nil)
	if err != nil || len(items) != 1 || items[0].JSON["name"] != "ada" {
		t.Fatalf("first match: %v, %v", items, err)
	}
	items, err = runPackageOp(p, map[string]any{"operation": "lookup", "lookupColumn": "team", "lookupValue": "x",
		"options": map[string]any{"returnAllMatches": true}}, cred, nil)
	if err != nil || len(items) != 2 || items[1].JSON["name"] != "cy" {
		t.Fatalf("all matches: %v, %v", items, err)
	}
	if !reflect.DeepEqual(items[0].JSON, map[string]any{"name": "ada", "team": "x"}) {
		t.Errorf("reshaped row = %v", items[0].JSON)
	}
	items, err = runPackageOp(p, map[string]any{"operation": "lookup", "lookupColumn": "team", "lookupValue": "z"}, cred, nil)
	if err != nil || len(items) != 0 {
		t.Errorf("no match: %v, %v", items, err)
	}
}

func TestOperationScopeGroupOverridesCredential(t *testing.T) {
	p := mustParse(t, `{"$schema":"apipkg.v1","id":"ms","title":"MS","base_url":"https://graph.test",
	  "credentials":[{"platform":"microsoft","reuse_platform":"microsoft","methods":["oauth"],"scope_group":"mail",
	    "inject":{"headers":{"Authorization":"Bearer {{ cred.access_token }}"}}}],
	  "resources":[{"value":"message","label":"Message"},{"value":"contact","label":"Contact"}],
	  "operations":[
	    {"resource":"message","operation":"get","title":"Get","method":"GET","path":"/messages"},
	    {"resource":"contact","operation":"get","title":"Get","method":"GET","path":"/contacts","scope_group":"contacts"}]}`)
	pagedServer(t, func(_ int, _ *http.Request, w http.ResponseWriter) { fmt.Fprint(w, `{}`) })
	old := ScopeCheck
	t.Cleanup(func() { ScopeCheck = old })
	var groups []string
	ScopeCheck = func(_ map[string]any, g string) error {
		groups = append(groups, g)
		if g == "contacts" {
			return errors.New("missing contacts; connect microsoft --scopes mail,contacts")
		}
		return nil
	}
	c := map[string]any{"access_token": "t"}
	if _, err := runPackageOp(p, map[string]any{"resource": "message", "operation": "get"}, c, nil); err != nil {
		t.Fatal(err)
	}
	_, err := runPackageOp(p, map[string]any{"resource": "contact", "operation": "get"}, c, nil)
	if err == nil || !strings.Contains(err.Error(), "contact.get") || !strings.Contains(err.Error(), "--scopes mail,contacts") {
		t.Errorf("contact op on a mail grant: %v", err)
	}
	if strings.Join(groups, ",") != "mail,contacts" {
		t.Errorf("checked groups %v, want mail then contacts", groups)
	}
	// The real check: an umbrella grant without Contacts.ReadWrite fails.
	ScopeCheck = old
	c = map[string]any{"access_token": "t", "oauth_platform": "microsoft", "scope": "Mail.ReadWrite Mail.Send User.Read offline_access"}
	if _, err := runPackageOp(p, map[string]any{"resource": "contact", "operation": "get"}, c, nil); err == nil ||
		!strings.Contains(err.Error(), "monoagentcli connect microsoft --scopes mail,contacts") {
		t.Errorf("umbrella check: %v", err)
	}
}

func TestScopeGroupNamesValidated(t *testing.T) {
	_, err := Parse([]byte(`{"$schema":"apipkg.v1","id":"ms","title":"MS","base_url":"https://graph.test",
	  "credentials":[{"platform":"microsoft","reuse_platform":"microsoft","methods":["oauth"],"scope_group":"mial",
	    "inject":{"headers":{"Authorization":"Bearer {{ cred.access_token }}"}}}],
	  "operations":[{"resource":"","operation":"get","title":"Get","method":"GET","path":"/x","scope_group":"contcts"}]}`), "test")
	if err == nil || !strings.Contains(err.Error(), `unknown scope_group "mial"`) || !strings.Contains(err.Error(), `unknown scope_group "contcts"`) {
		t.Errorf("typos in scope groups must fail validation: %v", err)
	}
}
