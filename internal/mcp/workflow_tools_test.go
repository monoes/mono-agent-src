package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// newWorkflowToolsServer is newTestServer with WorkflowTools and AllowMutations set.
func newWorkflowToolsServer(t *testing.T, selection string, allowMutations bool) *Server {
	t.Helper()
	t.Setenv("MONOAGENT_MCP_WORKFLOW_TOOLS", "")
	dbPath := filepath.Join(t.TempDir(), "mcp-test.db")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("closing seed db: %v", err)
	}
	s := NewServer(Options{
		DBPath:         dbPath,
		Profile:        "default",
		WorkflowsDir:   filepath.Join(t.TempDir(), "workflows"),
		Version:        "test",
		AllowMutations: allowMutations,
		WorkflowTools:  selection,
	})
	t.Cleanup(func() { s.rt.Close() })
	return s
}

// saveWorkflow stores a manual-trigger workflow whose set node reads the given
// templates, and returns it.
func saveWorkflow(t *testing.T, s *Server, id, name string, fields map[string]interface{}) *workflow.Workflow {
	t.Helper()
	rt, err := s.runtime()
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	wf := &workflow.Workflow{
		ProfileID:   "default",
		ID:          id,
		Name:        name,
		Description: "Says hello.",
		IsActive:    true,
		Nodes: []workflow.WorkflowNode{
			{ID: "t", Type: "trigger.manual", Name: "Trigger"},
			{ID: "s", Type: "core.set", Name: "Set", Config: map[string]interface{}{"fields": fields}},
		},
		Connections: []workflow.WorkflowConnection{{
			SourceNodeID: "t", SourceHandle: "main",
			TargetNodeID: "s", TargetHandle: "main",
		}},
	}
	if err := rt.store.CreateWorkflow(workflow.WithApproval(context.Background()), wf); err != nil {
		t.Fatalf("save workflow: %v", err)
	}
	return wf
}

type listedTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

// handle answers one request without Serve, which closes the runtime (and the
// database the tests save workflows to) when its input ends.
func handle(t *testing.T, s *Server, line string) map[string]json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(s.handleLine(context.Background(), []byte(line)))
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	return m
}

func listTools(t *testing.T, s *Server) map[string]listedTool {
	t.Helper()
	resp := handle(t, s, request(1, "tools/list", map[string]interface{}{}))
	var r struct {
		Tools []listedTool `json:"tools"`
	}
	if err := json.Unmarshal(resp["result"], &r); err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	out := map[string]listedTool{}
	for _, tl := range r.Tools {
		out[tl.Name] = tl
	}
	return out
}

func wfToolNames(tools map[string]listedTool) []string {
	var names []string
	for n := range tools {
		if strings.HasPrefix(n, workflowToolPrefix) {
			names = append(names, n)
		}
	}
	return names
}

func TestWorkflowSlug(t *testing.T) {
	for in, want := range map[string]string{
		"Daily Digest":          "daily_digest",
		"  --Hello, World!!-- ": "hello_world",
		"Ünïcode name 2":        "n_code_name_2",
		"":                      "workflow",
		"!!!":                   "workflow",
		"already_snake_case":    "already_snake_case",
	} {
		if got := workflowSlug(in); got != want {
			t.Errorf("workflowSlug(%q) = %q, want %q", in, got, want)
		}
	}
	long := workflowToolName(workflowSlug(strings.Repeat("abc ", 40)), "")
	if len(long) > maxToolNameLen || !strings.HasPrefix(long, "wf_abc_abc") || strings.HasSuffix(long, "_") {
		t.Errorf("long name %q (%d chars)", long, len(long))
	}
	if n := workflowToolName(strings.Repeat("x", 100), "abc123"); len(n) != maxToolNameLen || !strings.HasSuffix(n, "_abc123") {
		t.Errorf("long name with suffix %q (%d chars)", n, len(n))
	}
}

func TestWorkflowToolNamesDedupe(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	wfs := []workflow.Workflow{
		{ID: "B-222222-x", Name: "Report!", CreatedAt: t0.Add(time.Hour)},
		{ID: "a1111111", Name: "report", CreatedAt: t0},
		{ID: "B-222222-y", Name: "REPORT", CreatedAt: t0.Add(2 * time.Hour)},
		{ID: "c3", Name: strings.Repeat("long ", 30), CreatedAt: t0},
		{ID: "d4", Name: strings.Repeat("long ", 30), CreatedAt: t0.Add(time.Hour)},
	}
	names := workflowToolNames(wfs)
	want := map[string]string{
		"a1111111":   "wf_report", // oldest keeps the plain slug
		"B-222222-x": "wf_report_b22222",
		"B-222222-y": "wf_report_b222222y", // the six-character suffix collides: a longer one
	}
	for id, w := range want {
		if names[id] != w {
			t.Errorf("%s: %q, want %q", id, names[id], w)
		}
	}
	if names["c3"] == names["d4"] || len(names["d4"]) > maxToolNameLen || !strings.HasSuffix(names["d4"], "_d4") {
		t.Errorf("long names %q and %q", names["c3"], names["d4"])
	}
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			t.Errorf("duplicate tool name %q in %v", n, names)
		}
		seen[n] = true
	}
}

