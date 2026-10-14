package apipkg

import (
	"reflect"
	"strings"
	"testing"
)

var tsc = Scope{
	P: map[string]any{
		"name": "Ada", "count": 3.0, "zero": 0.0, "off": false, "empty": "", "nilv": nil,
		"tags":   []any{"a", "b", 2.0},
		"extra":  map[string]any{"due": "today", "note": "", "prio": "4"},
		"rl":     map[string]any{"__rl": true, "mode": "id", "value": "123"},
		"nested": map[string]any{"list": []any{map[string]any{"id": "x"}, map[string]any{"id": "y"}}},
		"flag":   "yes", "num": "42.9", "Upper": "MiXeD",
	},
	Cred: map[string]any{"token": "T", "sub": "acme"},
	Item: map[string]any{"id": 9.0, "user": map[string]any{"email": "a@b.c"}},
}

func TestRenderStringTyped(t *testing.T) {
	cases := []struct {
		in   string
		want any
	}{
		{"plain", "plain"},
		{"{{ p.name }}", "Ada"},
		{"{{p.count}}", 3.0}, // a single expression keeps its type
		{"{{ p.tags }}", []any{"a", "b", 2.0}},
		{"{{ p.extra }}", sccExtra()},
		{"{{ p.off }}", false},
		{"{{ p.missing }}", nil},
		{"Hi {{ p.name }} x{{ p.count }}", "Hi Ada x3"}, // interpolation stringifies
		{"{{ p.name }}{{ p.name }}", "AdaAda"},
		{"[{{ p.missing }}]", "[]"},
		{"{{ cred.token }}", "T"},
		{"https://{{ cred.sub }}.example.com", "https://acme.example.com"},
		{"{{ item.id }}", 9.0},
		{"{{ item.user.email }}", "a@b.c"},
		{"{{ p.nested.list.-1.id }}", "y"},
		{"{{ p.nested.list.0.id }}", "x"},
		{"{{ p.nested.list.5.id }}", nil},
		{"{{ p.tags.1 }}", "b"},
		{"tags={{ p.tags }}", `tags=["a","b",2]`},
	}
	for _, c := range cases {
		got, err := renderString(c.in, tsc)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q = %#v, want %#v", c.in, got, c.want)
		}
	}
}

func sccExtra() map[string]any { return tsc.P["extra"].(map[string]any) }

func TestFilters(t *testing.T) {
	cases := []struct {
		in   string
		want any
	}{
		{"{{ p.tags | csv }}", "a,b,2"},
		{"{{ p.name | csv }}", "Ada"},
		{"{{ p.extra | json }}", `{"due":"today","note":"","prio":"4"}`},
		{"{{ p.extra.prio | int }}", int64(4)},
		{"{{ p.num | int }}", int64(42)},
		{"{{ p.num | float }}", 42.9},
		{"{{ p.count | string }}", "3"},
		{"{{ p.flag | bool }}", true},
		{"{{ p.zero | bool }}", false},
		{"{{ p.Upper | lower }}", "mixed"},
		{"{{ p.Upper | upper }}", "MIXED"},
		{"{{ p.missing | default:\"x\" }}", "x"},
		{"{{ p.empty | default:'y' }}", "y"},
		{"{{ p.zero | default:5 }}", 0.0}, // 0 is not empty
		{"{{ p.missing | default:p.name }}", "Ada"},
		{"{{ p.missing | default:cred.token }}", "T"},
		{"{{ p.missing | default:true }}", true},
		{"{{ p.rl | rl }}", "123"},
		{"{{ p.name | rl }}", "Ada"},
		{"{{ p.name | then:'/filter' }}", "/filter"},
		{"{{ p.empty | then:'/filter' }}", nil},
		{"{{ p.missing | int }}", nil}, // nil passes through
		{"{{ p.missing | json }}", nil},
		{"{{ p.empty | int }}", nil},
		{"{{ p.missing | default:'7' | int }}", int64(7)},
		{"{{ p.tags | csv | upper }}", "A,B,2"},
		{"a {{ p.tags | csv }} b", "a a,b,2 b"},
	}
	for _, c := range cases {
		got, err := renderString(c.in, tsc)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q = %#v (%T), want %#v (%T)", c.in, got, got, c.want, c.want)
		}
	}
}

