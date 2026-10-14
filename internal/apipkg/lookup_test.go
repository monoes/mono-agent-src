package apipkg

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestFindFindRowArithmeticColumnsGet(t *testing.T) {
	rows := []any{[]any{"id", "name"}, []any{"1", "ada"}, []any{"2", "bob"}}
	sc := Scope{
		P: map[string]any{"col": "name", "want": "bob", "start": 3.0, "n": "2", "letter": "C",
			"sheets": []any{map[string]any{"properties": map[string]any{"title": "S1", "sheetId": 7.0}}}},
		Item: map[string]any{"name": "ada"},
		Pre:  map[string]any{"values": rows, "headers": []any{map[string]any{"name": "Subject", "value": "hi"}}},
	}
	cases := []struct {
		in   string
		want any
	}{
		{"{{ pre.values | find_row:p.col,p.want }}", int64(3)},
		{"{{ pre.values | find_row:p.col,item }}", int64(2)},
		{"{{ pre.values | find_row:'name','zed' }}", nil},
		{"{{ pre.values | find_row:'nope','bob' }}", nil},
		{"{{ p.sheets | find:'properties.title','S1' | get:'properties.sheetId' }}", 7.0},
		{"{{ pre.headers | find:'name','Subject' | get:'value' }}", "hi"},
		{"{{ p.sheets | find:'properties.title','zz' }}", nil},
		{"{{ p.start | add:p.n }}", int64(5)},
		{"{{ p.start | sub:1 }}", int64(2)},
		{"{{ p.start | add:0.5 }}", 3.5},
		{"{{ p.letter | colnum }}", int64(3)},
		{"{{ p.start | colname }}", "C"},
	}
	for _, c := range cases {
		got, err := renderString(c.in, sc)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s = %#v, %v; want %#v", c.in, got, err, c.want)
		}
	}
	if v, _ := columnNumber("AA"); v != int64(27) {
		t.Errorf("AA = %v", v)
	}
	if v, _ := columnName(28); v != "AB" {
		t.Errorf("28 = %v", v)
	}
	for _, bad := range []string{"{{ p.col | find:'a' }}", "{{ p.col | find_row:'a','b','c' }}"} {
		if _, err := parseTemplate(bad); err == nil {
			t.Errorf("%s: want parse error", bad)
		}
	}
}

func TestEntriesObject(t *testing.T) {
	sc := Scope{P: map[string]any{"date": []any{
		map[string]any{"operation": "after", "value": "2024-01-01"},
		map[string]any{"operation": "before", "value": ""},
	}}}
	got, err := renderValue(map[string]any{"$entries": "{{ p.date }}", "$key": "date[{{ el.operation }}]", "$value": "{{ el.value | epoch }}"}, sc)
	if err != nil || !reflect.DeepEqual(got, map[string]any{"date[after]": int64(1704067200)}) {
		t.Errorf("entries = %#v, %v", got, err)
	}
	if _, err := renderValue(map[string]any{"$entries": "{{ p.date }}"}, sc); err == nil {
		t.Error("$entries without $key/$value must error")
	}
}

func TestOutputEachMatchExpand(t *testing.T) {
	p := mustParse(t, pkgJSON(`{"resource":"","operation":"list","title":"L","method":"GET","path":"/invoices",
	  "fields":[{"key":"filters","label":"F","type":"json"},{"key":"combine","label":"C","type":"text"},{"key":"first","label":"1","type":"boolean"}],
	  "output":{"path":"list","each":"invoice","match":{"rows":"{{ p.filters }}","column":"lookupColumn","value":"lookupValue","combine":"{{ p.combine }}","first":"{{ p.first }}"}}},
	  {"resource":"","operation":"msgs","title":"M","method":"GET","path":"/messages",
	  "output":{"path":"messages","expand":{"method":"GET","path":"/messages/{{ el.id }}","query":{"format":"metadata"}}}}`))
	seen := pagedServer(t, func(n int, r *http.Request, w http.ResponseWriter) {
		switch {
		case r.URL.Path == "/v1/invoices":
			w.Write([]byte(`{"list":[{"invoice":{"id":"i1","status":"paid"}},{"invoice":{"id":"i2","status":"due"}},{"invoice":{"id":"i3","status":"paid"}}]}`))
		case r.URL.Path == "/v1/messages":
			w.Write([]byte(`{"messages":[{"id":"m1"},{"id":"m2"}]}`))
		default:
			id := strings.TrimPrefix(r.URL.Path, "/v1/messages/")
			w.Write([]byte(`{"id":"` + id + `","snippet":"s-` + id + `","format":"` + r.URL.Query().Get("format") + `"}`))
		}
	})
	paid := []any{map[string]any{"lookupColumn": "status", "lookupValue": "paid"}}
	items, err := runPackageOp(p, map[string]any{"operation": "list", "filters": paid}, cred, nil)
	if err != nil || len(items) != 2 || items[1].JSON["id"] != "i3" {
		t.Fatalf("each+match: %v %v", items, err)
	}
	items, _ = runPackageOp(p, map[string]any{"operation": "list", "filters": paid, "first": true}, cred, nil)
	if len(items) != 1 {
		t.Errorf("first match: %d items", len(items))
	}
	or := []any{map[string]any{"lookupColumn": "id", "lookupValue": "i1"}, map[string]any{"lookupColumn": "id", "lookupValue": "i2"}}
	items, _ = runPackageOp(p, map[string]any{"operation": "list", "filters": or, "combine": "OR"}, cred, nil)
	if len(items) != 2 {
		t.Errorf("OR: %d items", len(items))
	}
	items, _ = runPackageOp(p, map[string]any{"operation": "list"}, cred, nil)
	if len(items) != 3 {
		t.Errorf("no filters keeps all: %d", len(items))
	}
	*seen = nil
	items, err = runPackageOp(p, map[string]any{"operation": "msgs"}, cred, nil)
	if err != nil || len(items) != 2 || items[1].JSON["snippet"] != "s-m2" || items[0].JSON["format"] != "metadata" || len(*seen) != 3 {
		t.Errorf("expand: %v %v (%d requests)", items, err, len(*seen))
	}
}

// Sheets append with a gid locator: resolve the title, then read its
// header, then append the item ordered by the header.
func TestBeforeStepsList(t *testing.T) {
	p := mustParse(t, pkgJSON(`{"resource":"","operation":"append","title":"A","method":"POST",
	  "path":"/s/values/{{ pres.0.sheets | find:'properties.sheetId',p.gid | get:'properties.title' }}:append",
	  "fields":[{"key":"gid","label":"G","type":"number","required":true}],
	  "before":[{"method":"GET","path":"/s","query":{"fields":"sheets.properties"}},
	            {"method":"GET","path":"/s/values/{{ pre.sheets | find:'properties.sheetId',p.gid | get:'properties.title' }}!1:1"}],
	  "body":{"values":["{{ item | row:pre.values }}"]}}`))
	var paths []string
	pagedServer(t, func(n int, r *http.Request, w http.ResponseWriter) {
		paths = append(paths, r.URL.Path)
		switch n {
		case 1:
			w.Write([]byte(`{"sheets":[{"properties":{"sheetId":0,"title":"Main"}},{"properties":{"sheetId":42,"title":"Leads"}}]}`))
		case 2:
			w.Write([]byte(`{"values":[["email","name"]]}`))
		default:
			w.Write([]byte(`{}`))
		}
	})
	if _, err := runPackageOp(p, map[string]any{"operation": "append", "gid": 42.0}, cred, map[string]any{"name": "ada", "email": "a@b"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"/v1/s", "/v1/s/values/Leads!1:1", "/v1/s/values/Leads:append"}
	if !reflect.DeepEqual(paths, want) {
		t.Errorf("paths = %v, want %v", paths, want)
	}
}

func TestSheetGIDParenArgsAndRowNull(t *testing.T) {
	sheets := []any{map[string]any{"properties": map[string]any{"sheetId": 0.0, "title": "Main"}},
		map[string]any{"properties": map[string]any{"sheetId": 123.0, "title": "Leads"}}}
	sc := Scope{P: map[string]any{"sheets": sheets,
		"list": map[string]any{"__rl": true, "mode": "list", "value": "gid=0"},
		"url":  map[string]any{"__rl": true, "mode": "url", "value": "https://docs.google.com/spreadsheets/d/abc/edit#gid=123"},
		"id":   map[string]any{"__rl": true, "mode": "id", "value": "123"},
		"num":  123.0},
		Item: map[string]any{"a": 1.0}, Pre: map[string]any{"values": []any{[]any{"a", "b"}}}}
	cases := map[string]any{
		"{{ p.list | sheetgid }}": "0",
		"{{ p.url | sheetgid }}":  "123",
		"{{ p.id | sheetgid }}":   "123",
		"{{ p.num | sheetgid }}":  "123",
		"{{ p.sheets | find:'properties.sheetId',(p.url | sheetgid) | get:'properties.title' }}":  "Leads",
		"{{ p.sheets | find:'properties.sheetId',(p.list | sheetgid) | get:'properties.title' }}": "Main",
		"{{ p.sheets | get:(p.num | sheetgid) }}":                                                 nil,
	}
	for in, want := range cases {
		if got, err := renderString(in, sc); err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %#v, %v; want %#v", in, got, err, want)
		}
	}
	for in, want := range map[string]any{
		"{{ item | row:pre.values }}":        []any{1.0, ""},
		"{{ item | row:pre.values,'null' }}": []any{1.0, nil},
		"{{ item | row:pre.values,null }}":   []any{1.0, nil},
	} {
		if got, err := renderString(in, sc); err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %#v, %v; want %#v", in, got, err, want)
		}
	}
}

