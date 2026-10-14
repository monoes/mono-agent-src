package workflowtemplates

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// The commands print on os.Stdout, so the tests that read it stay serial.

type testConfig struct {
	DBPath     string
	JSONOutput bool
}

func newTestConfig(t *testing.T) *testConfig {
	t.Helper()
	return &testConfig{DBPath: filepath.Join(t.TempDir(), "t.db"), JSONOutput: true}
}

// testDeps is the CLI around the commands: a SQLite-only store, every node type installed, and
// a stand-in `templates run`.
func testDeps(cfg *testConfig) Deps {
	return Deps{
		JSONOutput: func() bool { return cfg.JSONOutput },
		ProfileID:  func() string { return "default" },
		OpenDB: func() (*storage.Database, error) {
			db, err := storage.NewDatabase(cfg.DBPath)
			if err != nil {
				return nil, err
			}
			if err := db.ApplyMigrations(); err != nil {
				db.Close()
				return nil, err
			}
			return db, nil
		},
		WorkflowStore: func(db *storage.Database) *workflow.HybridWorkflowStore {
			return workflow.NewHybridWorkflowStore(nil, workflow.NewSQLiteWorkflowStore(db.DB))
		},
		CreateWorkflow: func(ctx context.Context, store *workflow.HybridWorkflowStore, wf *workflow.Workflow, _ bool) error {
			if err := store.CreateWorkflow(ctx, wf); err != nil {
				return err
			}
			for i := range wf.Nodes {
				if err := wf.Nodes[i].MarshalConfig(); err != nil {
					return err
				}
			}
			if err := store.SaveWorkflowNodes(ctx, wf.ID, wf.Nodes); err != nil {
				return err
			}
			return store.SaveWorkflowConnections(ctx, wf.ID, wf.Connections)
		},
		CheckNodeTypes: func(string, workflow.WorkflowFile) error { return nil },
		RunCmd:         func() *cobra.Command { return &cobra.Command{Use: "run <template-id>"} },
	}
}

