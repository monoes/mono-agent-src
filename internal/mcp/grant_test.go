package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/daemonhb"
	"github.com/monoes/mono-agent/internal/orgbridge"
	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/orggrant"
	"github.com/monoes/mono-agent/internal/profiledir"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

type grantFixture struct {
	server *Server
	db     *storage.Database
	grant  *orggrant.Grant
	store  *orggrant.Store
}

func newGrantFixture(t *testing.T, tool orggrant.Tool) *grantFixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MONOAGENT_DAEMON_HEARTBEAT", filepath.Join(home, "hb.json"))
	// The env monomind gives a role's tool provider; grant mode refuses to
	// serve without it.
	t.Setenv("MONOMIND_ORG_NAME", "growth")
	t.Setenv("MONOMIND_ORG_ROLE", "writer")
	t.Setenv("MONOMIND_ORG_RUN", "")
	dbPath := filepath.Join(t.TempDir(), "grant.db")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	wfStore := workflow.NewSQLiteWorkflowStore(db.DB)
	if err := wfStore.CreateWorkflow(ctx, &workflow.Workflow{ID: "wf-pub", Name: "Publish", ProfileID: "default"}); err != nil {
		t.Fatal(err)
	}
	store := orggrant.NewStore(db.DB)
	tool.Alias, tool.WorkflowID = "publish", "wf-pub"
	g, err := store.UpsertGrant(ctx, orggrant.GrantInput{ProfileID: "default", OrgName: "growth", RoleID: "writer", Tool: tool})
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(Options{DBPath: dbPath, Profile: "default", WorkflowsDir: filepath.Join(t.TempDir(), "wf"), Grant: g.ID})
	t.Cleanup(func() { s.closeRuntime() })
	oldPoll := grantPollInterval
	grantPollInterval = 20 * time.Millisecond
	t.Cleanup(func() { grantPollInterval = oldPoll })
	return &grantFixture{server: s, db: db, grant: g, store: store}
}