// Contentful: the token and output shape depend on node params.
func TestInjectReadsParamsAndTemplatedOutput(t *testing.T) {
	p := mustParse(t, `{"$schema":"apipkg.v1","id":"cf","title":"CF","base_url":"https://cdn.test",
	  "credentials":[{"platform":"cf","methods":["bearer"],"fields":[{"key":"access_token","label":"T","required":true},{"key":"preview_token","label":"P"}],
	    "inject":{"headers":{"Authorization":"Bearer {{ p.source | eq:'previewApi' | then:cred.preview_token | default:cred.access_token }}"}}}],
	  "operations":[{"resource":"","operation":"get","title":"Get","method":"GET","path":"/entries",
	    "fields":[{"key":"source","label":"S","type":"text"},{"key":"rawData","label":"R","type":"boolean"}],
	    "output":{"path":"items","each":"{{ p.rawData | ne:'true' | then:'fields' }}"}}]}`)
	var auths []string
	pagedServer(t, func(n int, r *http.Request, w http.ResponseWriter) {
		auths = append(auths, r.Header.Get("Authorization"))
		w.Write([]byte(`{"items":[{"sys":{"id":"e1"},"fields":{"title":"Hi"}}]}`))
	})
	c := map[string]any{"access_token": "D", "preview_token": "P"}
	items, err := runPackageOp(p, map[string]any{"operation": "get", "source": "previewApi"}, c, nil)
	if err != nil || len(items) != 1 || items[0].JSON["title"] != "Hi" {
		t.Fatalf("simple: %v %v", items, err)
	}
	items, _ = runPackageOp(p, map[string]any{"operation": "get", "rawData": true}, c, nil)
	if len(items) != 1 || items[0].JSON["fields"] == nil {
		t.Errorf("rawData: %v", items)
	}
	if !reflect.DeepEqual(auths, []string{"Bearer P", "Bearer D"}) {
		t.Errorf("auth headers = %v", auths)
	}
}

func TestEntriesFilterAndOutputTo(t *testing.T) {
	sc := Scope{P: map[string]any{"keyName": "row"}, Item: map[string]any{"row": map[string]any{"B": 2.0, "A": "x"}}}
	got, err := renderString("{{ item | get:p.keyName | entries:'column,value' }}", sc)
	want := []any{map[string]any{"column": "A", "value": "x"}, map[string]any{"column": "B", "value": 2.0}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("entries = %#v, %v", got, err)
	}
	if got, _ := renderString("{{ item.row | entries }}", sc); len(got.([]any)) != 2 || got.([]any)[0].(map[string]any)["key"] != "A" {
		t.Errorf("default entries = %#v", got)
	}
	p := mustParse(t, pkgJSON(`{"resource":"","operation":"rows","title":"R","method":"GET","path":"/rows",
	  "fields":[{"key":"rawData","label":"R","type":"boolean"}],
	  "output":{"path":"items","to":{"id":"{{ el.id }}","$merge":"{{ el.values }}"},"to_when":"{{ p.rawData | ne:'true' }}"}}`))
	pagedServer(t, func(n int, r *http.Request, w http.ResponseWriter) {
		w.Write([]byte(`{"items":[{"id":"r1","values":{"Name":"ada"}}]}`))
	})
	items, err := runPackageOp(p, map[string]any{"operation": "rows"}, cred, nil)
	if err != nil || items[0].JSON["Name"] != "ada" || items[0].JSON["id"] != "r1" {
		t.Errorf("to: %v %v", items, err)
	}
	items, _ = runPackageOp(p, map[string]any{"operation": "rows", "rawData": true}, cred, nil)
	if items[0].JSON["values"] == nil {
		t.Errorf("to_when false keeps raw: %v", items)
	}
}

// The harness itself: responses answer in order and expect_requests /
// the default main-request check see past before steps.
func TestHarnessMultiRequestCases(t *testing.T) {
	p := mustParse(t, pkgJSON(`{"resource":"","operation":"append","title":"A","method":"POST","path":"/s/v/Main:append",
	  "before":[{"method":"GET","path":"/s/v/Main!1:1"}],
	  "body":{"values":["{{ item | row:pre.values }}"]}}`))
	op := &p.Operations[0]
	var c opCase
	if err := json.Unmarshal([]byte(`{"input":{"name":"ada"},
	  "responses":[{"values":[["name","email"]]},{"updates":{}}],
	  "expect":{"method":"POST","path":"/v1/s/v/Main:append","body":{"values":[["ada",""]]}}}`), &c); err != nil {
		t.Fatal(err)
	}
	checkCase(t, p, op, c)
	c.ExpectRequests = []expectReq{{Method: "GET", Path: "/v1/s/v/Main!1:1"}, c.Expect}
	checkCase(t, p, op, c)
}

func TestOutputToOnSingleObject(t *testing.T) {
	p := mustParse(t, pkgJSON(`{"resource":"","operation":"get","title":"G","method":"GET","path":"/row",
	  "output":{"to":{"id":"{{ el.id }}","$merge":"{{ el.values }}"}}}`))
	pagedServer(t, func(n int, r *http.Request, w http.ResponseWriter) {
		w.Write([]byte(`{"id":"r1","values":{"Name":"ada"}}`))
	})
	items, err := runPackageOp(p, map[string]any{"operation": "get"}, cred, nil)
	if err != nil || len(items) != 1 || items[0].JSON["Name"] != "ada" || items[0].JSON["values"] != nil {
		t.Errorf("single object to: %v %v", items, err)
	}
}

func TestHarnessSkippedBeforeStep(t *testing.T) {
	p := mustParse(t, pkgJSON(`{"resource":"","operation":"send","title":"S","method":"POST","path":"/send",
	  "fields":[{"key":"senderName","label":"N","type":"text"}],
	  "before":[{"when":"{{ p.senderName }}","method":"GET","path":"/profile"}],"body":{"x":1}}`))
	var c opCase
	_ = json.Unmarshal([]byte(`{"expect":{"method":"POST","path":"/v1/send","body":{"x":1}}}`), &c)
	checkCase(t, p, &p.Operations[0], c) // before skipped: one request
	c.Params = map[string]any{"senderName": "Ada"}
	checkCase(t, p, &p.Operations[0], c) // before ran: main is request #1
}

func TestPathFilterBinRootAndAnyWildcard(t *testing.T) {
	p := mustParse(t, pkgJSON(`{"resource":"","operation":"upload","title":"U","method":"POST","path":"/v1/{{ p.name | path }}/files",
	  "fields":[{"key":"name","label":"N","type":"text"},{"key":"binaryPropertyName","label":"B","type":"text","default":"data"}],
	  "body_type":"raw","binary":{"upload_field":"binaryPropertyName"},
	  "also":[{"method":"PATCH","path":"/files/{{ res.id }}","query":{"requestId":"{{ uuid }}"},"body":{"name":"{{ bin.file_name }}","mimeType":"{{ bin.mime_type }}"}}]}`))
	var c opCase
	_ = json.Unmarshal([]byte(`{"params":{"name":"spaces/AA A/messages/B"},
	  "input":{"data":"aGk=","file_name":"report.pdf"},
	  "responses":[{"id":"f1"},{}],
	  "expect_requests":[
	    {"method":"POST","path":"/v1/v1/spaces/AA%20A/messages/B/files","headers":{"Content-Type":"application/pdf"},"body":"hi"},
	    {"method":"PATCH","path":"/v1/files/f1","query":{"requestId":"<any>"},"body":{"name":"report.pdf","mimeType":"application/pdf"}}]}`), &c)
	checkCase(t, p, &p.Operations[0], c)
}

func TestConfluenceAndClickUpFilters(t *testing.T) {
	sc := Scope{P: map[string]any{"text": "Hello <b>\r\n\n  a & b  \n", "mins": "2", "est": 3.0, "num": "42", "frac": "1.5", "word": "abc",
		"adf": `{"content":[{"type":"paragraph"}]}`}}
	cases := map[string]any{
		"{{ p.text | lines | escape_html | prefix:'<p>' | suffix:'</p>' | join:'' }}": "<p>Hello &lt;b&gt;</p><p>a &amp; b</p>",
		"{{ p.mins | mul:60000 }}": int64(120000),
		"{{ p.est | mul:6000 }}":   int64(18000),
		"{{ p.num | numeric }}":    int64(42),
		"{{ p.frac | numeric }}":   1.5,
		"{{ p.word | numeric }}":   "abc",
		"{{ p.none | lines }}":     nil,
	}
	for in, want := range cases {
		if got, err := renderString(in, sc); err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %#v, %v; want %#v", in, got, err, want)
		}
	}
	got, err := renderValue(map[string]any{"value": map[string]any{"$json": map[string]any{
		"type": "doc", "content": map[string]any{"$concat": []any{"{{ p.adf | fromjson | get:'content' }}"}}}}}, sc)
	if err != nil || got.(map[string]any)["value"] != `{"content":[{"type":"paragraph"}],"type":"doc"}` {
		t.Errorf("$json = %#v, %v", got, err)
	}
}

func TestNextURLPrefix(t *testing.T) {
	p := mustParse(t, pkgJSON(`{"resource":"","operation":"labels","title":"L","method":"GET","path":"/pages/1/labels",
	  "fields":[{"key":"returnAll","label":"A","type":"boolean","default":true}],
	  "pagination":{"type":"next_url","next_url_path":"_links.next","results_path":"results",
	    "next_url_prefix":"https://gw.example.test/ex/confluence/cloud1"}}`))
	seen := pagedServer(t, func(n int, r *http.Request, w http.ResponseWriter) {
		if n == 1 {
			w.Write([]byte(`{"results":[{"id":1}],"_links":{"next":"/wiki/api/v2/pages/1/labels?cursor=c2"}}`))
			return
		}
		w.Write([]byte(`{"results":[{"id":2}],"_links":{}}`))
	})
	items, err := runPackageOp(p, map[string]any{"operation": "labels", "returnAll": true}, cred, nil)
	if err != nil || len(items) != 2 {
		t.Fatalf("items %v %v", items, err)
	}
	if r := (*seen)[1]; r.URL.Path != "/ex/confluence/cloud1/wiki/api/v2/pages/1/labels" || r.URL.Query().Get("cursor") != "c2" {
		t.Errorf("second page = %s", r.URL)
	}
}