func TestWorkflowInputSchemaFromTriggerInputs(t *testing.T) {
	schema := workflowInputSchema([]workflow.TriggerField{
		{Name: "topic", Examples: []string{"go"}, Nodes: []string{"Set"}},
		{Name: "prompts", Structured: true, Examples: []string{"a", "b"}},
	})
	props := schema["properties"].(map[string]interface{})
	topic := props["topic"].(map[string]interface{})
	if topic["type"] != "string" || !strings.Contains(topic["description"].(string), "Set") {
		t.Errorf("topic: %v", topic)
	}
	if ex, _ := topic["examples"].([]interface{}); len(ex) != 1 || ex[0] != "go" {
		t.Errorf("topic examples: %v", topic["examples"])
	}
	prompts := props["prompts"].(map[string]interface{})
	if prompts["type"] != "array" {
		t.Errorf("prompts: %v", prompts)
	}
	if ex, _ := prompts["examples"].([]interface{}); len(ex) != 1 || len(ex[0].([]interface{})) != 2 {
		t.Errorf("prompts examples: %v", prompts["examples"])
	}
	if _, ok := props[workflowToolTimeoutArg]; !ok || schema["type"] != "object" || schema["required"] != nil {
		t.Errorf("schema: %v", schema)
	}
}

// Without the flag no wf_* tool is listed or callable, as today.
func TestWorkflowToolsAbsentWithoutTheFlag(t *testing.T) {
	s := newWorkflowToolsServer(t, "", true)
	saveWorkflow(t, s, "wf-1", "Hello", map[string]interface{}{"greeting": "hi {{ $json.name }}"})
	if names := wfToolNames(listTools(t, s)); len(names) != 0 {
		t.Errorf("wf tools listed without the flag: %v", names)
	}
	if text, isErr := toolText(t, handle(t, s, callToolReq(2, "wf_hello", map[string]interface{}{}))); !isErr || !strings.Contains(text, "unknown tool") {
		t.Errorf("call without the flag: %v %s", isErr, text)
	}
}

// The tools are mutating: hidden and refused without --allow-mutations.
func TestWorkflowToolsNeedMutations(t *testing.T) {
	s := newWorkflowToolsServer(t, "all", false)
	saveWorkflow(t, s, "wf-1", "Hello", map[string]interface{}{"greeting": "hi {{ $json.name }}"})
	if names := wfToolNames(listTools(t, s)); len(names) != 0 {
		t.Errorf("wf tools listed without --allow-mutations: %v", names)
	}
	if text, isErr := toolText(t, handle(t, s, callToolReq(2, "wf_hello", map[string]interface{}{}))); !isErr || !strings.Contains(text, "--allow-mutations") {
		t.Errorf("call without --allow-mutations: %v %s", isErr, text)
	}
}

