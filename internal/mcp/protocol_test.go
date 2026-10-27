package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/workflow"
)

// A workflow the desktop editor saved to the file store alone has no node
// rows in SQLite: connecting two of its nodes must still work.
func TestWorkflowConnectOnAFileOnlyWorkflow(t *testing.T) {
	s := newTestServerAllowMutations(t, true)
	files, err := workflow.NewWorkflowFileStore(s.opts.WorkflowsDir)
	if err != nil {
		t.Fatal(err)
	}
	wf := &workflow.Workflow{ProfileID: "default", ID: "wf-file-only", Name: "editor", Nodes: []workflow.WorkflowNode{
		{ID: "a", Type: "core.set", Name: "A", Config: map[string]interface{}{}},
		{ID: "b", Type: "core.filter", Name: "B", Config: map[string]interface{}{}},
	}}
	if err := files.SaveWorkflow(context.Background(), wf); err != nil {
		t.Fatal(err)
	}
	callInto(t, s, "workflow_connect", map[string]interface{}{"workflow_id": wf.ID, "from": "a", "to": "b"}, nil)
	if got := getWorkflow(t, s, wf.ID); len(got.Connections) != 1 || len(got.Nodes) != 2 {
		t.Fatalf("after connect: %d nodes, %d connections", len(got.Nodes), len(got.Connections))
	}
}

func TestNodeRunRunsOneNode(t *testing.T) {
	s := newTestServerAllowMutations(t, true)
	var res struct {
		Outputs map[string][]struct {
			JSON map[string]interface{} `json:"json"`
		} `json:"outputs"`
	}
	callInto(t, s, "node_run", map[string]interface{}{
		"type":   "core.set",
		"config": map[string]interface{}{"assignments": []map[string]interface{}{{"field": "greeting", "value": "hi {{ $json.name }}"}}},
		"input":  map[string]interface{}{"name": "Ada"},
	}, &res)
	items := res.Outputs["main"]
	if len(items) != 1 || items[0].JSON["greeting"] != "hi Ada" || items[0].JSON["name"] != "Ada" {
		t.Fatalf("node_run outputs = %+v", res.Outputs)
	}
	wantErr(t, s, "node_run", map[string]interface{}{"type": "core.nope"}, `unknown node type "core.nope"`)
	wantErr(t, s, "node_run", map[string]interface{}{}, "type is required")
}

// fakeDoctor makes the doctor tool run a script that records its arguments
// and prints what the test gives, exiting with code.
func fakeDoctor(t *testing.T, stdout, stderr string, code int) (argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	script := filepath.Join(dir, "monoagentcli")
	body := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\nprintf '%s' '" + stdout + "'\nprintf '%s' '" + stderr + "' >&2\nexit " + string(rune('0'+code)) + "\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	prev := doctorExecutable
	doctorExecutable = func() (string, error) { return script, nil }
	t.Cleanup(func() { doctorExecutable = prev })
	return argsFile
}

func TestDoctorReturnsTheCommandsReport(t *testing.T) {
	report := `{"version":"1","results":[{"id":"core.db","status":"fail"}]}`
	argsFile := fakeDoctor(t, report, "", 1) // a failed required check exits 1 and still prints the report
	s := newTestServer(t)
	text := callInto(t, s, "doctor", map[string]interface{}{"group": []string{"core"}, "skip_group": []string{"runtimes"}, "check": []string{"core.db"}}, nil)
	var got map[string]interface{}
	if err := json.Unmarshal([]byte(text), &got); err != nil || got["version"] != "1" {
		t.Fatalf("doctor = %s (%v)", text, err)
	}
	raw, _ := os.ReadFile(argsFile)
	args := strings.Fields(string(raw))
	want := []string{"doctor", "--json", "--db-path", s.opts.DBPath, "--profile", "default", "--group", "core", "--skip-group", "runtimes", "--check", "core.db"}
	if strings.Join(args, " ") != strings.Join(want, " ") {
		t.Errorf("doctor ran with %v, want %v", args, want)
	}
	wantErr(t, s, "doctor", map[string]interface{}{"group": []string{"--fix"}}, "is not a name")
}

func TestDoctorFailureSaysWhy(t *testing.T) {
	fakeDoctor(t, "", "boom", 2)
	wantErr(t, newTestServer(t), "doctor", map[string]interface{}{}, "doctor failed: boom")
}

