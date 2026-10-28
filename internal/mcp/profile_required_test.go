package mcp

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/orggrant"
	"github.com/monoes/mono-agent/internal/orgsign"
	"github.com/monoes/mono-agent/internal/publication"
	"github.com/monoes/mono-agent/internal/tasks"
	"github.com/monoes/mono-agent/internal/workflow"
)

// globalToolsExpected is the whole set of tools that act on the machine and take
// no profile. A tool joins it only by being argued for here: each reads and
// writes no profile data (node types and their schemas, the built-in docs,
// bundled workflow templates, the machine-wide message templates, the API
// server's saved settings and the daemon behind them).
var globalToolsExpected = []string{
	"api_config_apply", "api_config_get", "api_config_set", "docs", "node_list", "node_schema",
	"template_list", "workflow_template_list", "workflow_template_show",
}

const (
	profA   = "pa-0001"
	profB   = "pb-0002"
	nameA   = "Alpha"
	nameB   = "Beta"
	profAny = "pc-0003"
)

// twoProfileServer is a server that may mutate, with the profiles Alpha and
// Beta beside the default one, and a workflow tool for an active workflow of
// Alpha.
func twoProfileServer(t *testing.T) (*Server, *runtime) {
	t.Helper()
	keyring.MockInit()                                // secret_add goes through the real DEK/KEK machinery
	for _, m := range orgsign.AgentContextMarkers() { // publication_delete/redact refuse under an agent
		t.Setenv(m, "")
	}
	s := newTestServerAllowMutations(t, true)
	s.opts.WorkflowTools = "all"
	rt, err := s.runtime()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range [][2]string{{profA, nameA}, {profB, nameB}} {
		if _, err := rt.db.DB.Exec(`INSERT INTO profiles (id, name, created_at) VALUES (?, ?, '2026-01-01')`, p[0], p[1]); err != nil {
			t.Fatal(err)
		}
	}
	return s, rt
}

// toolDefs is what tools/list answers, by tool name.
func toolDefs(t *testing.T, s *Server) map[string]map[string]interface{} {
	t.Helper()
	out := map[string]map[string]interface{}{}
	for _, d := range definitionsOf(s.servedTools(), true) {
		out[d["name"].(string)] = d
	}
	return out
}

func requiredOf(def map[string]interface{}) []string {
	schema, _ := def["inputSchema"].(map[string]interface{})
	switch r := schema["required"].(type) {
	case []string:
		return r
	case []interface{}:
		var out []string
		for _, v := range r {
			out = append(out, v.(string))
		}
		return out
	}
	return nil
}

func hasString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// (a) Every tool the server lists (the workflow tools and task_approve included)
// requires `profile`, except the global tools, which do not take it.
func TestEveryScopedToolRequiresTheProfileArgument(t *testing.T) {
	s, rt := twoProfileServer(t)
	if err := rt.store.CreateWorkflow(workflow.WithApproval(context.Background()), &workflow.Workflow{ID: "wf-tool", Name: "Do the thing", ProfileID: profA, IsActive: true}); err != nil {
		t.Fatal(err)
	}
	defs := toolDefs(t, s)
	if len(defs) < 80 {
		t.Fatalf("only %d tools listed", len(defs))
	}
	var wfTool string
	for name := range defs {
		if strings.HasPrefix(name, "wf_") {
			wfTool = name
		}
	}
	if wfTool == "" {
		t.Fatal("the active workflow is not served as a tool")
	}
	// task_approve is a scoped tool too: serve it and check it.
	for _, tl := range scoped([]tool{taskApproveTool()}) {
		defs[tl.name] = definitionsOf([]tool{tl}, true)[0]
	}

	var globals []string
	for name, def := range defs {
		props, _ := def["inputSchema"].(map[string]interface{})["properties"].(map[string]interface{})
		if _, has := props[profileArg]; has != hasString(requiredOf(def), profileArg) {
			t.Errorf("%s: profile property %v but required %v", name, has, requiredOf(def))
		}
		if !hasString(requiredOf(def), profileArg) {
			globals = append(globals, name)
		}
	}
	sort.Strings(globals)
	if strings.Join(globals, ",") != strings.Join(globalToolsExpected, ",") {
		t.Errorf("tools without a required profile = %v, want %v", globals, globalToolsExpected)
	}
}