func TestWorkflowToolsListedWithSchemaAndCallRuns(t *testing.T) {
	s := newWorkflowToolsServer(t, "all", true)
	saveWorkflow(t, s, "wf-1", "Say Hello", map[string]interface{}{"greeting": "hi {{ $json.name }}"})

	tools := listTools(t, s)
	tl, ok := tools["wf_say_hello"]
	if !ok {
		t.Fatalf("wf_say_hello not listed: %v", wfToolNames(tools))
	}
	if !strings.Contains(tl.Description, "Say Hello") || !strings.Contains(tl.Description, "Says hello") || !strings.Contains(tl.Description, "execution_id") {
		t.Errorf("description: %s", tl.Description)
	}
	props, _ := tl.InputSchema["properties"].(map[string]interface{})
	if _, ok := props["name"]; !ok {
		t.Errorf("schema lacks the trigger field name: %v", tl.InputSchema)
	}

	// A workflow saved after the server started is listed at once.
	saveWorkflow(t, s, "wf-2", "Second", map[string]interface{}{"x": "{{ $json.y }}"})
	if _, ok := listTools(t, s)["wf_second"]; !ok {
		t.Errorf("workflow saved later is not listed")
	}

	text, isErr := toolText(t, handle(t, s, callToolReq(3, "wf_say_hello", map[string]interface{}{"name": "Ada", "_timeout_seconds": 30})))
	if isErr {
		t.Fatalf("call: %s", text)
	}
	var res struct {
		ExecutionID string `json:"execution_id"`
		Status      string `json:"status"`
		Nodes       []struct {
			NodeID      string            `json:"node_id"`
			OutputItems []json.RawMessage `json:"output_items"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(text), &res); err != nil {
		t.Fatalf("result: %v\n%s", err, text)
	}
	if res.ExecutionID == "" || res.Status != "SUCCESS" || !strings.Contains(text, "hi Ada") {
		t.Errorf("run result: %s", text)
	}
	if strings.Contains(text, workflowToolTimeoutArg) {
		t.Errorf("_timeout_seconds reached the workflow: %s", text)
	}
}

// A list serves only the workflows it names, by ID or by name.
func TestWorkflowToolsSelection(t *testing.T) {
	s := newWorkflowToolsServer(t, "wf-1, third", true)
	saveWorkflow(t, s, "wf-1", "First", map[string]interface{}{})
	saveWorkflow(t, s, "wf-2", "Second", map[string]interface{}{})
	saveWorkflow(t, s, "wf-3", "Third", map[string]interface{}{})
	tools := listTools(t, s)
	_, first := tools["wf_first"]
	_, second := tools["wf_second"]
	_, third := tools["wf_third"]
	if !first || second || !third {
		t.Errorf("listed %v", wfToolNames(tools))
	}
}

// The narrow modes serve one family: asked for workflow tools too, by the
// environment, the server refuses at start.
func TestWorkflowToolsRefusedWithNarrowModes(t *testing.T) {
	for _, env := range []string{"MONOAGENT_MCP_API_ONLY", "MONOAGENT_MCP_TASKS_ONLY"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv("MONOAGENT_MCP_API_ONLY", "")
			t.Setenv("MONOAGENT_MCP_TASKS_ONLY", "")
			t.Setenv(env, "1")
			t.Setenv("MONOAGENT_MCP_WORKFLOW_TOOLS", "1")
			s := NewServer(Options{DBPath: filepath.Join(t.TempDir(), "x.db")})
			err := s.Serve(context.Background(), strings.NewReader(""), &strings.Builder{})
			if !errors.Is(err, ErrWorkflowToolsWithNarrowMode) {
				t.Errorf("Serve: %v", err)
			}
		})
	}
}

// An inactive workflow cannot run, so it is not served (its tool would fail
// on every call with "workflow is not active").
func TestWorkflowToolsSkipInactive(t *testing.T) {
	s := newWorkflowToolsServer(t, "all", true)
	saveWorkflow(t, s, "wf-on", "On", nil)
	off := saveWorkflow(t, s, "wf-off", "Off", nil)
	rt, err := s.runtime()
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.store.SetWorkflowActive(context.Background(), off.ID, false); err != nil {
		t.Fatal(err)
	}
	tools := listTools(t, s)
	if _, ok := tools["wf_on"]; !ok {
		t.Errorf("active workflow not served: %v", wfToolNames(tools))
	}
	if _, ok := tools["wf_off"]; ok {
		t.Errorf("inactive workflow served: %v", wfToolNames(tools))
	}
}

func TestWorkflowToolSuffixSkipsTheWfPrefix(t *testing.T) {
	if got := idSuffix("wf-3845abcd", 6); got != "3845ab" {
		t.Errorf("idSuffix = %q, want 3845ab", got)
	}
}

// workflow_status names the URL a run paused for a reply resumes through.
func TestWorkflowStatusShowsResumeURL(t *testing.T) {
	t.Setenv("MONOAGENT_PUBLIC_URL", "http://127.0.0.1:19721")
	s := newTestServer(t)
	rt, err := s.runtime()
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	ctx := context.Background()
	wf := &workflow.Workflow{ID: "wf-wait", Name: "Wait", ProfileID: "default"}
	if err := rt.store.CreateWorkflow(ctx, wf); err != nil {
		t.Fatal(err)
	}
	exec := &workflow.WorkflowExecution{ID: "exec-wait", WorkflowID: wf.ID, ProfileID: "default", Status: "WAITING", TriggerType: "manual"}
	if err := rt.store.CreateExecution(ctx, exec); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.db.DB.Exec(`INSERT INTO wait_replies (execution_id, node_id, run_index, workflow_id, token, kind, spec)
		VALUES ('exec-wait','wait',0,'wf-wait','tokWait','webhook','{"kind":"webhook","outbound":false}')`); err != nil {
		t.Fatal(err)
	}

	resps := serveLines(t, s, callToolReq(1, "workflow_status", map[string]interface{}{"execution_id": exec.ID}))
	text, isErr := toolText(t, respByID(t, resps, "1"))
	if isErr {
		t.Fatalf("workflow_status: %s", text)
	}
	var got struct {
		Status  string                  `json:"status"`
		Waiting []workflow.PendingReply `json:"waiting_for"`
	}
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	if got.Status != "WAITING" || len(got.Waiting) != 1 || got.Waiting[0].URL != "http://127.0.0.1:19721/reply/tokWait" {
		t.Fatalf("workflow_status = %s", text)
	}
}