func TestWorkflowRunReportsProgressWhenAskedTo(t *testing.T) {
	s := newTestServerAllowMutations(t, true)
	var saved savedWorkflow
	callInto(t, s, "workflow_save", map[string]interface{}{"workflow": map[string]interface{}{
		"name": "progress", "is_active": true,
		"nodes": []map[string]interface{}{
			{"id": "t", "node_type": "trigger.manual", "name": "Trigger", "config": map[string]interface{}{}},
			{"id": "s", "node_type": "core.set", "name": "Set", "config": map[string]interface{}{"assignments": []map[string]interface{}{{"field": "x", "value": "1"}}}},
		},
		"connections": []map[string]interface{}{{"id": "c", "source_node_id": "t", "source_handle": "main", "target_node_id": "s", "target_handle": "main"}},
	}}, &saved)
	activateForTest(t, s, saved.ID)

	req := request(7, "tools/call", map[string]interface{}{
		"name": "workflow_run", "arguments": map[string]interface{}{"id": saved.ID, "timeout_seconds": 30, "profile": testProfile},
		"_meta": map[string]interface{}{"progressToken": "run-7"},
	})
	var progress []map[string]interface{}
	var final map[string]json.RawMessage
	for _, l := range serveLines(t, s, req) {
		if string(l["method"]) == `"notifications/progress"` {
			var p map[string]interface{}
			_ = json.Unmarshal(l["params"], &p)
			progress = append(progress, p)
		} else {
			final = l
		}
	}
	if final == nil {
		t.Fatal("no response")
	}
	if text, isErr := toolText(t, final); isErr || !strings.Contains(text, `"SUCCESS"`) {
		t.Fatalf("workflow_run = %s", text)
	}
	if len(progress) < 2 {
		t.Fatalf("progress notifications = %v, want a start and an end", progress)
	}
	last := -1.0
	for _, p := range progress {
		if p["progressToken"] != "run-7" {
			t.Errorf("progressToken = %v", p["progressToken"])
		}
		v, _ := p["progress"].(float64)
		if v <= last {
			t.Errorf("progress did not increase: %v", progress)
		}
		last = v
		if p["total"] != 2.0 {
			t.Errorf("total = %v, want the workflow's 2 nodes", p["total"])
		}
	}
	if msg, _ := progress[len(progress)-1]["message"].(string); !strings.Contains(msg, "success") {
		t.Errorf("last message = %q", msg)
	}

	// Without a progressToken: no notification at all.
	s2 := newTestServerAllowMutations(t, true)
	callInto(t, s2, "workflow_save", map[string]interface{}{"workflow": map[string]interface{}{"id": saved.ID, "name": "again", "is_active": true,
		"nodes": []map[string]interface{}{{"id": "t2", "node_type": "trigger.manual", "name": "Trigger", "config": map[string]interface{}{}}}}}, nil)
	for _, l := range serveLines(t, s2, callToolReq(8, "workflow_run", map[string]interface{}{"id": saved.ID, "timeout_seconds": 30})) {
		if len(l["method"]) > 0 {
			t.Fatalf("a call without progressToken got a notification: %s", l["method"])
		}
	}
}

func TestProgressReporterOnlyIncreases(t *testing.T) {
	var got []float64
	notify := notifyFunc(func(method string, params interface{}) {
		got = append(got, params.(map[string]interface{})["progress"].(float64))
	})
	meta := json.RawMessage(`{"progressToken":3}`)
	p := progressFrom(withProgress(withNotifier(context.Background(), notify), meta))
	for _, v := range []float64{0, 0, 1, 1, 0.5, 2} {
		p.report(v, 2, "")
	}
	if len(got) != 3 || got[0] != 0 || got[1] != 1 || got[2] != 2 {
		t.Errorf("reported %v, want [0 1 2]", got)
	}
	if progressFrom(withProgress(context.Background(), meta)) != nil {
		t.Error("a reporter without a notifier")
	}
	if progressFrom(withProgress(withNotifier(context.Background(), notify), json.RawMessage(`{}`))) != nil {
		t.Error("a reporter without a progressToken")
	}
	(*progressReporter)(nil).report(1, 1, "") // safe
}

