package apipkg

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestUniqueFilterAndBuilderKey(t *testing.T) {
	sc := Scope{P: map[string]any{
		"urls":  []any{"a", "b", "a", "c", "b"},
		"files": []any{map[string]any{"url": "u1", "n": 1}, map[string]any{"url": "u1", "n": 2}, map[string]any{"n": 3}},
		"old":   []any{"x", "y"},
		"new":   []any{"y", "z"},
	}}
	for tpl, want := range map[string]string{
		"{{ p.urls | unique }}":        `["a","b","c"]`,
		"{{ p.files | unique:'url' }}": `[{"n":1,"url":"u1"},{"n":3}]`,
		"{{ p.missing | unique }}":     `null`,
	} {
		v, err := renderString(tpl, sc)
		if err != nil {
			t.Fatalf("%s: %v", tpl, err)
		}
		if b, _ := json.Marshal(v); string(b) != want {
			t.Errorf("%s = %s, want %s", tpl, b, want)
		}
	}
	v, err := renderValue(map[string]any{"$concat": []any{"{{ p.old }}", "{{ p.new }}"}, "$unique": true}, sc)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(v); string(b) != `["x","y","z"]` {
		t.Errorf("$concat $unique = %s", b)
	}
	v, _ = renderValue(map[string]any{"$map": "{{ p.files }}", "$to": "{{ el }}", "$unique": "url"}, sc)
	if l, _ := v.([]any); len(l) != 2 {
		t.Errorf("$map $unique:url = %v", v)
	}
}

func TestSetAndNotFilters(t *testing.T) {
	sc := Scope{P: map[string]any{"rows": []any{map[string]any{"column": "age", "fieldValue": "9"}}, "col": "name", "val": "zed"}}
	for tpl, want := range map[string]string{
		"{{ p.rows | kv:'column,fieldValue' | set:p.col,p.val }}": `{"age":"9","name":"zed"}`,
		"{{ p.none | set:p.col,p.val }}":                          `{"name":"zed"}`,
		"{{ p.none | set:p.missing,p.val }}":                      `{}`,
		"{{ p.none | not }}":                                      `true`,
		"{{ p.col | not }}":                                       `null`,
		"{{ p.col | not | then:'x' | default:'y' }}":              `"y"`,
	} {
		v, err := renderString(tpl, sc)
		if err != nil {
			t.Fatalf("%s: %v", tpl, err)
		}
		if b, _ := json.Marshal(v); string(b) != want {
			t.Errorf("%s = %s, want %s", tpl, b, want)
		}
	}
	if _, err := renderString("{{ p.col | set:'a',p.val }}", sc); err == nil || !strings.Contains(err.Error(), "not an object") {
		t.Errorf("set on text: %v", err)
	}
}

// bind replaces whole-value and whole-key $N placeholders the way n8n's
// query parameters do, and refuses anything that would add structure.
func TestBindFilter(t *testing.T) {
	q := `{"query":{"bool":{"must":[{"match":{"$1":"$2"}},{"range":{"age":{"gte":"$3"}}}],"filter":"$4","note":"$4 text"}}}`
	sc := Scope{P: map[string]any{"q": q, "params": `["title","n8n",30,null]`, "list": []any{"title", "n8n", 30.0, nil}}}
	want := `{"query":{"bool":{"filter":null,"must":[{"match":{"title":"n8n"}},{"range":{"age":{"gte":30}}}],"note":"$4 text"}}}`
	for _, tpl := range []string{"{{ p.q | fromjson | bind:p.params }}", "{{ p.q | fromjson | bind:p.list }}"} {
		v, err := renderString(tpl, sc)
		if err != nil {
			t.Fatalf("%s: %v", tpl, err)
		}
		if b, _ := json.Marshal(v); string(b) != want {
			t.Errorf("%s = %s", tpl, b)
		}
	}
	if v, err := renderString("{{ p.q | fromjson | bind:p.none }}", sc); err != nil || v == nil {
		t.Errorf("no parameters leave the query: %v %v", v, err)
	}
	for params, msg := range map[string]string{
		`["title","n8n"]`:              "has no value",
		`["title","n8n",30,null,"x"]`:  "parameter 5 is not used",
		`["$x","n8n",30,null]`:         "placeholder $1 is not a valid field name",
		`[{"a":1},"n8n",30,null]`:      "parameter 1 must be a scalar",
		`{"a":1}`:                      "must be a list",
		`["title","n8n",30,null] junk`: "must be a JSON array",
	} {
		sc.P["params"] = params
		_, err := renderString("{{ p.q | fromjson | bind:p.params }}", sc)
		if err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("%s: %v, want %q", params, err, msg)
		}
	}
	sc.P["dup"] = `{"title":1,"$1":2}`
	if _, err := renderString(`{{ p.dup | fromjson | bind:'["title"]' }}`, sc); err == nil || !strings.Contains(err.Error(), "used more than once") {
		t.Errorf("duplicate key: %v", err)
	}
}

