package workflowmanage

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// seedWorkflow saves a trigger.manual → core.set workflow (core.set reads $json.prompts) into the
// test database and returns its id.
func seedWorkflow(t *testing.T, cfg *testConfig, value string) string {
	t.Helper()
	db, err := storage.NewDatabase(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	wf := &workflow.Workflow{ID: "wf-" + value, Name: "seeded", Version: 1, ProfileID: "default",
		Nodes: []workflow.WorkflowNode{
			{ID: "t", Type: "trigger.manual", Name: "T"},
			{ID: "s", Type: "core.set", Name: "S", Config: map[string]interface{}{"value": "{{ json $json.prompts }}"}},
		},
		Connections: []workflow.WorkflowConnection{{ID: "c", SourceNodeID: "t", SourceHandle: "main", TargetNodeID: "s", TargetHandle: "main"}},
	}
	for i := range wf.Nodes {
		if err := wf.Nodes[i].MarshalConfig(); err != nil {
			t.Fatal(err)
		}
	}
	if err := testDeps(cfg).CreateWorkflow(context.Background(), sqliteStore(db), wf, false); err != nil {
		t.Fatal(err)
	}
	return wf.ID
}

func TestWorkflowCreateGetList(t *testing.T) {
	cfg := newTestDB(t, "")
	out, err := run(t, cfg, "create", "My flow", "--description", "d")
	if err != nil {
		t.Fatal(err)
	}
	var created workflow.Workflow
	if err := json.Unmarshal([]byte(out), &created); err != nil || created.Name != "My flow" || created.ProfileID != "default" {
		t.Fatalf("create printed %q (%v)", out, err)
	}
	out, err = run(t, cfg, "get", created.ID)
	if err != nil || !strings.Contains(out, `"name": "My flow"`) {
		t.Fatalf("get = %q, %v", out, err)
	}
	out, err = run(t, cfg, "list")
	if err != nil || !strings.Contains(out, `"node_count":0`) || !strings.Contains(out, created.ID) {
		t.Fatalf("list = %q, %v", out, err)
	}
	cfg.JSONOutput = false
	if out, err = run(t, cfg, "create", "Plain"); err != nil || !strings.HasPrefix(out, "Created workflow: Plain  (id: ") {
		t.Fatalf("create (text) = %q, %v", out, err)
	}
}

func TestWorkflowGetDeleteNotFound(t *testing.T) {
	t.Parallel()
	cfg := newTestDB(t, `INSERT INTO workflows (id, name, is_active, profile_id) VALUES ('w3','C',1,'other')`)
	for _, args := range [][]string{
		{"get", "nope"}, {"get", "w3"},
		// Deleting an unknown workflow is exit 2, found before the prompt or the engine (RV4-4).
		{"delete", "no-such-workflow", "--force"}, {"delete", "w3", "--force"},
	} {
		if err := execute(cfg, nil, args...); exitCode(err) != 2 {
			t.Errorf("%v: exit %d (%v), want 2", args, exitCode(err), err)
		}
	}
}

// activate, deactivate and delete go through the CLI's engine.
func TestWorkflowEngineCommandsUseBuildEngine(t *testing.T) {
	t.Parallel()
	cfg := newTestDB(t, "")
	id := seedWorkflow(t, cfg, "engine")
	for _, args := range [][]string{{"activate", id}, {"deactivate", id}, {"delete", id, "--yes"}} {
		if err := execute(cfg, nil, args...); err == nil || !strings.Contains(err.Error(), "build engine: no engine in tests") {
			t.Errorf("%v: err = %v, want the engine's", args, err)
		}
	}
}

func TestWorkflowDeleteRefusedByOrgReferences(t *testing.T) {
	t.Parallel()
	cfg := newTestDB(t, "")
	id := seedWorkflow(t, cfg, "org")
	d := testDeps(cfg)
	var gotForce bool
	d.CheckOrgReferences = func(_ context.Context, wid string, force bool) error {
		gotForce = force
		return d.InvalidInput("workflow %q is used by grant to o:r", wid)
	}
	cmd := d.NewDeleteCmd()
	cmd.SetArgs([]string{id, "--force"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); exitCode(err) != 3 || !gotForce {
		t.Fatalf("exit %d (%v), force %v; want 3 with force", exitCode(err), err, gotForce)
	}
}

func TestWorkflowInputs(t *testing.T) {
	t.Parallel()
	cfg := newTestDB(t, "")
	id := seedWorkflow(t, cfg, "inputs")
	var out bytes.Buffer
	if err := execute(cfg, &out, "inputs", id); err != nil {
		t.Fatal(err)
	}
	var got struct {
		V        int    `json:"v"`
		Workflow string `json:"workflow"`
		Fields   []struct {
			Name       string `json:"name"`
			Structured bool   `json:"structured"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.V != 1 || got.Workflow != id ||
		len(got.Fields) != 1 || got.Fields[0].Name != "prompts" || !got.Fields[0].Structured {
		t.Fatalf("inputs = %s (%v)", out.String(), err)
	}

	cfg2 := *cfg
	cfg2.JSONOutput = false
	out.Reset()
	if err := execute(&cfg2, &out, "inputs", id); err != nil || !strings.Contains(out.String(), "seeded reads 1 trigger field(s):") ||
		!strings.Contains(out.String(), "monoagentcli workflow run "+id+" --input '") {
		t.Fatalf("inputs (text) = %q, %v", out.String(), err)
	}
	if err := execute(cfg, &out, "inputs", "nope"); exitCode(err) != 2 {
		t.Fatalf("unknown workflow: exit %d (%v), want 2", exitCode(err), err)
	}
}

func TestWorkflowVersions(t *testing.T) {
	cfg := newTestDB(t, "")
	id := seedWorkflow(t, cfg, "versions")
	out, err := run(t, cfg, "versions", id)
	if err != nil {
		t.Fatal(err)
	}
	var rows []VersionRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 1 || !rows[0].Current || rows[0].Nodes != 2 || rows[0].Connections != 1 {
		t.Fatalf("versions = %q (%v)", out, err)
	}
	if out, err = run(t, cfg, "versions", "show", id, "1"); err != nil || !strings.Contains(out, `"version":1`) {
		t.Fatalf("show = %q, %v", out, err)
	}
	if out, err = run(t, cfg, "versions", "restore", id, "1"); err != nil || !strings.Contains(out, `"restored_from":1`) {
		t.Fatalf("restore = %q, %v", out, err)
	}
	if out, err = run(t, cfg, "versions", "prune", "--all", "--keep", "1"); err != nil || !strings.Contains(out, `"deleted":`) {
		t.Fatalf("prune = %q, %v", out, err)
	}
}

func TestWorkflowVersionsErrors(t *testing.T) {
	t.Parallel()
	cfg := newTestDB(t, `INSERT INTO workflows (id, name, is_active, profile_id) VALUES ('w3','C',1,'other')`)
	id := seedWorkflow(t, cfg, "verr")
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"versions", "no-such-workflow"}, 2},
		{[]string{"versions", "w3"}, 2},
		{[]string{"versions", "show", id, "99"}, 2},
		{[]string{"versions", "show", id, "zero"}, 3},
		{[]string{"versions", "restore", id, "7"}, 2},
		{[]string{"versions", "prune", "--keep", "0", id}, 3},
		{[]string{"versions", "prune", "--keep", "1"}, 3},
		{[]string{"versions", "prune", "--keep", "1", "w3"}, 2},
	} {
		if err := execute(cfg, nil, tc.args...); exitCode(err) != tc.code {
			t.Errorf("%v: exit %d (%v), want %d", tc.args, exitCode(err), err, tc.code)
		}
	}
}