func TestFilterErrors(t *testing.T) {
	for _, in := range []string{
		"{{ p.name | int }}", "{{ p.name | bool }}", "{{ p.extra | float }}",
	} {
		if _, err := renderString(in, tsc); err == nil || !strings.Contains(err.Error(), in[3:len(in)-3]) {
			t.Errorf("%q: want an error quoting the expression, got %v", in, err)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for in, want := range map[string]string{
		"{{ p.name":             "unclosed",
		"{{ }}":                 "empty expression",
		"{{ x.name }}":          "must start with p., cred., item., items, el., up., res., main., hdr., pre., pres., bin., page., req., batch, hook. or be uuid / now / idx",
		"{{ p..a }}":            "empty path segment",
		"{{ p.a | nope }}":      "unknown filter",
		"{{ p.a | default }}":   "needs an argument",
		"{{ p.a | then }}":      "needs an argument",
		"{{ p.a | default:x }}": "not a quoted string",
	} {
		_, err := parseTemplate(in)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want error containing %q", in, err, want)
		}
	}
}

func TestRenderObjectOmitsEmptyAndMerges(t *testing.T) {
	tmpl := map[string]any{
		"name":    "{{ p.name }}",
		"gone":    "{{ p.missing }}",
		"blank":   "{{ p.empty }}",
		"zero":    "{{ p.zero }}",
		"off":     "{{ p.off }}",
		"!forced": "{{ p.empty }}",
		"!null":   "{{ p.missing }}",
		"$merge":  "{{ p.extra }}",
		"due":     "{{ p.missing }}", // empty explicit key keeps the merged value
		"prio":    "{{ p.count }}",   // explicit key wins over the merged one
		"nested":  map[string]any{"a": "{{ p.missing }}"},
		"sub":     map[string]any{"a": "{{ p.name }}", "b": ""},
		"list":    []any{"{{ p.name }}", "{{ p.missing }}", "x"},
		"nolist":  []any{"{{ p.missing }}"},
		"lit":     7.0,
	}
	got, err := renderObject(tmpl, tsc)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"name": "Ada", "zero": 0.0, "off": false, "forced": "", "null": nil,
		"due": "today", "prio": 3.0,
		"sub": map[string]any{"a": "Ada"}, "list": []any{"Ada", "x"}, "lit": 7.0,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %#v\nwant %#v", got, want)
	}
}

func TestMergeVariants(t *testing.T) {
	got, err := renderObject(map[string]any{"$merge": []any{"{{ p.extra }}", "{{ p.missing }}", map[string]any{"k": "v"}}}, tsc)
	if err != nil {
		t.Fatal(err)
	}
	if got["due"] != "today" || got["k"] != "v" || got["note"] != nil {
		t.Errorf("merge list: %v", got)
	}
	if _, err := renderObject(map[string]any{"$merge": "{{ p.name }}"}, tsc); err == nil || !strings.Contains(err.Error(), "not an object") {
		t.Errorf("merging a string should fail: %v", err)
	}
}

func TestRenderPathEscapes(t *testing.T) {
	sc := Scope{P: map[string]any{"id": "a b/c", "f": "x"}}
	for in, want := range map[string]string{
		"/items/{{ p.id }}":                   "/items/a%20b%2Fc",
		"/tasks{{ p.f | then:'/filter' }}":    "/tasks/filter", // author literals are not escaped
		"/tasks{{ p.none | then:'/filter' }}": "/tasks",
		"/x/{{ p.none | default:'a/b' }}":     "/x/a/b",
		"/x/{{ p.id | default:'a/b' }}":       "/x/a%20b%2Fc",
		"/static":                             "/static",
	} {
		got, err := renderPath(in, sc)
		if err != nil || got != want {
			t.Errorf("%q = %q (%v), want %q", in, got, err, want)
		}
	}
}