func TestForcedEmptyListBuilder(t *testing.T) {
	sc := Scope{P: map[string]any{"add": "1, 2", "rem": ""}}
	got, err := renderValue(map[string]any{
		"!add": map[string]any{"$map": "{{ p.add | split }}", "$to": "{{ el | int }}"},
		"!rem": map[string]any{"$concat": []any{map[string]any{"$map": "{{ p.rem | split }}", "$to": "{{ el | int }}"}}},
		"!nul": "{{ p.none }}",
	}, sc)
	want := map[string]any{"add": []any{int64(1), int64(2)}, "rem": []any{}, "nul": nil}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, %v", got, err)
	}
}

func TestFirestorePickAndBinaryToggles(t *testing.T) {
	sc := Scope{P: map[string]any{"cols": "a, n"}, Item: map[string]any{"a": "x", "n": 2.0, "f": 1.5, "b": true, "t": "2026-01-02T03:04:05Z", "l": []any{"y"}, "m": map[string]any{"k": nil}}}
	got, _ := renderString("{{ item | firestore_fields:p.cols }}", sc)
	want := map[string]any{"a": map[string]any{"stringValue": "x"}, "n": map[string]any{"integerValue": "2"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("firestore_fields = %#v", got)
	}
	all, _ := renderString("{{ item | firestore_fields }}", sc)
	doc := map[string]any{"name": "projects/p/databases/(default)/documents/c/d1", "createTime": "c", "updateTime": "u", "fields": all}
	dec := firestoreDoc(doc).(map[string]any)
	if dec["_id"] != "d1" || dec["n"] != 2.0 || dec["f"] != 1.5 || dec["b"] != true || dec["t"] != "2026-01-02T03:04:05Z" ||
		!reflect.DeepEqual(dec["l"], []any{"y"}) || !reflect.DeepEqual(dec["m"], map[string]any{"k": nil}) || dec["_createTime"] != "c" {
		t.Errorf("round trip = %#v", dec)
	}
	if p, _ := renderString("{{ item | pick:p.cols }}", sc); !reflect.DeepEqual(p, map[string]any{"a": "x", "n": 2.0}) {
		t.Errorf("pick = %#v", p)
	}

	pk := mustParse(t, pkgJSON(`{"resource":"","operation":"get","title":"G","method":"GET","path":"/o",
	  "fields":[{"key":"alt","label":"A","type":"text"}],"binary":{"download_when":"{{ p.alt | eq:'media' }}"}},
	  {"resource":"","operation":"create","title":"C","method":"POST","path":"/up","body_type":"raw",
	  "fields":[{"key":"text","label":"T","type":"text"},{"key":"binaryPropertyName","label":"B","type":"text","default":"data"}],
	  "binary":{"upload_field":"binaryPropertyName","upload_text":"{{ p.text }}"}}`))
	var cts, bodies []string
	pagedServer(t, func(n int, r *http.Request, w http.ResponseWriter) {
		b, _ := io.ReadAll(r.Body)
		cts, bodies = append(cts, r.Header.Get("Content-Type")), append(bodies, string(b))
		w.Write([]byte(`{"kind":"meta"}`))
	})
	items, _ := runPackageOp(pk, map[string]any{"operation": "get"}, cred, nil)
	if items[0].JSON["kind"] != "meta" {
		t.Errorf("metadata get: %v", items)
	}
	items, _ = runPackageOp(pk, map[string]any{"operation": "get", "alt": "media"}, cred, nil)
	if items[0].JSON["kind"] != nil {
		t.Errorf("alt=media must download: %v", items[0].JSON)
	}
	if _, err := runPackageOp(pk, map[string]any{"operation": "create", "text": "hello"}, cred, nil); err != nil {
		t.Fatal(err)
	}
	if bodies[len(bodies)-1] != "hello" || cts[len(cts)-1] != "text/plain" {
		t.Errorf("upload_text: %q %q", bodies[len(bodies)-1], cts[len(cts)-1])
	}
}

func TestBigQueryPollAndSchemaRows(t *testing.T) {
	p := mustParse(t, pkgJSON(`{"resource":"","operation":"query","title":"Q","method":"POST","path":"/queries",
	  "fields":[{"key":"nums","label":"N","type":"boolean"}],
	  "poll":{"method":"GET","path":"/queries/{{ res.jobReference.jobId }}","until":"{{ res.jobComplete }}","interval_ms":1},
	  "output":{"path":"rows","schema_rows":"schema.fields","numbers":"{{ p.nums }}"}}`))
	full := `{"jobComplete":true,"jobReference":{"jobId":"j1"},
	  "schema":{"fields":[{"name":"n","type":"INTEGER"},{"name":"ok","type":"BOOLEAN"},
	    {"name":"tags","type":"STRING","mode":"REPEATED"},
	    {"name":"addr","type":"RECORD","fields":[{"name":"city","type":"STRING"}]}]},
	  "rows":[{"f":[{"v":"7"},{"v":"true"},{"v":[{"v":"a"},{"v":"b"}]},{"v":{"f":[{"v":"Oslo"}]}}]}]}`
	seen := pagedServer(t, func(n int, r *http.Request, w http.ResponseWriter) {
		if n == 1 {
			w.Write([]byte(`{"jobComplete":false,"jobReference":{"jobId":"j1"}}`))
			return
		}
		w.Write([]byte(full))
	})
	items, err := runPackageOp(p, map[string]any{"operation": "query", "nums": true}, cred, nil)
	if err != nil || len(*seen) != 2 || (*seen)[1].URL.Path != "/v1/queries/j1" {
		t.Fatalf("poll: %v, %d requests", err, len(*seen))
	}
	want := map[string]any{"n": 7.0, "ok": true, "tags": []any{"a", "b"}, "addr": map[string]any{"city": "Oslo"}}
	if len(items) != 1 || !reflect.DeepEqual(items[0].JSON, want) {
		t.Errorf("rows = %#v", items)
	}
}

func TestReplaceAndRe(t *testing.T) {
	sc := Scope{P: map[string]any{"cid": "123-456-7890", "long": "accounts/1/locations/2/reviews/ABC/reply", "bare": "ABC"}}
	for in, want := range map[string]any{
		"{{ p.cid | replace:'-','' }}":               "1234567890",
		"{{ p.long | re:'reviews/([^/]+)' }}":        "ABC",
		"{{ p.bare | re:'reviews/([^/]+)' }}":        "ABC",
		"{{ p.long | re:'locations/[0-9]+' }}":       "locations/2",
		"{{ p.cid | replace:'-',(p.bare | lower) }}": "123abc456abc7890",
	} {
		if got, err := renderString(in, sc); err != nil || got != want {
			t.Errorf("%s = %#v, %v; want %#v", in, got, err, want)
		}
	}
	if _, err := renderString("{{ p.cid | re:'(' }}", sc); err == nil {
		t.Error("bad regexp must error")
	}
}

func TestGA4ZipRows(t *testing.T) {
	p := mustParse(t, pkgJSON(`{"resource":"","operation":"report","title":"R","method":"POST","path":"/runReport",
	  "output":{"path":"rows","to":{"$merge":["{{ el.dimensionValues | zip:res.dimensionHeaders,'name','value' }}",
	    "{{ el.metricValues | zip:res.metricHeaders,'name','value' }}"]}}}`))
	pagedServer(t, func(n int, r *http.Request, w http.ResponseWriter) {
		w.Write([]byte(`{"dimensionHeaders":[{"name":"country"}],"metricHeaders":[{"name":"sessions"},{"name":"users"}],
		  "rows":[{"dimensionValues":[{"value":"NO"}],"metricValues":[{"value":"10"},{"value":"7"}]}]}`))
	})
	items, err := runPackageOp(p, map[string]any{"operation": "report"}, cred, nil)
	want := map[string]any{"country": "NO", "sessions": "10", "users": "7"}
	if err != nil || len(items) != 1 || !reflect.DeepEqual(items[0].JSON, want) {
		t.Errorf("ga4 rows = %v, %v", items, err)
	}
	if got, _ := renderString("{{ p.v | zip:p.h }}", Scope{P: map[string]any{"h": []any{"a", "b"}, "v": []any{1.0}}}); !reflect.DeepEqual(got, map[string]any{"a": 1.0}) {
		t.Errorf("plain zip = %#v", got)
	}
}

func TestOperationParamForEachAndSessionAuthHarness(t *testing.T) {
	p := mustParse(t, `{"$schema":"apipkg.v1","id":"fm","title":"FM","base_url":"https://fm.test","n8n_operation_param":"action",
	  "credentials":[{"platform":"fm","methods":["basic"],"fields":[{"key":"user","label":"U","required":true},{"key":"pass","label":"P","secret":true,"required":true}],
	    "inject":{"headers":{"Authorization":"{{ pre.response.token | prefix:'Bearer ' | default:'none' }}"}}}],
	  "operations":[{"resource":"","operation":"find","title":"F","method":"POST","path":"/find",
	    "before":[{"method":"POST","path":"/sessions"}],
	    "fields":[{"key":"tagIds","label":"T","type":"json"}],
	    "also":[{"for_each":"{{ p.tagIds }}","method":"POST","path":"/tags/{{ el }}"}]},
	    {"resource":"","operation":"other","title":"O","method":"GET","path":"/o"}]}`)
	var c opCase
	_ = json.Unmarshal([]byte(`{"params":{"action":"find","operation":"","tagIds":["a","b"]},
	  "responses":[{"response":{"token":"T1"}},{},{},{}],
	  "expect_requests":[{"method":"POST","path":"/sessions"},{"method":"POST","path":"/find"},
	    {"method":"POST","path":"/tags/a"},{"method":"POST","path":"/tags/b"}]}`), &c)
	checkCase(t, p, &p.Operations[0], c)
	// n8n_operation_param picks the op when "operation" is absent.
	if op, err := p.resolveOp(map[string]any{"action": "other"}); err != nil || op.Operation != "other" {
		t.Errorf("action param: %v %v", op, err)
	}
}

func TestPollMainRoot(t *testing.T) {
	p := mustParse(t, pkgJSON(`{"resource":"","operation":"enrich","title":"E","method":"POST","path":"/batch",
	  "poll":{"method":"GET","path":"/batch/{{ main.request_id }}","until":"{{ res.success }}","interval_ms":1}}`))
	seen := pagedServer(t, func(n int, r *http.Request, w http.ResponseWriter) {
		switch n {
		case 1:
			w.Write([]byte(`{"request_id":"r9"}`))
		case 2:
			w.Write([]byte(`{"success":false}`))
		default:
			w.Write([]byte(`{"success":true,"data":[1]}`))
		}
	})
	if _, err := runPackageOp(p, map[string]any{"operation": "enrich"}, cred, nil); err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 3 || (*seen)[2].URL.Path != "/v1/batch/r9" {
		t.Errorf("polls: %d, last %s", len(*seen), (*seen)[len(*seen)-1].URL.Path)
	}
}

func TestUploadWhenAndTemplatedMultipart(t *testing.T) {
	p := mustParse(t, pkgJSON(`{"resource":"","operation":"POST","title":"P","method":"POST","path":"/node","body_type":"multipart",
	  "fields":[{"key":"sendBinaryData","label":"S","type":"boolean"},{"key":"binaryPropertyName","label":"B","type":"text","default":"data"},{"key":"msg","label":"M","type":"text"}],
	  "body":{"message":"{{ p.msg }}"},
	  "binary":{"upload_field":"binaryPropertyName","upload_when":"{{ p.sendBinaryData }}",
	    "multipart_field":"{{ p.binaryPropertyName | split:':' | get:'-2' | default:'file' }}",
	    "property":"{{ p.binaryPropertyName | split:':' | get:'-1' }}"}}`))
	var cts, bodies []string
	pagedServer(t, func(n int, r *http.Request, w http.ResponseWriter) {
		b, _ := io.ReadAll(r.Body)
		cts, bodies = append(cts, r.Header.Get("Content-Type")), append(bodies, string(b))
		w.Write([]byte(`{}`))
	})
	in := map[string]any{"pic": "aGk=", "file_name": "a.png"}
	if _, err := runPackageOp(p, map[string]any{"operation": "POST", "sendBinaryData": true, "binaryPropertyName": "source:pic", "msg": "hi"}, cred, in); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cts[0], "multipart/form-data") || !strings.Contains(bodies[0], `name="source"; filename="a.png"`) || !strings.Contains(bodies[0], "\r\nhi\r\n") {
		t.Errorf("upload: %s\n%s", cts[0], bodies[0])
	}
	if _, err := runPackageOp(p, map[string]any{"operation": "POST", "msg": "hi"}, cred, in); err != nil {
		t.Fatal(err)
	}
	if cts[1] != "application/json" || bodies[1] != `{"message":"hi"}` {
		t.Errorf("no upload: %s %s", cts[1], bodies[1])
	}
}