func liveHeartbeat(t *testing.T) {
	t.Helper()
	if err := daemonhb.Write(daemonhb.Heartbeat{PID: os.Getpid(), TS: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

// fakeDaemon completes the first QUEUED execution with one output item. It
// looks for the execution until the test ends.
func fakeDaemon(t *testing.T, db *storage.Database, output map[string]interface{}) {
	testCtx := t.Context()
	go func() {
		for testCtx.Err() == nil {
			var id string
			if err := db.DB.QueryRow(`SELECT id FROM workflow_executions WHERE status = 'QUEUED' LIMIT 1`).Scan(&id); err == nil {
				now := time.Now().UTC()
				ws := workflow.NewSQLiteWorkflowStore(db.DB)
				_ = ws.CreateExecutionNode(context.Background(), &workflow.WorkflowExecutionNode{
					ExecutionID: id, NodeID: "n1", NodeName: "post", Status: "SUCCESS",
					OutputItems: []workflow.Item{{JSON: output}}, StartedAt: &now, FinishedAt: &now})
				_, _ = db.DB.Exec(`UPDATE workflow_executions SET status = 'SUCCESS' WHERE id = ?`, id)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
}

func TestGrantModeListsOnlyGrantedTools(t *testing.T) {
	f := newGrantFixture(t, orggrant.Tool{Wait: true})
	resps := serveLines(t, f.server, request(1, "tools/list", map[string]interface{}{}))
	var res struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(resps[0]["result"], &res); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range res.Tools {
		names = append(names, tl.Name)
	}
	if strings.Join(names, ",") != "automation_output,automation_publish,automation_status" {
		t.Fatalf("grant-mode tools = %v", names)
	}
}

func TestGrantModeRunWaitsTracesAndRedacts(t *testing.T) {
	f := newGrantFixture(t, orggrant.Tool{Wait: true, Timeout: 5})
	ctx := context.Background()

	// No daemon: refuse fast.
	start := time.Now()
	if _, err := f.server.callGrantTool(ctx, "automation_publish", json.RawMessage(`{"text":"hi"}`)); err == nil || !strings.HasPrefix(err.Error(), codeDaemonRequired) {
		t.Fatalf("without daemon: %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("daemon_required took over a second")
	}

	liveHeartbeat(t)
	fakeDaemon(t, f.db, map[string]interface{}{"url": "https://example.test/1", "password": "hunter2"})
	meta := json.RawMessage(`{"trace":{"org":"growth","run":"run-7","role":"writer","chain_id":"chn_role1","hop":2}}`)
	out, err := f.server.callGrantTool(withMeta(ctx, meta), "automation_publish", json.RawMessage(`{"text":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"status": "success"`) || !strings.Contains(out, "example.test/1") || strings.Contains(out, "hunter2") {
		t.Fatalf("run output = %s", out)
	}
	var trigger, data string
	if err := f.db.DB.QueryRow(`SELECT trigger_type, trigger_data FROM workflow_executions`).Scan(&trigger, &data); err != nil {
		t.Fatal(err)
	}
	if trigger != workflow.TriggerTypeOrgTool || !strings.Contains(data, `"text":"hi"`) || !strings.Contains(data, `"run":"run-7"`) {
		t.Fatalf("execution = %s %s", trigger, data)
	}
	var chain, grantID, run string
	var hop int
	if err := f.db.DB.QueryRow(`SELECT chain_id, hop, grant_id, run_id FROM org_bridge_calls WHERE direction = 'role_tool'`).Scan(&chain, &hop, &grantID, &run); err != nil {
		t.Fatal(err)
	}
	if chain != "chn_role1" || hop != 3 || grantID != f.grant.ID || run != "run-7" {
		t.Fatalf("ledger row = %s %d %s %s", chain, hop, grantID, run)
	}

	// The role cannot read runs its grants did not start.
	if _, err := f.server.callGrantTool(ctx, "automation_status", json.RawMessage(`{"execution_id":"someone-else"}`)); err == nil {
		t.Fatal("read another execution")
	}

	// Revocation takes effect on the next call (C-29).
	if err := f.store.RevokeGrant(ctx, "default", "growth", "writer", "publish"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.callGrantTool(ctx, "automation_publish", nil); err == nil || !strings.HasPrefix(err.Error(), codeRefusedGrant) {
		t.Fatalf("after revoke: %v", err)
	}
}

func TestGrantModeScopeAndCaps(t *testing.T) {
	f := newGrantFixture(t, orggrant.Tool{Wait: false, MaxCallsPerRun: 1})
	ctx := context.Background()
	liveHeartbeat(t)

	t.Setenv("MONOMIND_ORG_ROLE", "lead")
	if _, err := f.server.callGrantTool(ctx, "automation_publish", nil); err == nil || !strings.Contains(err.Error(), `is for role "writer"`) {
		t.Fatalf("grant used by another role: %v", err)
	}
	t.Setenv("MONOMIND_ORG_ROLE", "writer")
	t.Setenv("MONOMIND_ORG_RUN", "run-1")
	out, err := f.server.callGrantTool(ctx, "automation_publish", nil)
	if err != nil || !strings.Contains(out, `"status": "queued"`) {
		t.Fatalf("first call: %s %v", out, err)
	}
	if _, err := f.server.callGrantTool(ctx, "automation_publish", nil); err == nil || !strings.HasPrefix(err.Error(), codeRefusedCap) {
		t.Fatalf("second call in the run: %v", err)
	}
	if _, err := f.server.callGrantTool(ctx, "workflow_run", nil); err == nil || !strings.HasPrefix(err.Error(), codeRefusedGrant) {
		t.Fatalf("ungranted tool: %v", err)
	}
}

func TestGrantModeReturnsHILImmediately(t *testing.T) {
	f := newGrantFixture(t, orggrant.Tool{Wait: true, Timeout: 30})
	ctx := context.Background()
	liveHeartbeat(t)
	go func() {
		for i := 0; i < 500; i++ {
			var id string
			if f.db.DB.QueryRow(`SELECT id FROM workflow_executions WHERE status = 'QUEUED' LIMIT 1`).Scan(&id) == nil {
				_, _ = f.db.DB.Exec(`UPDATE workflow_executions SET status = 'WAITING' WHERE id = ?`, id)
				_, _ = f.db.DB.Exec(`INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, status, profile_id) VALUES ('hil-1', ?, 'wf-pub', 'n', 'Review post', 'pending', 'default')`, id)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	start := time.Now()
	out, err := f.server.callGrantTool(ctx, "automation_publish", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"hil"`) || !strings.Contains(out, "hil-1") || time.Since(start) > 10*time.Second {
		t.Fatalf("HIL result = %s", out)
	}
}

// Grant ids are not secrets — they sit in every role's provider args in
// the org JSON — so grant mode must fail closed when the environment that
// names the role is missing: an unset variable used to skip the check
// entirely, so a role that still holds Bash could serve another role's
// grant by clearing it (C-3).
func TestGrantModeRefusesWithoutOrgEnvironment(t *testing.T) {
	f := newGrantFixture(t, orggrant.Tool{})
	ctx := context.Background()
	liveHeartbeat(t)

	t.Setenv("MONOMIND_ORG_NAME", "")
	if defs := f.server.grantToolDefinitions(ctx); len(defs) != 0 {
		t.Fatalf("tools listed with no org in the environment: %v", defs)
	}
	if _, err := f.server.callGrantTool(ctx, "automation_publish", nil); err == nil || !strings.Contains(err.Error(), "MONOMIND_ORG_NAME") {
		t.Fatalf("served a grant with no org in the environment: %v", err)
	}
	t.Setenv("MONOMIND_ORG_NAME", "growth")
	t.Setenv("MONOMIND_ORG_ROLE", "")
	if _, err := f.server.callGrantTool(ctx, "automation_publish", nil); err == nil || !strings.Contains(err.Error(), "MONOMIND_ORG_ROLE") {
		t.Fatalf("served a grant with no role in the environment: %v", err)
	}
}

// The per-run and per-day caps are the only quantity bound on a granted
// automation, so a ledger read that fails must refuse rather than skip
// them — "database is locked" under concurrent daemon writes used to lift
// the cap for that call.
func TestGrantModeRefusesWhenTheCapCannotBeRead(t *testing.T) {
	f := newGrantFixture(t, orggrant.Tool{MaxCallsPerRun: 5})
	ctx := context.Background()
	liveHeartbeat(t)
	// Make every ledger read fail, as a locked database does.
	if _, err := f.db.DB.Exec(`ALTER TABLE org_bridge_calls RENAME TO org_bridge_calls_unreadable`); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONOMIND_ORG_RUN", "run-1")
	if _, err := f.server.callGrantTool(ctx, "automation_publish", nil); err == nil || !strings.HasPrefix(err.Error(), codeRefusedCap) {
		t.Fatalf("ran with an unreadable per-run cap: %v", err)
	}
	// With no run id only the daily cap stands between the role and the
	// automation.
	t.Setenv("MONOMIND_ORG_RUN", "")
	if _, err := f.server.callGrantTool(ctx, "automation_publish", nil); err == nil || !strings.HasPrefix(err.Error(), codeRefusedCap) {
		t.Fatalf("ran with an unreadable daily cap: %v", err)
	}
	var execs int
	_ = f.db.DB.QueryRow(`SELECT COUNT(*) FROM workflow_executions`).Scan(&execs)
	if execs != 0 {
		t.Fatalf("%d executions started behind an unreadable cap", execs)
	}
}

// run_config lives in the org JSON, which any role whose fileWrite reaches
// `.monomind/` can edit — the reason grants, endpoints and autonomy moved
// to the database — so what it may ask for is clamped: a role must not be
// able to raise U10's loop control out of the way.
func TestOrgLimitsAreClampedToACeiling(t *testing.T) {
	f := newGrantFixture(t, orggrant.Tool{})
	ctx := context.Background()
	root := profiledir.Root(f.db.DB, "default")
	doc := &orgdesign.Doc{Name: "growth", Status: "stopped", Schedule: json.RawMessage("null"),
		RunConfig: map[string]json.RawMessage{"max_hops": json.RawMessage("999999"), "max_repeats": json.RawMessage("1000000")},
		Roles:     []orgdesign.Role{{ID: "writer", Title: "Writer", Type: "boss", Responsibilities: []string{"Write."}}}}
	if _, err := orgdesign.Save(root, doc); err != nil {
		t.Fatal(err)
	}
	b, _, err := f.server.grantScope(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if lim := orgLimitsFor(f.db.DB, b); lim.MaxHops != orgbridge.MaxHopsCeiling || lim.MaxRepeats != orgbridge.MaxRepeatsCeiling {
		t.Fatalf("limits the org file asked for = %+v", lim)
	}
}

// C-46 live gate: a grant with no input_schema advertised a schema without
// properties, and monomind — which keeps only listed arguments — delivered
// every call as input: {}. The tool now lists the input fields the
// workflow's templates read.
func TestGrantModeAdvertisesTheWorkflowsInputFields(t *testing.T) {
	f := newGrantFixture(t, orggrant.Tool{Wait: true})
	ws := workflow.NewSQLiteWorkflowStore(f.db.DB)
	if err := ws.SaveWorkflowNodes(context.Background(), "wf-pub", []workflow.WorkflowNode{
		{ID: "t", Type: "trigger.manual", Name: "Start", Config: map[string]interface{}{}},
		{ID: "w", Type: "data.write_binary_file", Name: "Write", Config: map[string]interface{}{
			"file_path": "{{ $json.input.path }}", "field": "content"}},
		{ID: "s", Type: "core.set", Name: "Content", Config: map[string]interface{}{
			"assignments": `[{"field":"content","value":"{{ $json.input.text }}"}]`}},
	}); err != nil {
		t.Fatal(err)
	}
	resps := serveLines(t, f.server, request(1, "tools/list", map[string]interface{}{}))
	var res struct {
		Tools []struct {
			Name        string                 `json:"name"`
			InputSchema map[string]interface{} `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(resps[0]["result"], &res); err != nil {
		t.Fatal(err)
	}
	for _, tl := range res.Tools {
		if tl.Name != "automation_publish" {
			continue
		}
		props, _ := tl.InputSchema["properties"].(map[string]interface{})
		if props[profileArg] == nil {
			t.Fatalf("a grant tool must accept the optional profile argument: %v", props)
		}
		delete(props, profileArg) // the grant's own, not a workflow input field
		if props["path"] == nil || props["text"] == nil || len(props) != 2 {
			t.Fatalf("automation_publish properties = %v, want path and text", props)
		}
		if tl.InputSchema["additionalProperties"] != true {
			t.Fatalf("schema must still accept other fields: %v", tl.InputSchema)
		}
		return
	}
	t.Fatal("automation_publish not listed")
}

// #241: the monomind-growth org's granted workflows read {{ $json.keywords }}
// and {{ json $json.targets }}, as a manual --input run delivers them. The
// tool advertised no such arguments and the run got them only under input,
// so x.find_by_keyword searched for "<no value>".
func TestGrantRunExposesArgumentsAtTheTopLevel(t *testing.T) {
	f := newGrantFixture(t, orggrant.Tool{Wait: false})
	ws := workflow.NewSQLiteWorkflowStore(f.db.DB)
	if err := ws.SaveWorkflowNodes(context.Background(), "wf-pub", []workflow.WorkflowNode{
		{ID: "t", Type: "trigger.manual", Name: "Start", Config: map[string]interface{}{}},
		{ID: "s", Type: "x.find_by_keyword", Name: "Search", Config: map[string]interface{}{
			"keywords": "{{ $json.keywords }}", "note": "{{ $json.org.workdir }}"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := ws.SaveWorkflowConnections(context.Background(), "wf-pub", []workflow.WorkflowConnection{
		{SourceNodeID: "t", TargetNodeID: "s"},
	}); err != nil {
		t.Fatal(err)
	}

	var props map[string]interface{}
	for _, tl := range f.server.grantToolDefinitions(context.Background()) {
		if tl["name"] == "automation_publish" {
			schema, _ := tl["inputSchema"].(map[string]interface{})
			props, _ = schema["properties"].(map[string]interface{})
		}
	}
	delete(props, profileArg) // the grant's own, not a workflow input field
	if props["keywords"] == nil || len(props) != 1 {
		t.Fatalf("automation_publish properties = %v, want keywords only", props)
	}

	liveHeartbeat(t)
	args := `{"keywords":"golang","org":{"workdir":"/"},"trace":{"chain_id":"chn_spoof","hop":0},"trigger_type":"manual"}`
	if _, err := f.server.callGrantTool(context.Background(), "automation_publish", json.RawMessage(args)); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := f.db.DB.QueryRow(`SELECT trigger_data FROM workflow_executions ORDER BY rowid DESC LIMIT 1`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var data map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		t.Fatal(err)
	}
	if data["keywords"] != "golang" || data["trigger_type"] != workflow.TriggerTypeOrgTool {
		t.Fatalf("trigger data = %s", raw)
	}
	if org, _ := data["org"].(map[string]interface{}); org["workdir"] == "/" || org["automation"] != "publish" {
		t.Fatalf("org overridden by the arguments: %s", raw)
	}
	if tr, _ := data["trace"].(map[string]interface{}); tr["chain_id"] == "chn_spoof" {
		t.Fatalf("trace overridden by the arguments: %s", raw)
	}
	if in, _ := data["input"].(map[string]interface{}); in["keywords"] != "golang" {
		t.Fatalf("input lost: %s", raw)
	}

	got, err := workflow.NewExpressionEngine().EvaluateString("{{ $json.keywords }}|{{ $json.input.keywords }}",
		workflow.ExpressionContext{JSON: data})
	if err != nil || got != "golang|golang" {
		t.Fatalf("rendered %q, %v", got, err)
	}
}

// --profile takes an id or a name (as everywhere in the CLI); the grant's
// profile check must compare ids, so a grant for profile "work" is served to
// `--profile work` and still refused to any other profile.
func TestGrantScopeResolvesProfileNames(t *testing.T) {
	f := newGrantFixture(t, orggrant.Tool{})
	ctx := context.Background()
	const workID, homeID = "6f1c2a9e-0000-4000-8000-00000000a001", "6f1c2a9e-0000-4000-8000-00000000a002"
	for _, p := range [][2]string{{workID, "work"}, {homeID, "home"}} {
		if _, err := f.db.DB.Exec(`INSERT INTO profiles (id, name, created_at) VALUES (?, ?, '2026-01-01')`, p[0], p[1]); err != nil {
			t.Fatal(err)
		}
	}
	g, err := f.store.UpsertGrant(ctx, orggrant.GrantInput{
		ProfileID: workID, OrgName: "growth", RoleID: "writer",
		Tool: orggrant.Tool{Alias: "publish", WorkflowID: "wf-pub"}})
	if err != nil {
		t.Fatal(err)
	}
	scope := func(profile string) error {
		s := NewServer(Options{DBPath: f.server.opts.DBPath, Profile: profile, WorkflowsDir: f.server.opts.WorkflowsDir, Grant: g.ID})
		defer s.closeRuntime()
		_, _, err := s.grantScope(ctx)
		return err
	}
	for _, ok := range []string{"work", workID} {
		if err := scope(ok); err != nil {
			t.Errorf("--profile %s: %v", ok, err)
		}
	}
	for _, bad := range []string{"home", homeID, "nosuch"} {
		if err := scope(bad); err == nil {
			t.Errorf("--profile %s was served a grant of profile work", bad)
		} else if bad != "nosuch" && !strings.HasPrefix(err.Error(), codeRefusedGrant) {
			t.Errorf("--profile %s: %v", bad, err)
		}
	}
}