func TestStringify(t *testing.T) {
	for v, want := range map[any]string{1e21: "1000000000000000000000", 2.5: "2.5", true: "true", int64(5): "5"} {
		if got := stringify(v); got != want {
			t.Errorf("stringify(%v) = %q", v, got)
		}
	}
	if stringify(map[string]any{"a": 1.0}) != `{"a":1}` || stringify(nil) != "" {
		t.Error("stringify object/nil")
	}
}

func TestFiltersKVEqOmit(t *testing.T) {
	sc := Scope{
		P: map[string]any{
			"mode":   "autoMapInputData",
			"rows":   []any{map[string]any{"fieldId": "a", "fieldValue": 1.0}, map[string]any{"fieldId": "", "fieldValue": 2.0}, map[string]any{"fieldId": "b", "fieldValue": "x"}},
			"wrap":   map[string]any{"fieldValues": []any{map[string]any{"fieldId": "c", "fieldValue": true}}},
			"ignore": "secret, internal",
		},
		Item: map[string]any{"name": "n", "secret": "s", "internal": 1.0},
	}
	cases := []struct {
		in   string
		want any
	}{
		{"{{ p.rows | kv:'fieldId,fieldValue' }}", map[string]any{"a": 1.0, "b": "x"}},
		{"{{ p.wrap | kv:'fieldId,fieldValue' }}", map[string]any{"c": true}},
		{"{{ p.missing | kv:'fieldId,fieldValue' }}", nil},
		{"{{ p.mode | eq:'autoMapInputData' }}", true},
		{"{{ p.mode | eq:'defineBelow' }}", nil},
		{"{{ p.mode | ne:'defineBelow' }}", true},
		{"{{ p.mode | eq:'autoMapInputData' | then:item }}", sc.Item},
		{"{{ p.mode | eq:'defineBelow' | then:item }}", nil},
		{"{{ item | omit:p.ignore }}", map[string]any{"name": "n"}},
		{"{{ item | omit:'secret' }}", map[string]any{"name": "n", "internal": 1.0}},
	}
	for _, c := range cases {
		got, err := renderString(c.in, sc)
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s = %#v, want %#v", c.in, got, c.want)
		}
	}
	body, err := renderValue(map[string]any{"$merge": []any{
		"{{ p.mode | eq:'autoMapInputData' | then:item }}",
		"{{ p.rows | kv:'fieldId,fieldValue' }}",
	}}, sc)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"name": "n", "secret": "s", "internal": 1.0, "a": 1.0, "b": "x"}
	if !reflect.DeepEqual(body, want) {
		t.Errorf("dataToSend merge = %#v, want %#v", body, want)
	}
	for _, bad := range []string{"{{ p.rows | kv:'fieldId' }}", "{{ p.mode | kv:'a,b' }}", "{{ p.mode | omit:'a' }}"} {
		if _, err := renderString(bad, sc); err == nil {
			t.Errorf("%s: want error", bad)
		}
	}
}

func TestRenderObjectForcesEmptyObjectsAndArrays(t *testing.T) {
	tmpl := map[string]any{
		"!vars":   map[string]any{"$merge": "{{ p.missing }}"},
		"!ids":    []any{"{{ p.missing }}"},
		"!lit":    map[string]any{},
		"dropped": map[string]any{"$merge": "{{ p.missing }}"},
		"outer":   map[string]any{"!inner": map[string]any{"a": "{{ p.missing }}"}, "!list": []any{}},
		"$merge":  map[string]any{"!merged": map[string]any{}, "gone": map[string]any{}},
	}
	got, err := renderObject(tmpl, tsc)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"vars": map[string]any{}, "ids": []any{}, "lit": map[string]any{},
		"outer":  map[string]any{"inner": map[string]any{}, "list": []any{}},
		"merged": map[string]any{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v\nwant %#v", got, want)
	}
}