func TestPluck(t *testing.T) {
	sc := Scope{P: map[string]any{"f": map[string]any{"field": []any{map[string]any{"name": "id"}, map[string]any{"name": ""}, map[string]any{"name": "name"}}}}}
	if got, err := renderString("{{ p.f.field | pluck:'name' | csv }}", sc); err != nil || got != "id,name" {
		t.Errorf("pluck = %#v, %v", got, err)
	}
}

func TestDiscordHelpers(t *testing.T) {
	sc := Scope{P: map[string]any{"color": "#ff0000", "dur": 60.0}}
	if got, _ := renderString("{{ p.color | hex }}", sc); got != int64(16711680) {
		t.Errorf("hex = %#v", got)
	}
	got, err := renderString("{{ now | epoch | add:p.dur | date:'YYYY-MM-DDTHH:mm:ssZ' }}", sc)
	ts, perr := time.Parse(time.RFC3339, stringify(got))
	if err != nil || perr != nil || ts.Sub(time.Now()) < 50*time.Second || ts.Sub(time.Now()) > 70*time.Second {
		t.Errorf("now + 60s = %v (%v %v)", got, err, perr)
	}
	if got, _ := renderString("{{ p.s | date:'YYYY-MM-DD' }}", Scope{P: map[string]any{"s": int64(1767312000)}}); got != "2026-01-02" {
		t.Errorf("epoch seconds date = %v", got)
	}
	p := mustParse(t, `{"$schema":"apipkg.v1","id":"dc","title":"D","base_url":"https://d.test",
	  "resources":[{"value":"channel","label":"C"},{"value":"message","label":"M"}],
	  "operations":[{"resource":"channel","operation":"create","title":"C","method":"POST","path":"/c"},
	    {"resource":"message","operation":"sendLegacy","title":"S","method":"POST","path":"/w"},
	    {"resource":"message","operation":"create","title":"MC","method":"POST","path":"/m"}]}`)
	if op, err := p.resolveOp(map[string]any{"operation": "sendLegacy"}); err != nil || op.Path != "/w" {
		t.Errorf("unique op fallback: %v %v", op, err)
	}
	if op, err := p.resolveOp(map[string]any{"operation": "create"}); err != nil || op.Path != "/c" {
		t.Errorf("default resource still wins: %v %v", op, err)
	}
}

func TestTriggerOnlyPackage(t *testing.T) {
	p, err := Parse([]byte(`{"$schema":"apipkg.v1","id":"eventbrite","title":"Eventbrite","base_url":"https://www.eventbriteapi.com/v3",
	  "n8n_trigger_types":["n8n-nodes-base.eventbriteTrigger"],"operations":[]}`), "test")
	if err != nil {
		t.Fatalf("trigger-only package must validate: %v", err)
	}
	if len(p.Operations) != 0 || len(p.N8nTriggerTypes) != 1 {
		t.Errorf("parsed: %+v", p)
	}
	if _, err := Parse([]byte(`{"$schema":"apipkg.v1","id":"empty","title":"E","base_url":"https://e.test","operations":[]}`), "test"); err == nil ||
		!strings.Contains(err.Error(), "n8n_trigger_types") {
		t.Errorf("no ops and no triggers must fail: %v", err)
	}
}

func TestJWTInjectGhost(t *testing.T) {
	p := mustParse(t, `{"$schema":"apipkg.v1","id":"ghost","title":"Ghost","base_url":"https://blog.test/ghost/api/admin",
	  "credentials":[{"platform":"ghost","methods":["api_key"],"fields":[{"key":"api_key","label":"K","secret":true,"required":true}],
	    "inject":{"jwt":{"prefix":"Ghost ","alg":"HS256","secret":"{{ cred.api_key | split:':' | get:'1' }}","secret_encoding":"hex",
	      "kid":"{{ cred.api_key | split:':' | get:'0' }}","claims":{"aud":"/admin/"}}}}],
	  "operations":[{"resource":"","operation":"get","title":"G","method":"GET","path":"/posts/"}]}`)
	var auth string
	pagedServer(t, func(n int, r *http.Request, w http.ResponseWriter) {
		auth = r.Header.Get("Authorization")
		w.Write([]byte(`{"posts":[]}`))
	})
	if _, err := runPackageOp(p, map[string]any{"operation": "get"}, map[string]any{"api_key": "kid1:00ff"}, nil); err != nil {
		t.Fatal(err)
	}
	tok := strings.TrimPrefix(auth, "Ghost ")
	parts := strings.Split(tok, ".")
	if !strings.HasPrefix(auth, "Ghost ") || len(parts) != 3 {
		t.Fatalf("auth = %q", auth)
	}
	hb, _ := base64.RawURLEncoding.DecodeString(parts[0])
	cb, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var h, c map[string]any
	_ = json.Unmarshal(hb, &h)
	_ = json.Unmarshal(cb, &c)
	if h["kid"] != "kid1" || h["alg"] != "HS256" || c["aud"] != "/admin/" || c["exp"].(float64)-c["iat"].(float64) != 300 {
		t.Errorf("header %v claims %v", h, c)
	}
	m := hmac.New(sha256.New, []byte{0x00, 0xff})
	m.Write([]byte(parts[0] + "." + parts[1]))
	if base64.RawURLEncoding.EncodeToString(m.Sum(nil)) != parts[2] {
		t.Error("signature mismatch")
	}
}

func TestJWTES256(t *testing.T) {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	tok, err := signJWT(&JWTInject{Alg: "ES256", Secret: "{{ cred.key }}", Claims: map[string]any{"aud": "appstoreconnect-v1"}}, Scope{Cred: map[string]any{"key": pemKey}})
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
	if len(sig) != 64 || !ecdsa.Verify(&k.PublicKey, sum[:], r, s) {
		t.Error("ES256 signature does not verify")
	}
}

func TestExtendsCannotShadowNativeOps(t *testing.T) {
	_, err := Parse([]byte(`{"$schema":"apipkg.v1","id":"gh","title":"GH","base_url":"https://api.github.test","extends":"service.github",
	  "operations":[{"resource":"","operation":"create_issue","title":"X","method":"POST","path":"/x"}]}`), "test")
	if err == nil || !strings.Contains(err.Error(), "would replace the native one") {
		t.Errorf("want shadowing error, got %v", err)
	}
	if _, err := Parse([]byte(`{"$schema":"apipkg.v1","id":"gh","title":"GH","base_url":"https://api.github.test","extends":"service.github",
	  "operations":[{"resource":"","operation":"get_user_n8n","title":"X","method":"GET","path":"/x"}]}`), "test"); err != nil {
		t.Errorf("a new resourceless op is fine: %v", err)
	}
}

