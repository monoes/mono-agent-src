package apipkg

import (
	"encoding/json"
	"github.com/monoes/mono-agent/internal/testhome"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

func outputKeys(outs []workflow.NodeSchemaOutput) string {
	var ks []string
	for _, o := range outs {
		ks = append(ks, o.Key+":"+o.Type)
	}
	return strings.Join(ks, ",")
}

func TestDeriveOutputs(t *testing.T) {
	open := responseOutputKey + ":any"
	page := &Pagination{Type: PageCursor}
	cases := []struct {
		name string
		op   Operation
		want string
	}{
		{"no output settings: open response", Operation{}, open},
		{"list op: open response + pagination_truncated", Operation{Pagination: page}, open + ",pagination_truncated:boolean"},
		{"output.to: its keys", Operation{Output: &Output{To: map[string]any{
			"id": "{{ el.id }}", "kind": "file", "ok": true, "n": 2.0, "tags": []any{}, "meta": map[string]any{}}}},
			"id:any,kind:string,meta:object,n:number,ok:boolean,tags:array,data:any"},
		{"output.to with $merge stays open", Operation{Output: &Output{To: map[string]any{"id": "{{ el.id }}", "$merge": "{{ el.values }}"}}}, "id:any,data:any," + open},
		{"output.to_when may skip the reshape", Operation{Output: &Output{To: map[string]any{"id": "x"}, ToWhen: "{{ p.simple }}"}}, open},
		{"output.to as a template", Operation{Output: &Output{To: "{{ el | dotnet_date }}"}}, open},
		{"binary download", Operation{Binary: &Binary{Download: true}},
			"mime_type:string,file_name:string,size:number,binary_property:string"},
		{"download_when: file or response", Operation{Binary: &Binary{DownloadWhen: "{{ p.download }}"}},
			"mime_type:string,file_name:string,size:number,binary_property:string," + open},
		{"download_base64 keeps the JSON", Operation{Binary: &Binary{Download: true, DownloadBase64: "{{ res.content }}"}}, open},
		{"upload only: the response", Operation{Binary: &Binary{UploadField: "binaryPropertyName"}}, open},
		{"bulk output joins the op's", Operation{Output: &Output{To: map[string]any{"id": "x"}}, Bulk: &Bulk{Output: &Output{To: map[string]any{"took": "{{ el.took }}"}}}},
			"id:string,data:any,took:any"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := outputKeys(deriveOutputs(&c.op)); got != c.want {
				t.Errorf("got %s\nwant %s", got, c.want)
			}
		})
	}
}

func TestDeclaredOutputsWin(t *testing.T) {
	decl := []workflow.NodeSchemaOutput{{Key: "id", Type: "string"}}
	pkgDecl := []workflow.NodeSchemaOutput{{Key: "ok", Type: "boolean"}}
	p := &Package{Outputs: pkgDecl}
	if got := outputKeys(p.opOutputs(&Operation{Outputs: decl, Binary: &Binary{Download: true}})); got != "id:string" {
		t.Errorf("op outputs: %s", got)
	}
	if got := outputKeys(p.opOutputs(&Operation{})); got != "ok:boolean" {
		t.Errorf("package default: %s", got)
	}
	p.Outputs = nil
	if got := outputKeys(p.opOutputs(&Operation{})); got != responseOutputKey+":any" {
		t.Errorf("derived: %s", got)
	}
}

const outputsPkg = `{"$schema":"apipkg.v1","id":"outs","title":"Outs","base_url":"https://x.test",
 "resources":[{"value":"file","label":"File"},{"value":"item","label":"Item"}],
 "operations":[
  {"resource":"file","operation":"download","title":"Download","method":"GET","path":"/f","binary":{"download":true}},
  {"resource":"item","operation":"get","title":"Get","method":"GET","path":"/i","output":{"to":{"id":"{{ el.id }}","name":"{{ el.name }}"}}},
  {"resource":"item","operation":"list","title":"List","method":"GET","path":"/i","pagination":{"type":"cursor","cursor_param":"c","cursor_path":"next"}},
  {"resource":"item","operation":"count","title":"Count","method":"GET","path":"/n","outputs":[{"key":"count","type":"number","description":"how many"}]}
 ]}`