// waitWithProgress gives up with the wait it wraps.
func TestWaitWithProgressStopsWithTheContext(t *testing.T) {
	s := newTestServer(t)
	rt, err := s.runtime()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	sent := 0
	notify := notifyFunc(func(string, interface{}) { sent++ })
	p := progressFrom(withProgress(withNotifier(ctx, notify), json.RawMessage(`{"progressToken":"w"}`)))
	if exec := waitWithProgress(ctx, rt, "no-such", "no-such-wf", time.Minute, false, p); exec.Status != "UNKNOWN" {
		t.Fatalf("exec = %+v", exec)
	}
	if sent == 0 {
		t.Error("no progress was sent")
	}
}

func TestStructuredContentAndOutputSchema(t *testing.T) {
	s := newTestServer(t)
	lines := serveLines(t, s,
		callToolReq(1, "workflow_validate", map[string]interface{}{"workflow": map[string]interface{}{"name": "x", "nodes": []interface{}{}}}),
		callToolReq(2, "workflow_list", map[string]interface{}{}),
		callToolReq(3, "workflow_status", map[string]interface{}{"execution_id": "nope"}),
	)
	var res struct {
		Content           []toolContent          `json:"content"`
		StructuredContent map[string]interface{} `json:"structuredContent"`
	}
	if err := json.Unmarshal(respByID(t, lines, "1")["result"], &res); err != nil {
		t.Fatal(err)
	}
	if _, ok := res.StructuredContent["valid"]; !ok {
		t.Fatalf("workflow_validate structuredContent = %v", res.StructuredContent)
	}
	var fromText map[string]interface{}
	if err := json.Unmarshal([]byte(res.Content[0].Text), &fromText); err != nil || fromText["valid"] != res.StructuredContent["valid"] {
		t.Errorf("the text and structuredContent differ: %s vs %v", res.Content[0].Text, res.StructuredContent)
	}
	// An array result (workflow_list) and an error carry none.
	for _, id := range []string{"2", "3"} {
		if raw := respByID(t, lines, id)["result"]; bytes.Contains(raw, []byte("structuredContent")) {
			t.Errorf("response %s has structuredContent: %s", id, raw)
		}
	}
	schemas := map[string]bool{}
	for _, d := range toolDefinitions(true) {
		if _, ok := d["outputSchema"]; ok {
			schemas[d["name"].(string)] = true
		}
	}
	for _, name := range []string{"workflow_run", "workflow_status", "workflow_validate"} {
		if !schemas[name] {
			t.Errorf("%s has no outputSchema", name)
		}
	}
}

func TestInitializeNegotiatesTheProtocolVersion(t *testing.T) {
	for asked, want := range map[string]string{"2024-11-05": "2024-11-05", "2025-03-26": "2025-03-26", "2025-06-18": "2025-06-18", "1999-01-01": protocolVersion, "": protocolVersion} {
		if got := negotiateProtocolVersion(json.RawMessage(`{"protocolVersion":"` + asked + `"}`)); got != want {
			t.Errorf("asked %q: %q, want %q", asked, got, want)
		}
	}
}

func initializeCapabilities(t *testing.T, s *Server) map[string]interface{} {
	t.Helper()
	var res struct {
		Capabilities map[string]interface{} `json:"capabilities"`
	}
	if err := json.Unmarshal(serveLines(t, s, request(1, "initialize", map[string]interface{}{}))[0]["result"], &res); err != nil {
		t.Fatal(err)
	}
	return res.Capabilities
}