func TestBase64ChunksFilter(t *testing.T) {
	sc := Scope{P: map[string]any{"b": base64.StdEncoding.EncodeToString([]byte("abcdefg"))}}
	v, err := renderString("{{ p.b | base64_chunks:3 }}", sc)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range v.([]any) {
		b, _ := base64.StdEncoding.DecodeString(c.(string))
		got = append(got, string(b))
	}
	if strings.Join(got, "|") != "abc|def|g" {
		t.Errorf("chunks %v", got)
	}
	if _, err := renderString("{{ p.b | base64_chunks:0 }}", sc); err == nil {
		t.Error("a zero chunk size is an error")
	}
	sc.P["files"] = []any{map[string]any{"base64": sc.P["b"], "file_name": "a"}, base64.StdEncoding.EncodeToString([]byte("xy"))}
	v, err = renderString("{{ p.files | base64_chunks:4 }}", sc)
	if err != nil {
		t.Fatal(err)
	}
	var segs []string
	for _, s := range v.([]any) {
		m := s.(map[string]any)
		b, _ := base64.StdEncoding.DecodeString(m["data"].(string))
		segs = append(segs, fmt.Sprintf("%v.%v=%s", m["item"], m["segment"], b))
	}
	if strings.Join(segs, " ") != "0.0=abcd 0.1=efg 1.0=xy" {
		t.Errorf("list segments %v", segs)
	}
	if m := v.([]any)[1].(map[string]any); m["file"].(map[string]any)["file_name"] != "a" {
		t.Errorf("segment file %v", m["file"])
	}
}

func TestNDJSONLines(t *testing.T) {
	b, err := ndjsonLines([]any{[]any{map[string]any{"index": map[string]any{"_id": "1"}}, map[string]any{"a": 1}}, map[string]any{"delete": map[string]any{}}})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "{\"index\":{\"_id\":\"1\"}}\n{\"a\":1}\n{\"delete\":{}}\n" {
		t.Errorf("ndjson %q", b)
	}
	if _, err := ndjsonLines(nil); err == nil {
		t.Error("an empty ndjson body is an error")
	}
}

func TestValidateParityKeys(t *testing.T) {
	for js, msg := range map[string]string{
		pkgJSON(`{"resource":"","operation":"l","title":"L","method":"GET","path":"/x",
		  "pagination":{"type":"page","page_param":"p","next":{"method":"GET","path":"/y"}}}`): `pagination.next works with type "cursor" only`,
		pkgJSON(`{"resource":"","operation":"l","title":"L","method":"GET","path":"/x",
		  "pagination":{"type":"cursor","cursor_path":"c","next":{"method":"GET","path":""}}}`): "pagination.next.path: must not be empty",
		pkgJSON(`{"resource":"","operation":"c","title":"C","method":"POST","path":"/x",
		  "bulk":{"when":"","method":"POST","path":"/_bulk","body_type":"ndjson","body":"{{ p.nope }}"}}`): "bulk: needs a when template",
		pkgJSON(`{"resource":"","operation":"c","title":"C","method":"POST","path":"/x",
		  "bulk":{"when":"{{ p.nope }}","method":"POST","path":"/_bulk"}}`): `"nope" is not one of the op's fields`,
		strings.Replace(pkgJSON(`{"resource":"","operation":"c","title":"C","method":"GET","path":"/x"}`),
			`"title":"Demo",`, `"title":"Demo","n8n_version_defaults":{"v3":{"a":1}},`, 1): `key "v3" must be a typeVersion`,
	} {
		_, err := Parse([]byte(js), "test")
		if err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("want %q, got %v", msg, err)
		}
	}
	// A cursor with a continuation request needs no cursor_param.
	mustParse(t, pkgJSON(`{"resource":"","operation":"l","title":"L","method":"POST","path":"/x",
	  "pagination":{"type":"cursor","cursor_path":"c","next":{"method":"POST","path":"/x/continue","body":{"cursor":"{{ page.cursor }}"},"error_when":"{{ res.bad }}"}}}`))
}