func run(t *testing.T, d Deps, args ...string) (string, error) {
	t.Helper()
	root := &cobra.Command{Use: "workflow", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(d.NewTemplatesCmd(), d.NewSearchCmd())
	root.SetArgs(args)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	runErr := root.Execute()
	os.Stdout = orig
	w.Close()
	out, _ := io.ReadAll(r)
	return string(out), runErr
}

func firstTemplate(t *testing.T) workflow.Template {
	t.Helper()
	tmpls := workflow.ListTemplates()
	if len(tmpls) == 0 {
		t.Skip("no bundled templates")
	}
	return tmpls[0]
}

func TestInputHelpers(t *testing.T) {
	t.Parallel()
	if got := InputKeyList(nil); got != "(none)" {
		t.Errorf("InputKeyList(nil) = %q", got)
	}
	if got := InputKeyList([]string{"a", "b"}); got != "a,b" {
		t.Errorf("InputKeyList = %q", got)
	}
	if got := ExampleInputJSON(nil); got != "" {
		t.Errorf("ExampleInputJSON(nil) = %q", got)
	}
	if got := ExampleInputJSON([]string{"prompts", "keywords", "url"}); got != `{"prompts":["...","..."],"keywords":"...","url":"..."}` {
		t.Errorf("ExampleInputJSON = %q", got)
	}
}

// Every instantiation gets fresh ids, connections follow the nodes, and an Outlook sync node is
// scoped to the profile.
func TestInstantiateTemplate(t *testing.T) {
	t.Parallel()
	tmpl := workflow.WorkflowFile{Name: "n", Description: "d",
		Nodes:       []workflow.WorkflowFileNode{{ID: "trigger", Type: "trigger.manual"}, {ID: "sync", Type: "people.sync_outlook_message"}},
		Connections: []workflow.WorkflowFileEdge{{ID: "e", Source: "trigger", Target: "sync", SourceHandle: "main", TargetHandle: "main"}},
	}
	a, b := InstantiateTemplate(tmpl, "p1"), InstantiateTemplate(tmpl, "p1")
	if a.ID == b.ID || a.Nodes[0].ID == b.Nodes[0].ID || a.Nodes[0].ID == "trigger" {
		t.Fatalf("ids not fresh: %s %s", a.Nodes[0].ID, b.Nodes[0].ID)
	}
	if c := a.Connections[0]; c.SourceNodeID != a.Nodes[0].ID || c.TargetNodeID != a.Nodes[1].ID || c.WorkflowID != a.ID {
		t.Fatalf("connection %+v", c)
	}
	if a.IsActive || a.Version != 1 || a.ProfileID != "p1" || a.Nodes[1].Config["profile_id"] != "p1" {
		t.Fatalf("instantiated %+v", a)
	}
}

func TestTemplatesListShow(t *testing.T) {
	tmpl := firstTemplate(t)
	cfg := newTestConfig(t)
	out, err := run(t, testDeps(cfg), "templates", "list")
	var listed []workflow.Template
	if err != nil || json.Unmarshal([]byte(out), &listed) != nil || len(listed) != len(workflow.ListTemplates()) {
		t.Fatalf("list = %q, %v", out, err)
	}
	out, err = run(t, testDeps(cfg), "templates", "show", tmpl.ID)
	if err != nil || !strings.Contains(out, `"id":"`+tmpl.ID+`"`) || !strings.Contains(out, `"definition":`) {
		t.Fatalf("show = %q, %v", out, err)
	}
	cfg.JSONOutput = false
	if out, err = run(t, testDeps(cfg), "templates", "show", tmpl.ID); err != nil ||
		!strings.Contains(out, "Run it:\n  monoagentcli workflow templates run "+tmpl.ID) {
		t.Fatalf("show (text) = %q, %v", out, err)
	}
	if out, err = run(t, testDeps(cfg), "templates", "list"); err != nil || !strings.HasPrefix(out, "ID ") {
		t.Fatalf("list (text) = %q, %v", out, err)
	}
	for _, sub := range []string{"show", "use"} {
		if _, err := run(t, testDeps(cfg), "templates", sub, "nope"); err == nil ||
			err.Error() != `unknown template "nope". Run `+"`monoagentcli workflow templates list`"+` to see available IDs` {
			t.Errorf("%s nope: %v", sub, err)
		}
	}
	if _, _, err := testDeps(cfg).NewTemplatesCmd().Find([]string{"run"}); err != nil {
		t.Errorf("templates run is not attached: %v", err)
	}
}

func TestTemplatesUseAndSearch(t *testing.T) {
	tmpl := firstTemplate(t)
	cfg := newTestConfig(t)
	d := testDeps(cfg)
	want := errors.New("not installed")
	d.CheckNodeTypes = func(id string, _ workflow.WorkflowFile) error {
		if id != tmpl.ID {
			t.Errorf("checked %q", id)
		}
		return want
	}
	if _, err := run(t, d, "templates", "use", tmpl.ID); !errors.Is(err, want) {
		t.Fatalf("use with a missing node type: %v", err)
	}
	if _, err := os.Stat(cfg.DBPath); err == nil {
		t.Fatal("the database was opened before the node types were checked")
	}

	out, err := run(t, testDeps(cfg), "templates", "use", tmpl.ID)
	var created workflow.Workflow
	if err != nil || json.Unmarshal([]byte(out), &created) != nil || created.ID == "" || created.IsActive || len(created.Nodes) == 0 {
		t.Fatalf("use = %q, %v", out, err)
	}
	// The saved copy reads as plain text in the app: the template's summary,
	// not its "Run with: workflow templates run …" description.
	if tmpl.Summary == "" || created.Description != tmpl.Summary {
		t.Errorf("saved description = %q, want the summary %q", created.Description, tmpl.Summary)
	}

	out, err = run(t, testDeps(cfg), "search")
	var hits []struct {
		Source, ID, Command string
	}
	if err != nil || json.Unmarshal([]byte(out), &hits) != nil {
		t.Fatalf("search = %q, %v", out, err)
	}
	var sawTemplate, sawWorkflow bool
	for _, h := range hits {
		sawTemplate = sawTemplate || (h.Source == "template" && h.ID == tmpl.ID && strings.HasPrefix(h.Command, "monoagentcli workflow templates run "+tmpl.ID))
		sawWorkflow = sawWorkflow || (h.Source == "workflow" && h.ID == created.ID && h.Command == "monoagentcli workflow run "+created.ID)
	}
	if !sawTemplate || !sawWorkflow {
		t.Fatalf("search hits = %+v", hits)
	}
	if out, err = run(t, testDeps(cfg), "search", "zzz-no-such-thing"); err != nil || strings.TrimSpace(out) != "[]" {
		t.Fatalf("search with no match = %q, %v", out, err)
	}
	cfg.JSONOutput = false
	if out, err = run(t, testDeps(cfg), "search", "zzz-no-such-thing"); err != nil || !strings.HasPrefix(out, `Nothing matches "zzz-no-such-thing".`) {
		t.Fatalf("search with no match (text) = %q, %v", out, err)
	}
}