func TestBase64InJSONUploadAndDownload(t *testing.T) {
	p := mustParse(t, pkgJSON(`{"resource":"","operation":"put","title":"P","method":"PUT","path":"/contents/{{ p.path }}",
	  "fields":[{"key":"path","label":"P","type":"text"},{"key":"binaryData","label":"B","type":"boolean"},{"key":"fileContent","label":"C","type":"text"},
	    {"key":"binaryPropertyName","label":"N","type":"text","default":"data"}],
	  "body":{"content":"{{ p.binaryData | then:bin.base64 | default:(p.fileContent | base64) }}"},
	  "binary":{"upload_field":"binaryPropertyName","upload_when":"{{ p.binaryData }}"}},
	  {"resource":"","operation":"get","title":"G","method":"GET","path":"/contents/{{ p.path }}",
	  "fields":[{"key":"path","label":"P","type":"text"},{"key":"asBinaryProperty","label":"A","type":"boolean"}],
	  "binary":{"download_when":"{{ p.asBinaryProperty }}","download_base64":"{{ res.content }}","file_name":"{{ res.name }}"}}`))
	var bodies []string
	pagedServer(t, func(n int, r *http.Request, w http.ResponseWriter) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		w.Write([]byte(`{"name":"a.txt","sha":"s1","content":"aGVs\nbG8="}`))
	})
	if _, err := runPackageOp(p, map[string]any{"operation": "put", "path": "a.txt", "binaryData": true}, cred, map[string]any{"data": "aGk="}); err != nil {
		t.Fatal(err)
	}
	if _, err := runPackageOp(p, map[string]any{"operation": "put", "path": "a.txt", "fileContent": "yo"}, cred, nil); err != nil {
		t.Fatal(err)
	}
	if bodies[0] != `{"content":"aGk="}` || bodies[1] != `{"content":"eW8="}` {
		t.Errorf("bodies = %v", bodies)
	}
	items, err := runPackageOp(p, map[string]any{"operation": "get", "path": "a.txt", "asBinaryProperty": true}, cred, nil)
	if err != nil || string(items[0].Binary["data"]) != "hello" || items[0].JSON["sha"] != "s1" || items[0].JSON["file_name"] != "a.txt" || items[0].JSON["mime_type"] != "text/plain; charset=utf-8" {
		t.Errorf("download: %v %v", items, err)
	}
	items, _ = runPackageOp(p, map[string]any{"operation": "get", "path": "a.txt"}, cred, nil)
	if items[0].Binary != nil || items[0].JSON["content"] == nil {
		t.Errorf("no download: %v", items)
	}
}

func TestRoundHelpScoutLinkedInHumantic(t *testing.T) {
	p := mustParse(t, pkgJSON(`{"resource":"","operation":"create","title":"C","method":"POST","path":"/conversations",
	  "fields":[{"key":"resolveData","label":"R","type":"boolean","default":true}],"body":{"x":1},
	  "output":{"to":{"id":"{{ hdr.Resource-ID }}","uri":"{{ hdr.location }}"}}},
	  {"resource":"","operation":"post","title":"P","method":"POST","path":"/rest/posts",
	   "fields":[{"key":"binaryPropertyName","label":"B","type":"text","default":"data"}],
	   "before":[{"method":"POST","path":"/rest/images","query":{"action":"initializeUpload"}},
	             {"upload":true,"method":"PUT","path":"{{ pre.value.uploadUrl }}"}],
	   "body":{"media":"{{ pres.0.value.image }}"},"binary":{"upload_field":"binaryPropertyName","upload_when":"false"}},
	  {"resource":"","operation":"profile","title":"H","method":"{{ p.resume | then:'post' | default:'GET' }}","path":"/user-profile/create",
	   "fields":[{"key":"resume","label":"R","type":"text"}]},
	  {"resource":"","operation":"list","title":"L","method":"GET","path":"/list",
	   "fields":[{"key":"returnAll","label":"A","type":"boolean"},{"key":"limit","label":"L","type":"number"}],"output":{"limit":true}}`))
	var reqs []*http.Request
	var bodies []string
	pagedServer(t, func(n int, r *http.Request, w http.ResponseWriter) {
		b, _ := io.ReadAll(r.Body)
		reqs, bodies = append(reqs, r), append(bodies, string(b))
		switch r.URL.Path {
		case "/v1/conversations":
			w.Header().Set("Resource-ID", "77")
			w.Header().Set("Location", "https://api.test/conversations/77")
			w.WriteHeader(201)
		case "/v1/rest/images":
			w.Write([]byte(`{"value":{"uploadUrl":"https://up.test/u1","image":"urn:li:image:1"}}`))
		case "/v1/list":
			w.Write([]byte(`[1,2,3,4]`))
		default:
			w.Write([]byte(`{}`))
		}
	})
	items, err := runPackageOp(p, map[string]any{"operation": "create"}, cred, nil)
	if err != nil || items[0].JSON["id"] != "77" || items[0].JSON["uri"] != "https://api.test/conversations/77" {
		t.Errorf("hdr: %v %v", items, err)
	}
	reqs, bodies = nil, nil
	if _, err := runPackageOp(p, map[string]any{"operation": "post"}, cred, map[string]any{"data": "aGk=", "file_name": "a.png"}); err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 3 || reqs[1].Method != "PUT" || reqs[1].URL.Path != "/u1" || bodies[1] != "hi" || reqs[1].Header.Get("Content-Type") != "image/png" ||
		bodies[2] != `{"media":"urn:li:image:1"}` {
		t.Errorf("linkedin upload chain: %d reqs, bodies %q, step %s %s ct=%q", len(reqs), bodies, reqs[1].Method, reqs[1].URL.Path, reqs[1].Header.Get("Content-Type"))
	}
	reqs = nil
	runPackageOp(p, map[string]any{"operation": "profile", "resume": "x"}, cred, nil)
	runPackageOp(p, map[string]any{"operation": "profile"}, cred, nil)
	if reqs[0].Method != "POST" || reqs[1].Method != "GET" {
		t.Errorf("templated method: %s %s", reqs[0].Method, reqs[1].Method)
	}
	items, _ = runPackageOp(p, map[string]any{"operation": "list", "limit": 2}, cred, nil)
	all, _ := runPackageOp(p, map[string]any{"operation": "list", "returnAll": true, "limit": 2}, cred, nil)
	if len(items) != 2 || len(all) != 4 {
		t.Errorf("output.limit: %d / %d", len(items), len(all))
	}
}

func TestBracketQueryStyle(t *testing.T) {
	p := mustParse(t, pkgJSON(`{"resource":"","operation":"list","title":"L","method":"GET","path":"/products","query_style":"brackets",
	  "fields":[{"key":"filters","label":"F","type":"json"},{"key":"returnAll","label":"A","type":"boolean"},{"key":"limit","label":"L","type":"number","default":5}],
	  "query":{"search_criteria":{"filter_groups":[{"filters":"{{ p.filters }}"}]}},
	  "pagination":{"type":"page","page_param":"search_criteria[current_page]","limit_param":"search_criteria[page_size]","page_size":5,"start_page":1,"results_path":"items"}}`))
	seen := pagedServer(t, func(n int, r *http.Request, w http.ResponseWriter) {
		w.Write([]byte(`{"items":[{"sku":"a"}]}`))
	})
	if _, err := runPackageOp(p, map[string]any{"operation": "list", "filters": []any{map[string]any{"field": "sku", "value": "x", "condition_type": "eq"}}}, cred, nil); err != nil {
		t.Fatal(err)
	}
	q := (*seen)[0].URL.Query()
	if q.Get("search_criteria[filter_groups][0][filters][0][field]") != "sku" || q.Get("search_criteria[filter_groups][0][filters][0][condition_type]") != "eq" ||
		q.Get("search_criteria[current_page]") != "1" || q.Get("search_criteria[page_size]") != "5" {
		t.Errorf("query = %v", q)
	}
}

func TestJoinBuilder(t *testing.T) {
	sc := Scope{P: map[string]any{"name": "Ada", "dep": 5.0, "none": "",
		"sort": []any{map[string]any{"field": "a", "direction": "desc"}, map[string]any{"field": "b"}}}}
	join := map[string]any{"$join": []any{"{{ p.name | prefix:\"name:'\" | suffix:\"'\" }}", "{{ p.none | prefix:'x:' }}", "{{ p.dep | prefix:'department_id:' }}"},
		"$sep": " AND ", "$prefix": "\"", "$suffix": "\""}
	if got, err := renderValue(join, sc); err != nil || got != `"name:'Ada' AND department_id:5"` {
		t.Errorf("join = %#v, %v", got, err)
	}
	empty := map[string]any{"$join": []any{"{{ p.none }}"}, "$sep": ",", "$prefix": "\""}
	if got, _ := renderValue(empty, sc); got != nil {
		t.Errorf("empty join = %#v", got)
	}
	grist := map[string]any{"$join": map[string]any{"$map": "{{ p.sort }}", "$to": "{{ el.direction | eq:'desc' | then:'-' }}{{ el.field }}"}, "$sep": ","}
	if got, _ := renderValue(grist, sc); got != "-a,b" {
		t.Errorf("grist = %#v", got)
	}
}

