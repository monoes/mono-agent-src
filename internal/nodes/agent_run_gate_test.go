package nodes

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// A browser action of a package that isn't built-in (recorded, local,
// imported, or one whose trust can't be read) runs only for the user: an
// agent (a marker), a step the user hasn't approved, or a workflow such a
// one called is refused before any tab, whatever the action declares. A
// built-in package's action runs for them as before (its sends wait for
// approval in hilsend), and the user runs any.
func TestAnAgentRunsNoBrowserActionOfAPackageThatIsntBuiltIn(t *testing.T) {
	prev := globalSessionProvider
	t.Cleanup(func() { action.SetDefSource(nil); SetGlobalSessionProvider(prev) })
	agents := map[string]func(t *testing.T) context.Context{
		"an agent's shell": func(t *testing.T) context.Context {
			t.Setenv("CLAUDECODE", "1")
			return context.Background()
		},
		"another agent's shell": func(t *testing.T) context.Context {
			t.Setenv("AI_AGENT", "1")
			return context.Background()
		},
		"an unapproved step": func(*testing.T) context.Context {
			return workflow.WithAIAuthored(context.Background())
		},
		"a workflow an unapproved one called": func(*testing.T) context.Context {
			return workflow.WithTrigger(context.Background(), workflow.TriggerTypeExecuteWorkflow,
				map[string]interface{}{workflow.UnapprovedCallerKey: "wf-agent"})
		},
	}
	for _, trust := range []string{"recorded", "local", "imported", ""} {
		for who, ctxOf := range agents {
			t.Run(trust+"/"+who, func(t *testing.T) {
				action.SetDefSource(liveSource{pkg: livePkg{trust: trust, confirmed: true}, effects: liveEffects})
				p := &countingProvider{}
				SetGlobalSessionProvider(p)
				_, err := NewBrowserNode("acme", "list").Execute(ctxOf(t), workflow.NodeInput{}, map[string]interface{}{})
				if !errors.Is(err, ErrUserOnlyBrowserRun) || p.calls != 0 {
					t.Fatalf("err = %v, tabs %d; want ErrUserOnlyBrowserRun before any tab", err, p.calls)
				}
			})
		}
	}
	for who, ctxOf := range map[string]func(t *testing.T) context.Context{
		"an agent, on a built-in package": agents["an agent's shell"],
		"the user, on a recorded package": func(*testing.T) context.Context { return context.Background() },
	} {
		t.Run(who, func(t *testing.T) {
			trust := "recorded"
			if who == "an agent, on a built-in package" {
				trust = "builtin"
			}
			action.SetDefSource(liveSource{pkg: livePkg{trust: trust}, effects: liveEffects})
			p := &countingProvider{}
			SetGlobalSessionProvider(p)
			_, err := NewBrowserNode("acme", "list").Execute(ctxOf(t), workflow.NodeInput{}, map[string]interface{}{})
			if errors.Is(err, ErrUserOnlyBrowserRun) || p.calls != 1 {
				t.Fatalf("err = %v, tabs %d; want it to reach the browser", err, p.calls)
			}
		})
	}
}