// (b) A scoped call without a profile is an invalid-input error that names the
// argument and lists the profiles, before any handler runs: for every scoped tool.
func TestACallWithoutAProfileIsRefusedAndListsTheProfiles(t *testing.T) {
	s, _ := twoProfileServer(t)
	for name, def := range toolDefs(t, s) {
		if !hasString(requiredOf(def), profileArg) {
			continue
		}
		for _, args := range []string{``, `{}`, `{"profile":""}`, `{"profile":null}`} {
			_, err := callTool(context.Background(), s, name, json.RawMessage(args))
			if err == nil {
				t.Errorf("%s %q: no error", name, args)
				continue
			}
			msg := err.Error()
			if !strings.HasPrefix(msg, "invalid_input: ") || !strings.Contains(msg, `"profile"`) ||
				!strings.Contains(msg, "Alpha ("+profA+")") || !strings.Contains(msg, "Beta ("+profB+")") {
				t.Errorf("%s %q: %v, want invalid_input naming profile and listing the profiles", name, args, err)
			}
		}
	}
	// A global tool needs none.
	if _, err := callTool(context.Background(), s, "node_list", nil); err != nil {
		t.Errorf("node_list without a profile: %v", err)
	}
	if _, err := callTool(context.Background(), s, "template_list", nil); err != nil {
		t.Errorf("template_list without a profile: %v", err)
	}
}

// An unknown profile is refused with the list; a name two profiles share is
// refused too, and never guessed.
func TestAnUnknownOrAmbiguousProfileIsRefused(t *testing.T) {
	s, rt := twoProfileServer(t)
	_, err := callToolIn(context.Background(), s, "nosuch", "workflow_list", nil)
	if err == nil || !strings.HasPrefix(err.Error(), "invalid_input: ") || !strings.Contains(err.Error(), "Alpha ("+profA+")") {
		t.Errorf("unknown profile: %v", err)
	}
	if _, err := rt.db.DB.Exec(`INSERT INTO profiles (id, name, created_at) VALUES (?, 'alpha', '2026-01-02')`, profAny); err != nil {
		t.Fatal(err)
	}
	_, err = callToolIn(context.Background(), s, "Alpha", "workflow_list", nil)
	if err == nil || !strings.Contains(err.Error(), "matches 2 profiles") || !strings.Contains(err.Error(), profA) || !strings.Contains(err.Error(), profAny) {
		t.Errorf("ambiguous name: %v", err)
	}
	// Its id is not ambiguous.
	if _, err := callToolIn(context.Background(), s, profA, "workflow_list", nil); err != nil {
		t.Errorf("by id: %v", err)
	}
}