func TestGraphQLBodyPagination(t *testing.T) {
	p := mustParse(t, pkgJSON(`{"resource":"","operation":"issues","title":"I","method":"POST","path":"/graphql",
	  "fields":[{"key":"returnAll","label":"A","type":"boolean"}],
	  "body":{"query":"query($first:Int,$after:String){issues(first:$first,after:$after){nodes{id} pageInfo{hasNextPage endCursor}}}","variables":{"x":1}},
	  "pagination":{"type":"cursor","param_in":"body","cursor_param":"variables.after","limit_param":"variables.first","page_size":2,
	    "cursor_path":"data.issues.pageInfo.endCursor","has_more_path":"data.issues.pageInfo.hasNextPage","results_path":"data.issues.nodes"}}`))
	var bodies []map[string]any
	pagedServer(t, func(n int, r *http.Request, w http.ResponseWriter) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		if n == 1 {
			w.Write([]byte(`{"data":{"issues":{"nodes":[{"id":1},{"id":2}],"pageInfo":{"hasNextPage":true,"endCursor":"c1"}}}}`))
			return
		}
		w.Write([]byte(`{"data":{"issues":{"nodes":[{"id":3}],"pageInfo":{"hasNextPage":false}}}}`))
	})
	items, err := runPackageOp(p, map[string]any{"operation": "issues", "returnAll": true}, cred, nil)
	if err != nil || len(items) != 3 {
		t.Fatalf("items %v %v", items, err)
	}
	v2 := bodies[1]["variables"].(map[string]any)
	if v2["after"] != "c1" || v2["first"] != 2.0 || v2["x"] != 1.0 || bodies[1]["after"] != nil {
		t.Errorf("second page body = %v", bodies[1])
	}
}

func TestValuesFromMergeAndWebDAV(t *testing.T) {
	p := mustParse(t, pkgJSON(`{"resource":"","operation":"lights","title":"L","method":"GET","path":"/lights","output":{"from":"{{ res | values }}"}},
	  {"resource":"","operation":"update","title":"U","method":"PUT","path":"/lights/1/state","output":{"each":"success","merge":true}},
	  {"resource":"","operation":"versions","title":"V","method":"GET","path":"/pkg","output":{"from":"{{ res.time | entries:'version,published_at' }}"}},
	  {"resource":"","operation":"list","title":"F","method":"PROPFIND","path":"/dav/files/a/","headers":{"Depth":"1"},
	   "output":{"path":"multistatus.response"}}`))
	var methods []string
	pagedServer(t, func(n int, r *http.Request, w http.ResponseWriter) {
		methods = append(methods, r.Method)
		switch r.URL.Path {
		case "/v1/lights":
			w.Write([]byte(`{"2":{"name":"b"},"1":{"name":"a"}}`))
		case "/v1/lights/1/state":
			w.Write([]byte(`[{"success":{"/lights/1/state/on":true}},{"success":{"/lights/1/state/bri":200}}]`))
		case "/v1/pkg":
			w.Write([]byte(`{"time":{"1.0.0":"2020","1.1.0":"2021"}}`))
		default:
			w.Write([]byte(`<?xml version="1.0"?><d:multistatus xmlns:d="DAV:"><d:response><d:href>/a/x.txt</d:href></d:response><d:response><d:href>/a/y</d:href><d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop></d:propstat></d:response></d:multistatus>`))
		}
	})
	items, err := runPackageOp(p, map[string]any{"operation": "lights"}, cred, nil)
	if err != nil || len(items) != 2 || items[0].JSON["name"] != "a" {
		t.Errorf("values: %v %v", items, err)
	}
	items, _ = runPackageOp(p, map[string]any{"operation": "update"}, cred, nil)
	if len(items) != 1 || items[0].JSON["/lights/1/state/bri"] != 200.0 || items[0].JSON["/lights/1/state/on"] != true {
		t.Errorf("merge: %v", items)
	}
	items, _ = runPackageOp(p, map[string]any{"operation": "versions"}, cred, nil)
	if len(items) != 2 || items[1].JSON["version"] != "1.1.0" {
		t.Errorf("entries from: %v", items)
	}
	items, err = runPackageOp(p, map[string]any{"operation": "list"}, cred, nil)
	if err != nil || len(items) != 2 || items[0].JSON["href"] != "/a/x.txt" || methods[len(methods)-1] != "PROPFIND" {
		t.Errorf("webdav: %v %v %v", items, err, methods)
	}
}

func TestPagedValuesInjectBodyAndPathWildcard(t *testing.T) {
	p := mustParse(t, `{"$schema":"apipkg.v1","id":"mt","title":"M","base_url":"https://m.test",
	  "credentials":[{"platform":"mt","methods":["api_key"],"fields":[{"key":"api_key","label":"K","secret":true,"required":true}],
	    "inject":{"body":{"key":"{{ cred.api_key }}"}}}],
	  "operations":[{"resource":"","operation":"contacts","title":"C","method":"GET","path":"/contacts",
	    "fields":[{"key":"returnAll","label":"A","type":"boolean"}],
	    "pagination":{"type":"offset","offset_param":"start","limit_param":"limit","page_size":2,"results_path":"contacts","values":true}},
	   {"resource":"","operation":"send","title":"S","method":"POST","path":"/send/{{ uuid }}","body":{"text":"hi"}}]}`)
	var bodies []string
	pagedServer(t, func(n int, r *http.Request, w http.ResponseWriter) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		switch r.URL.Query().Get("start") {
		case "0":
			w.Write([]byte(`{"total":3,"contacts":{"12":{"id":12},"9":{"id":9}}}`))
		case "2":
			w.Write([]byte(`{"total":3,"contacts":{"100":{"id":100}}}`))
		default:
			w.Write([]byte(`{"ok":true}`))
		}
	})
	c := map[string]any{"api_key": "K1"}
	items, err := runPackageOp(p, map[string]any{"operation": "contacts", "returnAll": true}, c, nil)
	if err != nil || len(items) != 3 || items[0].JSON["id"] != 9.0 || items[2].JSON["id"] != 100.0 {
		t.Errorf("paged values: %v %v", items, err)
	}
	bodies = nil
	if _, err := runPackageOp(p, map[string]any{"operation": "send"}, c, nil); err != nil {
		t.Fatal(err)
	}
	if bodies[0] != `{"key":"K1","text":"hi"}` {
		t.Errorf("inject.body: %s", bodies[0])
	}
	if !pathMatches("/send/abc-123", "/send/<any>") || pathMatches("/send/", "/send/<any>") || pathMatches("/x/a", "/send/<any>") {
		t.Error("pathMatches")
	}
}

func TestMultiFileUploadsAndTypedInjectBody(t *testing.T) {
	p := mustParse(t, `{"$schema":"apipkg.v1","id":"mg","title":"MG","base_url":"https://mg.test",
	  "credentials":[{"platform":"mg","methods":["api_key"],"fields":[{"key":"api_key","label":"K","secret":true,"required":true},{"key":"sandbox","label":"S"}],
	    "inject":{"body":{"key":"{{ cred.api_key }}","SandboxMode":"{{ cred.sandbox | bool }}"}}}],
	  "operations":[{"resource":"","operation":"send","title":"S","method":"POST","path":"/messages","body_type":"multipart",
	    "fields":[{"key":"attachments","label":"A","type":"text"},{"key":"binaryPropertyName","label":"B","type":"text","default":"data"}],
	    "body":{"to":"a@b"},
	    "binary":{"upload_field":"binaryPropertyName","properties":"{{ p.attachments }}","multipart_field":"attachment[{{ el.index }}]"}},
	   {"resource":"","operation":"json","title":"J","method":"POST","path":"/json",
	    "fields":[{"key":"attachments","label":"A","type":"text"},{"key":"binaryPropertyName","label":"B","type":"text","default":"data"}],
	    "body":{"attachments":{"$map":"{{ bin.files }}","$to":{"name":"{{ el.file_name }}","content":"{{ el.base64 }}"}}},
	    "binary":{"upload_field":"binaryPropertyName","properties":"{{ p.attachments }}"}}]}`)
	var bodies []string
	pagedServer(t, func(n int, r *http.Request, w http.ResponseWriter) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		w.Write([]byte(`{}`))
	})
	in := map[string]any{"a": "aGk=", "b": "eW8=", "file_name": "f.txt"}
	c := map[string]any{"api_key": "K", "sandbox": "true"}
	if _, err := runPackageOp(p, map[string]any{"operation": "send", "attachments": "a, b"}, c, in); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bodies[0], `name="attachment[0]"`) || !strings.Contains(bodies[0], `name="attachment[1]"`) ||
		!strings.Contains(bodies[0], "\r\nhi\r\n") || !strings.Contains(bodies[0], "\r\nyo\r\n") {
		t.Errorf("multipart: %s", bodies[0])
	}
	if _, err := runPackageOp(p, map[string]any{"operation": "json", "attachments": "a,b"}, c, in); err != nil {
		t.Fatal(err)
	}
	var j map[string]any
	_ = json.Unmarshal([]byte(bodies[1]), &j)
	at, _ := j["attachments"].([]any)
	if len(at) != 2 || at[1].(map[string]any)["content"] != "eW8=" || j["SandboxMode"] != true || j["key"] != "K" {
		t.Errorf("json files / typed inject: %s", bodies[1])
	}
}

// Pushbullet file push: request an upload slot (needs the file name and
// type), POST the file as multipart to the presigned URL, then create the
// push. upload_when "false" (a literal) keeps the main request plain.
func TestPresignedMultipartUploadStep(t *testing.T) {
	p := mustParse(t, pkgJSON(`{"resource":"","operation":"push","title":"P","method":"POST","path":"/pushes",
	  "fields":[{"key":"binaryPropertyName","label":"B","type":"text","default":"data"}],
	  "before":[{"method":"POST","path":"/upload-request","body":{"file_name":"{{ bin.file_name }}","file_type":"{{ bin.mime_type }}"}},
	    {"upload":true,"method":"POST","path":"{{ pre.upload_url }}","body_type":"multipart","body":{"awsaccesskeyid":"{{ pre.data.awsaccesskeyid }}"}}],
	  "body":{"type":"file","file_url":"{{ pres.0.file_url }}"},
	  "binary":{"upload_field":"binaryPropertyName","upload_when":"false","multipart_field":"file"}}`))
	var cts, bodies []string
	pagedServer(t, func(n int, r *http.Request, w http.ResponseWriter) {
		b, _ := io.ReadAll(r.Body)
		cts, bodies = append(cts, r.Header.Get("Content-Type")), append(bodies, string(b))
		if n == 1 {
			w.Write([]byte(`{"upload_url":"https://s3.test/up","file_url":"https://dl.test/f.png","data":{"awsaccesskeyid":"AK"}}`))
			return
		}
		w.Write([]byte(`{}`))
	})
	if _, err := runPackageOp(p, map[string]any{"operation": "push"}, cred, map[string]any{"data": "aGk=", "file_name": "f.png"}); err != nil {
		t.Fatal(err)
	}
	if bodies[0] != `{"file_name":"f.png","file_type":"image/png"}` {
		t.Errorf("upload request: %s", bodies[0])
	}
	if !strings.HasPrefix(cts[1], "multipart/form-data") || !strings.Contains(bodies[1], `name="awsaccesskeyid"`) || !strings.Contains(bodies[1], `name="file"; filename="f.png"`) {
		t.Errorf("presigned post: %s %s", cts[1], bodies[1])
	}
	if cts[2] != "application/json" || bodies[2] != `{"file_url":"https://dl.test/f.png","type":"file"}` {
		t.Errorf("push: %s %s", cts[2], bodies[2])
	}
}