// A package an agent writes by hand, a folder under ~/.monoagent/actions,
// becomes a local package (local-<p>, also under its own name), whose
// actions an agent doesn't run either.
func TestAnAgentRunsNoHandWrittenLocalPackage(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "actions", "handmade")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "press.json"), []byte(`{"actionType":"press","steps":[{"id":"go","type":"click","selector":"#go"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := BootAutomations(home); err != nil {
		t.Fatal(err)
	}
	prev := globalSessionProvider
	t.Cleanup(func() { action.SetDefSource(nil); SetGlobalSessionProvider(prev) })
	r := workflow.NewNodeTypeRegistry()
	RegisterBrowserNodes(r)
	t.Setenv("CLAUDECODE", "1")
	for _, typ := range []string{"local-handmade.press", "handmade.press"} {
		f, ok := r.Get(typ)
		if !ok {
			t.Fatalf("%s is not registered (%v)", typ, r.Types())
		}
		p := &countingProvider{}
		SetGlobalSessionProvider(p)
		if _, err := f().Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{}); !errors.Is(err, ErrUserOnlyBrowserRun) || p.calls != 0 {
			t.Errorf("%s: err = %v, tabs %d; want ErrUserOnlyBrowserRun before any tab", typ, err, p.calls)
		}
	}
}

// A symlinked actions folder doesn't get an agent past the gate. When
// ~/.monoagent/actions itself is a symlink, its packages load as local
// ones and an agent runs none of them; a symlinked package folder inside it
// isn't loaded at all.
func TestAnAgentRunsNoActionFromASymlinkedActionsFolder(t *testing.T) {
	writePress := func(dir string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "press.json"), []byte(`{"actionType":"press","steps":[{"id":"go","type":"click","selector":"#go"}]}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	prev := globalSessionProvider
	t.Cleanup(func() { action.SetDefSource(nil); SetGlobalSessionProvider(prev) })

	t.Run("the actions folder is a symlink", func(t *testing.T) {
		home, elsewhere := t.TempDir(), t.TempDir()
		writePress(filepath.Join(elsewhere, "linked"))
		if err := os.Symlink(elsewhere, filepath.Join(home, "actions")); err != nil {
			t.Fatal(err)
		}
		if _, err := BootAutomations(home); err != nil {
			t.Fatal(err)
		}
		r := workflow.NewNodeTypeRegistry()
		RegisterBrowserNodes(r)
		if got := automationTrust("local-linked"); got != "local" {
			t.Fatalf("the package's trust is %q, want local", got)
		}
		t.Setenv("CLAUDECODE", "1")
		for _, typ := range []string{"local-linked.press", "linked.press"} {
			f, ok := r.Get(typ)
			if !ok {
				t.Fatalf("%s is not registered", typ)
			}
			p := &countingProvider{}
			SetGlobalSessionProvider(p)
			if _, err := f().Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{}); !errors.Is(err, ErrUserOnlyBrowserRun) || p.calls != 0 {
				t.Errorf("%s: err = %v, tabs %d; want ErrUserOnlyBrowserRun before any tab", typ, err, p.calls)
			}
		}
	})

	t.Run("a package folder is a symlink", func(t *testing.T) {
		home, elsewhere := t.TempDir(), t.TempDir()
		writePress(elsewhere)
		if err := os.MkdirAll(filepath.Join(home, "actions"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(elsewhere, filepath.Join(home, "actions", "linked")); err != nil {
			t.Fatal(err)
		}
		if _, err := BootAutomations(home); err != nil {
			t.Fatal(err)
		}
		r := workflow.NewNodeTypeRegistry()
		RegisterBrowserNodes(r)
		for _, typ := range []string{"local-linked.press", "linked.press"} {
			if _, ok := r.Get(typ); ok {
				t.Errorf("%s is registered from a symlinked package folder", typ)
			}
		}
	})
}

// A workflow the user approved runs a local package's action as theirs; once
// it is changed and not approved again (an agent edited it), its run is an
// unapproved one, and the action is refused before a tab.
func TestAnApprovedWorkflowEditedSinceRunsNoLocalBrowserAction(t *testing.T) {
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	ctx := vault.ContextWithProfileID(vault.ContextWithDB(workflow.WithNodeState(context.Background(), nil), db.DB), "default")
	action.SetDefSource(liveSource{pkg: livePkg{trust: "local", confirmed: true}, effects: liveEffects})
	prev := globalSessionProvider
	p := &countingProvider{}
	SetGlobalSessionProvider(p)
	t.Cleanup(func() { action.SetDefSource(nil); SetGlobalSessionProvider(prev) })
	store := workflow.NewSQLiteWorkflowStore(db.DB)
	reg := workflow.NewNodeTypeRegistry()
	reg.Register("acme.list", func() workflow.NodeExecutor { return NewBrowserNode("acme", "list") })
	wf := &workflow.Workflow{ID: "wf-edit", Name: "wf-edit", ProfileID: "default",
		Nodes: []workflow.WorkflowNode{
			{ID: "t", WorkflowID: "wf-edit", Type: "trigger.manual", Name: "T"},
			{ID: "l", WorkflowID: "wf-edit", Type: "acme.list", Name: "List", Config: map[string]interface{}{"keywords": "a"}},
		},
		Connections: []workflow.WorkflowConnection{{ID: "c", WorkflowID: "wf-edit", SourceNodeID: "t", SourceHandle: "main", TargetNodeID: "l", TargetHandle: "main"}},
	}
	for _, err := range []error{store.CreateWorkflow(ctx, wf), store.SaveWorkflowNodes(ctx, wf.ID, wf.Nodes),
		store.SaveWorkflowConnections(ctx, wf.ID, wf.Connections), store.ApproveDefinition(ctx, wf.ID)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	run := func() error {
		t.Helper()
		dag, err := workflow.BuildDAG(wf.Nodes, wf.Connections)
		if err != nil {
			t.Fatal(err)
		}
		exec := &workflow.WorkflowExecution{ID: uuid.NewString(), WorkflowID: wf.ID, ProfileID: "default", TriggerType: "manual", TriggerNodeID: "t",
			TriggerData: map[string]interface{}{}}
		if err := store.CreateExecution(ctx, exec); err != nil {
			t.Fatal(err)
		}
		return workflow.RunExecution(ctx, exec, wf, dag, reg, store, nil, workflow.NewExpressionEngine(), zerolog.Nop())
	}
	_ = run()
	if p.calls != 1 {
		t.Fatalf("the approved workflow's run opened %d tab(s), want 1", p.calls)
	}
	wf.Nodes[1].Config = map[string]interface{}{"keywords": "edited by an agent"}
	if err := store.SaveWorkflowNodes(ctx, wf.ID, wf.Nodes); err != nil {
		t.Fatal(err)
	}
	if err := run(); err == nil || !strings.Contains(err.Error(), "only the user runs") || p.calls != 1 {
		t.Fatalf("the edited workflow's run: %v, tabs %d; want it refused before a tab", err, p.calls)
	}
}

// A run an agent started with `workflow run` carries that as its mark (workflow.AICallerKey), also when
// the process had no marker (a coder turn): a package that isn't built-in is not run in the user's browser.
func TestAMarkedAgentRunRunsNoBrowserActionOfAPackageThatIsntBuiltIn(t *testing.T) {
	prev := globalSessionProvider
	t.Cleanup(func() { action.SetDefSource(nil); SetGlobalSessionProvider(prev) })
	for _, k := range []string{"CLAUDECODE", "AI_AGENT"} {
		t.Setenv(k, "")
	}
	for _, mark := range []string{"other", "coder", "other/org"} {
		action.SetDefSource(liveSource{pkg: livePkg{trust: "recorded", confirmed: true}, effects: liveEffects})
		p := &countingProvider{}
		SetGlobalSessionProvider(p)
		ctx := workflow.WithTrigger(context.Background(), "trigger.manual", map[string]interface{}{workflow.AICallerKey: mark})
		if _, err := NewBrowserNode("acme", "list").Execute(ctx, workflow.NodeInput{}, map[string]interface{}{}); !errors.Is(err, ErrUserOnlyBrowserRun) || p.calls != 0 {
			t.Errorf("mark %q: err = %v, tabs %d; want ErrUserOnlyBrowserRun before any tab", mark, err, p.calls)
		}
	}
	action.SetDefSource(liveSource{pkg: livePkg{trust: "recorded", confirmed: true}, effects: liveEffects})
	p := &countingProvider{}
	SetGlobalSessionProvider(p)
	ctx := workflow.WithTrigger(context.Background(), "trigger.manual", map[string]interface{}{"who": "zed"})
	if _, err := NewBrowserNode("acme", "list").Execute(ctx, workflow.NodeInput{}, map[string]interface{}{}); errors.Is(err, ErrUserOnlyBrowserRun) || p.calls != 1 {
		t.Errorf("the user's unmarked run: err = %v, tabs %d; want it to reach the browser", err, p.calls)
	}
}