// (d) A profile is named by id or by name (the CLI's rule: a name matches without regard to case).
func TestAProfileIsNamedByIDOrName(t *testing.T) {
	s, rt := twoProfileServer(t)
	if err := rt.store.CreateWorkflow(context.Background(), &workflow.Workflow{ID: "wf-alpha", Name: "Alpha's", ProfileID: profA}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{profA, nameA, "alpha", "ALPHA"} {
		out, err := callToolIn(context.Background(), s, p, "workflow_list", nil)
		if err != nil || !strings.Contains(out, "wf-alpha") {
			t.Errorf("profile %q: %s %v", p, out, err)
		}
	}
	for _, p := range []string{profB, nameB, "default"} {
		out, err := callToolIn(context.Background(), s, p, "workflow_list", nil)
		if err != nil || strings.Contains(out, "wf-alpha") {
			t.Errorf("profile %q sees Alpha's workflow: %s %v", p, out, err)
		}
	}
}

func mustCallIn(t *testing.T, s *Server, profile, name string, args string) string {
	t.Helper()
	out, err := callToolIn(context.Background(), s, profile, name, json.RawMessage(args))
	if err != nil {
		t.Fatalf("%s in %s: %v", name, profile, err)
	}
	return out
}

func notFoundIn(t *testing.T, s *Server, profile, name, args string) {
	t.Helper()
	out, err := callToolIn(context.Background(), s, profile, name, json.RawMessage(args))
	if err == nil {
		t.Errorf("%s %s from %s succeeded: %s", name, args, profile, out)
		return
	}
	if !strings.Contains(err.Error(), "not found") && !strings.Contains(err.Error(), "not_found") {
		t.Errorf("%s %s from %s: %v, want not found", name, args, profile, err)
	}
}

// (c) A call of profile A cannot read or change a workflow, person, secret,
// task, HIL item or publication of profile B: it is not found, and B's record
// is untouched.
func TestACallOfOneProfileNeverReachesAnotherProfilesRecords(t *testing.T) {
	s, rt := twoProfileServer(t)
	ctx := context.Background()
	db := rt.db.DB

	// Beta's records.
	if err := rt.store.CreateWorkflow(ctx, &workflow.Workflow{ID: "wf-beta", Name: "Beta's", ProfileID: profB}); err != nil {
		t.Fatal(err)
	}
	exec := &workflow.WorkflowExecution{ID: "ex-beta", WorkflowID: "wf-beta", ProfileID: profB, Status: "SUCCESS", TriggerType: "manual"}
	if err := rt.store.CreateExecution(ctx, exec); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO people (id, platform_username, platform, profile_id) VALUES ('person-beta', 'bea', 'instagram', ?)`, profB); err != nil {
		t.Fatal(err)
	}
	secret := mustCallIn(t, s, nameB, "secret_add", `{"kind":"secret","name":"beta-key","fields":{"value":"x"}}`)
	var added struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(secret), &added); err != nil || added.ID == "" {
		t.Fatalf("secret_add result: %s (%v)", secret, err)
	}
	task, _, err := tasks.NewStore(db).Add(ctx, profB, tasks.AddInput{Title: "beta task", Ready: true}, tasks.Actor{Kind: tasks.Human})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, status, profile_id) VALUES ('hil-beta', 'ex-beta', 'wf-beta', 'n', 'Review', 'pending', ?)`, profB); err != nil {
		t.Fatal(err)
	}
	pub, err := publication.NewStore(db, profB).Register(ctx, publication.Entry{Platform: "custom", Kind: "post", Body: "beta post"})
	if err != nil {
		t.Fatal(err)
	}

	// Lists from Alpha show none of them; lists from Beta show them.
	for _, c := range []struct{ tool, args, needle string }{
		{"workflow_list", `{}`, "wf-beta"}, {"person_list", `{}`, "person-beta"}, {"secret_list", `{}`, "beta-key"},
		{"task_list", `{}`, "beta task"}, {"hil_list", `{}`, "hil-beta"}, {"publication_list", `{}`, "beta post"},
		{"workflow_executions", `{}`, "ex-beta"},
	} {
		if out := mustCallIn(t, s, nameA, c.tool, c.args); strings.Contains(out, c.needle) {
			t.Errorf("%s from Alpha lists Beta's %q: %s", c.tool, c.needle, out)
		}
		if out := mustCallIn(t, s, nameB, c.tool, c.args); !strings.Contains(out, c.needle) {
			t.Errorf("%s from Beta does not list its own %q: %s", c.tool, c.needle, out)
		}
	}

	// Reads and writes by id from Alpha are not found.
	taskID := `{"id":` + itoa(task.ID) + `}`
	notFoundIn(t, s, nameA, "workflow_get", `{"id":"wf-beta"}`)
	notFoundIn(t, s, nameA, "workflow_validate", `{"id":"wf-beta"}`)
	notFoundIn(t, s, nameA, "workflow_status", `{"execution_id":"ex-beta"}`)
	notFoundIn(t, s, nameA, "workflow_run", `{"id":"wf-beta"}`)
	notFoundIn(t, s, nameA, "workflow_set_active", `{"workflow_id":"wf-beta","active":true}`)
	notFoundIn(t, s, nameA, "workflow_delete", `{"workflow_id":"wf-beta"}`)
	notFoundIn(t, s, nameA, "workflow_node_add", `{"workflow_id":"wf-beta","node_type":"core.noop","name":"n"}`)
	notFoundIn(t, s, nameA, "person_get", `{"person_id":"person-beta"}`)
	notFoundIn(t, s, nameA, "person_delete", `{"person_id":"person-beta"}`)
	notFoundIn(t, s, nameA, "secret_update", `{"id":"`+added.ID+`","notes":"hijacked"}`)
	notFoundIn(t, s, nameA, "secret_delete", `{"id":"`+added.ID+`"}`)
	notFoundIn(t, s, nameA, "task_get", taskID)
	notFoundIn(t, s, nameA, "task_claim", taskID)
	notFoundIn(t, s, nameA, "hil_approve", `{"id":"hil-beta"}`)
	notFoundIn(t, s, nameA, "hil_reject", `{"id":"hil-beta"}`)
	notFoundIn(t, s, nameA, "publication_get", `{"id":"`+pub.ID+`"}`)
	notFoundIn(t, s, nameA, "publication_delete", `{"id":"`+pub.ID+`"}`)
	notFoundIn(t, s, nameA, "publication_redact", `{"id":"`+pub.ID+`"}`)

	// Beta's records are all still there and unchanged.
	for _, c := range []struct{ tool, args, needle string }{
		{"workflow_get", `{"id":"wf-beta"}`, "Beta's"}, {"person_get", `{"person_id":"person-beta"}`, "bea"},
		{"secret_list", `{}`, "beta-key"}, {"task_get", taskID, "beta task"},
		{"hil_list", `{}`, "hil-beta"}, {"publication_get", `{"id":"` + pub.ID + `"}`, "beta post"},
	} {
		if out := mustCallIn(t, s, nameB, c.tool, c.args); !strings.Contains(out, c.needle) {
			t.Errorf("%s from Beta lost %q: %s", c.tool, c.needle, out)
		}
	}
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// A workflow tool belongs to the profile of its workflow: the same tool called
// with another profile does not find the workflow.
func TestAWorkflowToolIsBoundToItsWorkflowsProfile(t *testing.T) {
	s, rt := twoProfileServer(t)
	if err := rt.store.CreateWorkflow(workflow.WithApproval(context.Background()), &workflow.Workflow{ID: "wf-tool", Name: "Do the thing", ProfileID: profA, IsActive: true}); err != nil {
		t.Fatal(err)
	}
	var name string
	for n := range toolDefs(t, s) {
		if strings.HasPrefix(n, "wf_") {
			name = n
		}
	}
	if name == "" {
		t.Fatal("no workflow tool")
	}
	if _, err := callToolIn(context.Background(), s, nameB, name, json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("a workflow tool called with another profile: %v, want not found", err)
	}
}

// (e) A grant server serves its grant's profile: the profile argument is
// accepted when it names that profile, by id or name, and refused otherwise.
func TestGrantModeRefusesAProfileArgumentThatIsNotTheGrants(t *testing.T) {
	f := newGrantFixture(t, orggrant.Tool{})
	ctx := context.Background()
	if _, err := f.db.DB.Exec(`INSERT INTO profiles (id, name, created_at) VALUES (?, ?, '2026-01-01')`, profA, nameA); err != nil {
		t.Fatal(err)
	}
	// Both its own profile and no profile at all are accepted (the stripped
	// arguments reach the workflow; the run itself needs the daemon, so only the
	// profile check is observed here).
	rt, err := f.server.runtime()
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.store.ResolveBundle(ctx, f.grant.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, ok := range []string{`{"text":"hi"}`, `{"profile":"default","text":"hi"}`, `{"profile":"Default","text":"hi"}`} {
		rest, err := f.server.checkGrantProfileArg(rt, b, json.RawMessage(ok))
		if err != nil {
			t.Errorf("%s: %v", ok, err)
		}
		if strings.Contains(string(rest), profileArg) {
			t.Errorf("%s: the profile reached the tool's input: %s", ok, rest)
		}
	}
	for _, bad := range []string{`{"profile":"` + profA + `"}`, `{"profile":"Alpha"}`, `{"profile":"nosuch"}`} {
		if _, err := f.server.callGrantTool(ctx, "automation_publish", json.RawMessage(bad)); err == nil || !strings.HasPrefix(err.Error(), codeRefusedGrant) {
			t.Errorf("%s: %v, want %s", bad, err, codeRefusedGrant)
		}
		if _, err := f.server.callGrantTool(ctx, "automation_status", json.RawMessage(bad)); err == nil || !strings.HasPrefix(err.Error(), codeRefusedGrant) {
			t.Errorf("automation_status %s: %v, want %s", bad, err, codeRefusedGrant)
		}
	}
}

// A grant server started without --profile serves nothing.
func TestGrantModeNeedsItsProfile(t *testing.T) {
	f := newGrantFixture(t, orggrant.Tool{})
	f.server.opts.Profile = ""
	if _, _, err := f.server.grantScope(context.Background()); err == nil || !strings.HasPrefix(err.Error(), codeRefusedGrant) || !strings.Contains(err.Error(), "--profile") {
		t.Fatalf("grant mode without --profile: %v", err)
	}
}