func TestListSnakeSkipSortAndParenHead(t *testing.T) {
	sc := Scope{P: map[string]any{"db": "odoo", "uid": 2.0, "name": "Ada"}}
	got, err := renderValue(map[string]any{"$list": []any{"{{ p.db }}", "{{ p.uid }}", "", map[string]any{}, []any{}, "{{ p.none }}"}}, sc)
	if err != nil || !reflect.DeepEqual(got, []any{"odoo", 2.0, "", map[string]any{}, []any{}, nil}) {
		t.Errorf("$list = %#v, %v", got, err)
	}
	for in, want := range map[string]string{"Due Date": "due_date", "DueDate": "due_date", "due-date": "due_date", "HTTPServer2": "http_server2", "Q1 Goals": "q1_goals", "Prénom": "prénom", "already_snake": "already_snake"} {
		if g := snakeCase(in); g != want {
			t.Errorf("snake(%q) = %q, want %q", in, g, want)
		}
	}
	if got, err := renderString("{{ (p.name | lower) | prefix:'x-' }}", sc); err != nil || got != "x-ada" {
		t.Errorf("paren head = %#v, %v", got, err)
	}
	p := mustParse(t, pkgJSON(`{"resource":"","operation":"ls","title":"L","method":"GET","path":"/ls","output":{"skip":1,"sort":"v","sort_desc":true}}`))
	pagedServer(t, func(n int, r *http.Request, w http.ResponseWriter) {
		w.Write([]byte(`[{"v":0},{"v":2},{"v":10},{"v":1}]`))
	})
	items, _ := runPackageOp(p, map[string]any{"operation": "ls"}, cred, nil)
	if len(items) != 3 || items[0].JSON["v"] != 10.0 || items[2].JSON["v"] != 1.0 {
		t.Errorf("skip+sort: %v", items)
	}
}

func TestPaginationWhenAndTemplatedParams(t *testing.T) {
	p := mustParse(t, `{"$schema":"apipkg.v1","id":"st","title":"S","base_url":"https://s.test",
	  "credentials":[{"platform":"st","methods":["api_key"],"fields":[{"key":"api_key","label":"K","required":true},{"key":"api_version","label":"V"}],
	    "inject":{"headers":{"Authorization":"Bearer {{ cred.api_key }}"}}}],
	  "operations":[{"resource":"","operation":"list","title":"L","method":"GET","path":"/entries",
	    "fields":[{"key":"returnAll","label":"A","type":"boolean"}],
	    "output":{"path":"{{ cred.api_version | eq:'v4' | then:'data' }}"},
	    "pagination":{"type":"page","when":"{{ cred.api_version | eq:'v4' }}","page_param":"{{ cred.api_version | eq:'v4' | then:'pagination[page]' }}",
	      "limit_param":"pagination[pageSize]","page_size":2,"start_page":1,"results_path":"data"}}]}`)
	var qs []string
	pagedServer(t, func(n int, r *http.Request, w http.ResponseWriter) {
		qs = append(qs, r.URL.RawQuery)
		if r.URL.Query().Get("pagination[page]") == "" {
			w.Write([]byte(`[{"id":1},{"id":2},{"id":3}]`))
			return
		}
		if r.URL.Query().Get("pagination[page]") == "1" {
			w.Write([]byte(`{"data":[{"id":1},{"id":2}]}`))
			return
		}
		w.Write([]byte(`{"data":[{"id":3}]}`))
	})
	v4, err := runPackageOp(p, map[string]any{"operation": "list", "returnAll": true}, map[string]any{"api_key": "k", "api_version": "v4"}, nil)
	if err != nil || len(v4) != 3 || len(qs) != 2 {
		t.Fatalf("v4 paging: %d items, %v, %v", len(v4), qs, err)
	}
	qs = nil
	v3, err := runPackageOp(p, map[string]any{"operation": "list", "returnAll": true}, map[string]any{"api_key": "k", "api_version": "v3"}, nil)
	if err != nil || len(v3) != 3 || len(qs) != 1 || qs[0] != "" {
		t.Errorf("v3 no paging: %d items, %v, %v", len(v3), qs, err)
	}
}

func TestIdxMatchesInFlatten(t *testing.T) {
	sc := Scope{P: map[string]any{"dates": []any{"d1", "d2"}, "mrr": []any{10.0, 20.0}, "v": "TODAY", "lists": map[string]any{"update": []any{1.0}, "remove": []any{2.0, 3.0}}}}
	got, err := renderValue(map[string]any{"$map": "{{ p.dates }}", "$to": map[string]any{"date": "{{ el }}", "mrr": "{{ p.mrr | get:(idx | string) }}"}}, sc)
	want := []any{map[string]any{"date": "d1", "mrr": 10.0}, map[string]any{"date": "d2", "mrr": 20.0}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("idx pivot = %#v, %v", got, err)
	}
	for in, w := range map[string]any{
		"{{ p.v | matches:'^(TODAY|YESTERDAY|LAST_N_DAYS:[0-9]+)$' }}": true,
		"{{ p.v | in:'TODAY,TOMORROW' }}":                              true,
		"{{ p.v | in:'NOW' }}":                                         nil,
	} {
		if g, _ := renderString(in, sc); g != w {
			t.Errorf("%s = %#v", in, g)
		}
	}
	if g, _ := renderString("{{ p.lists | flatten }}", sc); !reflect.DeepEqual(g, []any{2.0, 3.0, 1.0}) {
		t.Errorf("flatten = %#v", g)
	}
}

func TestUnleashedTwistUprocTheHiveRound(t *testing.T) {
	p := mustParse(t, `{"$schema":"apipkg.v1","id":"ul","title":"U","base_url":"https://u.test","n8n_resource_param":"group",
	  "resources":[{"value":"sales","label":"S"},{"value":"tools","label":"T"}],
	  "credentials":[{"platform":"ul","methods":["api_key"],"fields":[{"key":"api_key","label":"K","secret":true,"required":true}],
	    "inject":{"hmac":{"header":"api-auth-signature","secret":"{{ cred.api_key }}","message":"{{ req.query }}"}}}],
	  "operations":[{"resource":"sales","operation":"getAll","title":"G","method":"GET","path":"/SalesOrders/{{ page.pageNumber }}",
	    "fields":[{"key":"returnAll","label":"A","type":"boolean"}],"query":{"orderStatus":"Placed"},
	    "pagination":{"type":"page","param_in":"path","page_param":"pageNumber","start_page":1,"page_size":2,"results_path":"Items"}},
	   {"resource":"tools","operation":"post","title":"P","method":"POST","path":"/msg","body_type":"form",
	    "fields":[{"key":"files","label":"F","type":"text"},{"key":"binaryPropertyName","label":"B","type":"text","default":"data"},{"key":"cf","label":"C","type":"json"}],
	    "before":[{"for_each":"{{ p.files | split }}","upload":true,"method":"POST","path":"/attachments/upload"}],
	    "body":{"attachments":"{{ pre | json }}","$merge":"{{ p.cf | unflatten }}"},
	    "binary":{"upload_field":"binaryPropertyName","upload_when":"false"},
	    "pagination":{"type":"offset","param_in":"body","offset_param":"offset","limit_param":"limit","page_size":5,"results_path":"x"}}]}`)
	var reqs []*http.Request
	var bodies []string
	pagedServer(t, func(n int, r *http.Request, w http.ResponseWriter) {
		b, _ := io.ReadAll(r.Body)
		reqs, bodies = append(reqs, r), append(bodies, string(b))
		switch {
		case r.URL.Path == "/SalesOrders/1":
			w.Write([]byte(`{"Items":[{"id":1},{"id":2}]}`))
		case r.URL.Path == "/SalesOrders/2":
			w.Write([]byte(`{"Items":[{"id":3}]}`))
		case r.URL.Path == "/attachments/upload":
			w.Write([]byte(`{"name":"` + string(b) + `"}`))
		default:
			w.Write([]byte(`{"x":[]}`))
		}
	})
	c := map[string]any{"api_key": "S3CR"}
	items, err := runPackageOp(p, map[string]any{"group": "sales", "operation": "getAll", "returnAll": true}, c, nil)
	if err != nil || len(items) != 3 {
		t.Fatalf("unleashed: %v %v", items, err)
	}
	m := hmac.New(sha256.New, []byte("S3CR"))
	m.Write([]byte("orderStatus=Placed"))
	if reqs[0].Header.Get("api-auth-signature") != base64.StdEncoding.EncodeToString(m.Sum(nil)) || reqs[0].URL.Query().Get("pageNumber") != "" {
		t.Errorf("hmac/page path: %q %s", reqs[0].Header.Get("api-auth-signature"), reqs[0].URL)
	}
	reqs, bodies = nil, nil
	if _, err := runPackageOp(p, map[string]any{"group": "tools", "operation": "post", "files": "a,b", "cf": map[string]any{"customFields.x": 1.0}}, c,
		map[string]any{"a": "aGk=", "b": "eW8="}); err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 3 || bodies[0] != "hi" || bodies[1] != "yo" {
		t.Fatalf("twist uploads: %d reqs %q", len(reqs), bodies)
	}
	if got := reqs[2].Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" ||
		!strings.Contains(bodies[2], "offset=0") || !strings.Contains(bodies[2], "attachments=") || !strings.Contains(bodies[2], "customFields=") {
		t.Errorf("form paging / collected pres / unflatten: %s %s", got, bodies[2])
	}
	if g, _ := renderString("{{ p.cf | unflatten }}", Scope{P: map[string]any{"cf": map[string]any{"a.b": 1.0, "c": 2.0}}}); !reflect.DeepEqual(g, map[string]any{"a": map[string]any{"b": 1.0}, "c": 2.0}) {
		t.Errorf("unflatten = %#v", g)
	}
}