func TestNodeSchemaOutputs(t *testing.T) {
	installPkg(t, map[string]string{"outs": outputsPkg})
	raw, ok := workflow.ReadEmbeddedSchema("app.outs")
	if !ok {
		t.Fatal("no schema")
	}
	var doc struct {
		Outputs []struct {
			Key         string  `json:"key"`
			Type        string  `json:"type"`
			Description string  `json:"description"`
			VisibleFor  []opRef `json:"visible_for"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, o := range doc.Outputs {
		var ops []string
		for _, r := range o.VisibleFor {
			ops = append(ops, r.Resource+"."+r.Operation)
		}
		got[o.Key] = o.Type + " " + strings.Join(ops, ",") + " | " + o.Description
	}
	want := map[string]string{
		"mime_type":            "string file.download | the downloaded file's MIME type — operations: file.download",
		"binary_property":      "string file.download | the binary property holding the file — operations: file.download",
		"id":                   "any item.get | operations: item.get",
		"name":                 "any item.get | operations: item.get",
		responseOutputKey:      "any item.list | the API's JSON response (one item per array element when the operation lists) — operations: item.list",
		"pagination_truncated": "boolean item.list | true on every item when the run stopped at max_pages before the list ended — operations: item.list",
		"count":                "number item.count | how many — operations: item.count",
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("output %s:\n got %q\nwant %q", k, got[k], w)
		}
	}
	// The workflow schema loader carries them (MCP node_schema, validate).
	s, err := workflow.LoadDefaultSchema("app.outs")
	if err != nil || len(s.Outputs) != len(doc.Outputs) {
		t.Fatalf("LoadDefaultSchema outputs: %v %v", s, err)
	}
}

func TestOutputDeclValidation(t *testing.T) {
	bad := strings.Replace(outputsPkg, `{"key":"count","type":"number","description":"how many"}`,
		`{"key":"count","type":"number"},{"key":"count"}`, 1)
	if _, err := Parse([]byte(bad), "outs"); err == nil || !strings.Contains(err.Error(), `key "count" is listed twice`) {
		t.Errorf("duplicate key: %v", err)
	}
	bad = strings.Replace(outputsPkg, `"type":"number","description"`, `"type":"int","description"`, 1)
	if _, err := Parse([]byte(bad), "outs"); err == nil || !strings.Contains(err.Error(), "outputs") {
		t.Errorf("bad type: %v", err)
	}
	bad = strings.Replace(outputsPkg, `"title":"Outs",`, `"title":"Outs","outputs":[{"type":"any"}],`, 1)
	if _, err := Parse([]byte(bad), "outs"); err == nil || !strings.Contains(err.Error(), "key") {
		t.Errorf("missing key: %v", err)
	}
	ok := strings.Replace(outputsPkg, `"title":"Outs",`, `"title":"Outs","outputs":[{"key":"id","type":"string"}],`, 1)
	if _, err := Parse([]byte(ok), "outs"); err != nil {
		t.Errorf("package outputs: %v", err)
	}
}

// TestCatalogSchemasDeclareOutputs: every catalog op shows up in its node
// schema's outputs, and extended natives keep theirs first.
func TestCatalogSchemasDeclareOutputs(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	Reload()
	t.Cleanup(Reload)
	pkgs, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pkgs {
		if len(p.Operations) == 0 {
			continue // trigger-only
		}
		raw, ok := workflow.ReadEmbeddedSchema(p.TargetType())
		if !ok {
			t.Errorf("%s: no schema for %s", p.ID, p.TargetType())
			continue
		}
		var doc struct {
			Outputs []map[string]any `json:"outputs"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", p.ID, err)
		}
		covered := map[opRef]bool{}
		for _, o := range doc.Outputs {
			vf, _ := json.Marshal(o["visible_for"])
			var refs []opRef
			_ = json.Unmarshal(vf, &refs)
			for _, r := range refs {
				covered[r] = true
			}
		}
		for _, o := range p.Operations {
			if !covered[opRef{o.Resource, o.Operation}] && !opKeysTaken(p, &o, doc.Outputs) {
				t.Errorf("%s: %s has no outputs in the %s schema", p.ID, o.Key(), p.TargetType())
			}
		}
	}
}

// opKeysTaken: an extension's op whose every output key the native schema
// already declares (so none was appended for it).
func opKeysTaken(p *Package, o *Operation, outs []map[string]any) bool {
	if p.Extends == "" {
		return false
	}
	have := map[string]bool{}
	for _, m := range outs {
		k, _ := m["key"].(string)
		have[k] = true
	}
	for _, x := range p.opOutputs(o) {
		if !have[x.Key] {
			return false
		}
	}
	return true
}

// checkItemOutputs: when an op's outputs are a closed key set, the items
// the harness run produced carry no other key (so the validator's
// unknown_upstream_field check can trust them).
func checkItemOutputs(t *testing.T, where string, p *Package, op *Operation, items []workflow.Item) {
	t.Helper()
	keys := map[string]bool{"error": true}
	for _, o := range p.opOutputs(op) {
		if strings.HasPrefix(o.Key, "<") {
			return // open
		}
		keys[o.Key] = true
	}
	for i, it := range items {
		for k := range it.JSON {
			if !keys[k] {
				t.Errorf("%s: item %d has key %q, which the op's outputs (%s) don't list", where, i, k, outputKeys(p.opOutputs(op)))
			}
		}
	}
}

// TestValidateCatalogChain: a node reading API response fields of a
// catalog op (an app node, or a native node a package extends) gets no
// unknown_upstream_field warning.
func TestValidateCatalogChain(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	Reload()
	t.Cleanup(Reload)
	set := func(id string) workflow.WorkflowNode {
		return workflow.WorkflowNode{ID: id, Type: "core.set", Name: "Set " + id, Config: map[string]any{
			"assignments": []any{map[string]any{"field": "a", "value": "{{ $json.uri }}{{ $json.scheduling_url }}"}}}}
	}
	for _, up := range []workflow.WorkflowNode{
		{ID: "u", Type: "app.calendly", Name: "Calendly", Config: map[string]any{"resource": "eventType", "operation": "getAll"}},
		{ID: "u", Type: "comm.slack", Name: "Slack", Config: map[string]any{"resource": "message", "operation": "delete", "channelId": "C1", "timestamp": "1.2"}},
	} {
		w := &workflow.Workflow{Name: "chain", Nodes: []workflow.WorkflowNode{
			{ID: "t", Type: "trigger.manual", Name: "Start", Config: map[string]any{}}, up, set("s")},
			Connections: []workflow.WorkflowConnection{
				{ID: "c1", SourceNodeID: "t", TargetNodeID: "u", SourceHandle: "main", TargetHandle: "main"},
				{ID: "c2", SourceNodeID: "u", TargetNodeID: "s", SourceHandle: "main", TargetHandle: "main"},
			}}
		rep := workflow.ValidateWorkflow(w, workflow.ValidateOptions{})
		for _, is := range rep.Issues {
			if is.Code == workflow.IssueUnknownUpstreamField {
				t.Errorf("%s: false positive: %+v", up.Type, is)
			}
		}
	}
}