func TestResourcesServeRefTopicsAndNodeSchemas(t *testing.T) {
	s := newTestServer(t)
	if _, ok := initializeCapabilities(t, s)["resources"]; !ok {
		t.Fatal("initialize does not advertise resources")
	}
	lines := serveLines(t, s,
		request(1, "resources/list", nil),
		request(2, "resources/read", map[string]interface{}{"uri": "monoagent://ref/workflow"}),
		request(3, "resources/read", map[string]interface{}{"uri": "monoagent://node/core.set"}),
		request(4, "resources/read", map[string]interface{}{"uri": "monoagent://node/no.such"}),
		request(5, "resources/templates/list", nil),
		request(6, "resources/read", map[string]interface{}{"uri": "monoagent://ref/api-tools"}),
	)
	var list struct {
		Resources []struct {
			URI string `json:"uri"`
		} `json:"resources"`
	}
	_ = json.Unmarshal(respByID(t, lines, "1")["result"], &list)
	uris := map[string]bool{}
	for _, r := range list.Resources {
		uris[r.URI] = true
	}
	for _, want := range []string{"monoagent://ref/workflow", "monoagent://ref/api-tools", "monoagent://node/core.set", "monoagent://node/trigger.manual"} {
		if !uris[want] {
			t.Errorf("resources/list lacks %s", want)
		}
	}
	read := func(id string) string {
		var r struct {
			Contents []struct {
				Text string `json:"text"`
			} `json:"contents"`
		}
		_ = json.Unmarshal(respByID(t, lines, id)["result"], &r)
		if len(r.Contents) != 1 {
			t.Fatalf("read %s: %s", id, respByID(t, lines, id)["result"])
		}
		return r.Contents[0].Text
	}
	if read("2") != docsTopics["workflow"] {
		t.Error("the ref resource is not the docs topic")
	}
	if !strings.Contains(read("3"), `"assignments"`) {
		t.Error("the node resource is not the node's schema")
	}
	if !strings.Contains(read("6"), "api_config_set") {
		t.Error("the api-tools resource lacks the api tools")
	}
	if e := respByID(t, lines, "4")["error"]; !bytes.Contains(e, []byte(`-32002`)) {
		t.Errorf("an unknown resource: %s", e)
	}
	if !bytes.Contains(respByID(t, lines, "5")["result"], []byte("monoagent://node/{type}")) {
		t.Error("resources/templates/list lacks the node template")
	}
}

// Grant mode and --tasks-only gain no resources; --api-only gets the one
// topic about its tools.
func TestResourcesFollowTheNarrowModes(t *testing.T) {
	tasks := newTestServer(t)
	tasks.opts.TasksOnly = true
	grant := newTestServer(t)
	grant.opts.Grant = "g-1"
	for name, s := range map[string]*Server{"tasks-only": tasks, "grant": grant} {
		if !strings.Contains(string(serveLines(t, s, request(1, "resources/list", nil))[0]["error"]), "method not found") {
			t.Errorf("%s serves resources/list", name)
		}
		if _, ok := initializeCapabilities(t, s)["resources"]; ok {
			t.Errorf("%s advertises resources", name)
		}
	}
	api := newTestServer(t)
	api.opts.APIOnly = true
	lines := serveLines(t, api,
		request(1, "resources/list", nil),
		request(2, "resources/read", map[string]interface{}{"uri": "monoagent://ref/workflow"}),
		request(3, "resources/read", map[string]interface{}{"uri": "monoagent://node/core.set"}),
	)
	if raw := string(respByID(t, lines, "1")["result"]); !strings.Contains(raw, "monoagent://ref/api-tools") || strings.Contains(raw, "monoagent://ref/workflow") || strings.Contains(raw, "monoagent://node/") {
		t.Errorf("--api-only resources: %s", raw)
	}
	for _, id := range []string{"2", "3"} {
		if respByID(t, lines, id)["error"] == nil {
			t.Errorf("--api-only read %s", id)
		}
	}
}

// Every api_* tool's description is short, and what was cut is in the
// api-tools topic.
func TestAPIToolDescriptionsAreShortAndTheRestIsInTheDocs(t *testing.T) {
	for _, tl := range allTools() {
		if !strings.HasPrefix(tl.name, "api_") {
			continue
		}
		if len(tl.description) > 520 {
			t.Errorf("%s: description of %d bytes", tl.name, len(tl.description))
		}
		if n := strings.Count(strings.TrimSuffix(tl.description, apiToolsDetails), ". "); n > 3 {
			t.Errorf("%s: %d sentences: %s", tl.name, n+1, tl.description)
		}
		if d, ok := apiToolDetails[tl.name]; ok && !strings.Contains(docsTopics[apiToolsTopic], d) {
			t.Errorf("%s: the api-tools topic lacks its rules", tl.name)
		}
	}
	for name := range apiToolDetails {
		if !apiToolNames()[name] {
			t.Errorf("api-tools documents %q, which is not an api tool", name)
		}
	}
}