func TestInsecureClientOptIn(t *testing.T) {
	old := httpClient
	httpClient = &http.Client{}
	t.Cleanup(func() { httpClient = old })
	r := &opRun{scope: Scope{Cred: map[string]any{"allow_unauthorized_certs": true}}}
	tr, ok := r.client().Transport.(*http.Transport)
	if !ok || tr.TLSClientConfig == nil || !tr.TLSClientConfig.InsecureSkipVerify {
		t.Error("allow_unauthorized_certs must skip TLS verification")
	}
	if (&opRun{scope: Scope{Cred: map[string]any{}}}).client() != httpClient {
		t.Error("default client must verify TLS")
	}
}

func TestThenTreatsFalseAsUnset(t *testing.T) {
	sc := Scope{P: map[string]any{"t": true, "f": false, "fs": "false", "z": 0.0}}
	for in, want := range map[string]any{
		"{{ p.t | then:'x' }}": "x", "{{ p.f | then:'x' }}": nil, "{{ p.fs | then:'x' }}": nil, "{{ p.z | then:'x' }}": "x",
		"{{ p.f | then:'x' | default:'y' }}": "y",
	} {
		if got, _ := renderString(in, sc); got != want {
			t.Errorf("%s = %#v, want %#v", in, got, want)
		}
	}
}

func TestAzureSigners(t *testing.T) {
	old := azureNow
	t.Cleanup(func() { azureNow = old })
	azureNow = func() time.Time { return time.Date(2017, 4, 27, 0, 51, 12, 0, time.UTC) }
	// Cosmos: Microsoft's published example (GET dbs/ToDoList).
	req, _ := http.NewRequest("GET", "https://acct.documents.azure.com/dbs/ToDoList", nil)
	key := "dsZQi3KtZmCv1ljt3VNWNm7sQUF1y5rJfC6kv5JiwvW0EndXdDku/dkKBp8/ufDToSxLzR4y+O/0H/t4bQtVNw=="
	if err := signAzureCosmos(req, &AzureCosmosInject{Key: "{{ cred.key }}"}, Scope{Cred: map[string]any{"key": key}}); err != nil {
		t.Fatal(err)
	}
	tok, _ := url.QueryUnescape(req.Header.Get("authorization"))
	if tok != "type=master&ver=1.0&sig=c09PEVJrgp2uQRkr934kFbTqhByc7TVr3OHyqlu+c+c=" || req.Header.Get("x-ms-date") != "Thu, 27 Apr 2017 00:51:12 GMT" {
		t.Errorf("cosmos token = %q", tok)
	}
	if typ, link := cosmosResource("/dbs/db1/colls/c1/docs"); typ != "docs" || link != "dbs/db1/colls/c1" {
		t.Errorf("list link: %s %s", typ, link)
	}
	if typ, link := cosmosResource("/dbs/db1/colls/c1/docs/d1"); typ != "docs" || link != "dbs/db1/colls/c1/docs/d1" {
		t.Errorf("item link: %s %s", typ, link)
	}
	// Storage SharedKey: the canonical string built independently.
	sreq, _ := http.NewRequest("GET", "https://acct.blob.core.windows.net/cont?restype=container&comp=list", nil)
	sreq.Header.Set("x-ms-version", "2021-08-06")
	skey := base64.StdEncoding.EncodeToString([]byte("secret-key"))
	if err := signAzureSharedKey(sreq, &AzureSharedKeyInject{Account: "acct", Key: skey}, Scope{}); err != nil {
		t.Fatal(err)
	}
	canon := "GET\n\n\n\n\n\n\n\n\n\n\n\nx-ms-date:Thu, 27 Apr 2017 00:51:12 GMT\nx-ms-version:2021-08-06\n/acct/cont\ncomp:list\nrestype:container"
	m := hmac.New(sha256.New, []byte("secret-key"))
	m.Write([]byte(canon))
	if want := "SharedKey acct:" + base64.StdEncoding.EncodeToString(m.Sum(nil)); sreq.Header.Get("Authorization") != want {
		t.Errorf("shared key = %q, want %q", sreq.Header.Get("Authorization"), want)
	}
}

func TestHeaderCursorPagination(t *testing.T) {
	p := mustParse(t, pkgJSON(`{"resource":"","operation":"docs","title":"D","method":"GET","path":"/docs",
	  "fields":[{"key":"returnAll","label":"A","type":"boolean"}],
	  "pagination":{"type":"header_cursor","cursor_param":"x-ms-continuation","cursor_path":"x-ms-continuation","results_path":"Documents"}}`))
	seen := pagedServer(t, func(n int, r *http.Request, w http.ResponseWriter) {
		if n == 1 {
			w.Header().Set("x-ms-continuation", "tok1")
			w.Write([]byte(`{"Documents":[{"id":1}]}`))
			return
		}
		w.Write([]byte(`{"Documents":[{"id":2}]}`))
	})
	items, err := runPackageOp(p, map[string]any{"operation": "docs", "returnAll": true}, cred, nil)
	if err != nil || len(items) != 2 || (*seen)[1].Header.Get("x-ms-continuation") != "tok1" || (*seen)[0].Header.Get("x-ms-continuation") != "" {
		t.Errorf("header cursor: %v %v", items, err)
	}
}

func TestDottedKeyLookup(t *testing.T) {
	doc := map[string]any{"@odata.nextLink": "https://graph.test/next", "@odata.count": 3.0, "a": map[string]any{"b.c": map[string]any{"d": 1.0}}, "x": map[string]any{"y": 2.0}}
	for path, want := range map[string]any{"@odata.nextLink": "https://graph.test/next", "@odata.count": 3.0, "a.b.c.d": 1.0, "x.y": 2.0, "x.z": nil} {
		if got := lookupPath(doc, strings.Split(path, ".")); got != want {
			t.Errorf("%s = %#v, want %#v", path, got, want)
		}
	}
}

func TestFlattenNested(t *testing.T) {
	sc := Scope{P: map[string]any{"docs": []any{
		map[string]any{"SummaryFields": []any{map[string]any{"a": 1.0}}},
		map[string]any{"SummaryFields": []any{map[string]any{"b": 2.0}, map[string]any{"c": 3.0}}}}}}
	got, _ := renderString("{{ p.docs | pluck:'SummaryFields' | flatten }}", sc)
	if l, ok := got.([]any); !ok || len(l) != 3 || l[2].(map[string]any)["c"] != 3.0 {
		t.Errorf("nested flatten = %#v", got)
	}
}

func TestStepWithoutAuth(t *testing.T) {
	p := mustParse(t, pkgJSON(`{"resource":"","operation":"get","title":"G","method":"GET","path":"/job",
	  "output":{"expand":{"method":"GET","path":"{{ el.uri }}","auth":false}}}`))
	seen := pagedServer(t, func(n int, r *http.Request, w http.ResponseWriter) {
		if n == 1 {
			w.Write([]byte(`{"uri":"https://bucket.s3.test/t.json?X-Amz-Signature=abc"}`))
			return
		}
		w.Write([]byte(`{"transcript":"hi"}`))
	})
	items, err := runPackageOp(p, map[string]any{"operation": "get"}, cred, nil)
	if err != nil || items[0].JSON["transcript"] != "hi" {
		t.Fatalf("%v %v", items, err)
	}
	if (*seen)[0].Header.Get("Authorization") != "Bearer T" || (*seen)[1].Header.Get("Authorization") != "" {
		t.Errorf("auth headers: %q %q", (*seen)[0].Header.Get("Authorization"), (*seen)[1].Header.Get("Authorization"))
	}
}

func TestObjectCursorInBody(t *testing.T) {
	p := mustParse(t, pkgJSON(`{"resource":"","operation":"scan","title":"S","method":"POST","path":"/","body":{"TableName":"t"},
	  "fields":[{"key":"returnAll","label":"A","type":"boolean"}],
	  "pagination":{"type":"cursor","param_in":"body","cursor_param":"ExclusiveStartKey","cursor_path":"LastEvaluatedKey","results_path":"Items"}}`))
	var bodies []map[string]any
	pagedServer(t, func(n int, r *http.Request, w http.ResponseWriter) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		if n == 1 {
			w.Write([]byte(`{"Items":[{"id":{"S":"1"}}],"LastEvaluatedKey":{"id":{"S":"1"}}}`))
			return
		}
		w.Write([]byte(`{"Items":[{"id":{"S":"2"}}]}`))
	})
	items, err := runPackageOp(p, map[string]any{"operation": "scan", "returnAll": true}, cred, nil)
	if err != nil || len(items) != 2 {
		t.Fatalf("%v %v", items, err)
	}
	if !reflect.DeepEqual(bodies[1]["ExclusiveStartKey"], map[string]any{"id": map[string]any{"S": "1"}}) || bodies[0]["ExclusiveStartKey"] != nil {
		t.Errorf("start keys: %v / %v", bodies[0], bodies[1])
	}
}
