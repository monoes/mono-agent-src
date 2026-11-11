package workflow

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/vault"
)

// Issue #1418: a granted @secret: must not be published as content. A node
// that publishes to third parties is refused, under agent access, when its
// run has read a secret (taint) or its own config holds one. These tests are
// red until the guard lands in RunExecGuard's seam; test.publishprobe stands
// for a registered publishing node (an undeclared type is publishing).

type publishProbe struct {
	mu   sync.Mutex
	runs []string
}

func (p *publishProbe) Type() string { return "test.publishprobe" }
func (p *publishProbe) Execute(_ context.Context, _ NodeInput, config map[string]interface{}) ([]NodeOutput, error) {
	text, _ := config["text"].(string)
	p.mu.Lock()
	p.runs = append(p.runs, text)
	p.mu.Unlock()
	return []NodeOutput{{Handle: "main", Items: []Item{NewItem(map[string]interface{}{"published": true})}}}, nil
}

func (p *publishProbe) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.runs)
}

// registerPublish registers the probe as a fresh instance and returns it.
func (e *accessEnv) registerPublish() *publishProbe {
	p := &publishProbe{}
	e.reg.Register("test.publishprobe", func() NodeExecutor { return p })
	return p
}

// P2: node A (an http.request to a granted host) echoes the secret; node B
// publishes it by data flow. B resolves no @secret: itself, so only the
// run's taint can stop it.
func TestAgentAccessPublishingNodeRefusedWhenRunReadASecret(t *testing.T) {
	e := newAccessEnv(t)
	e.reg.Register("http.request", func() NodeExecutor { return echoProbe{} })
	pub := e.registerPublish()
	e.grant(t, secrets.AccessGrant{Hosts: []string{"api.example.com"}})
	wf := chain("wf-p2",
		step("a1", "http.request", "Call", map[string]interface{}{"v": "@secret:vs"}),
		step("b1", "test.publishprobe", "Post", map[string]interface{}{"text": "{{ $json.v }}"}),
	)
	execID, err := e.run(t, wf, nil)
	if err == nil {
		t.Errorf("a publishing node ran in a run that read a granted secret (issue #1418, P2): want a refusal, got none")
	}
	if n := pub.count(); n != 0 {
		t.Errorf("the publishing node executed %d time(s) with %q; the refusal must come before anything is sent", n, pub.runs)
	}
	if strings.Contains(e.stored(t, execID), valVS) {
		t.Errorf("the stored run holds the value")
	}
}

// P3: --any-host lets every node type read a secret, and it does not lift
// the content guard: a publishing node whose own config is the reference is
// refused.
func TestAgentAccessAnyHostGrantStillRefusesSecretInPublishedContent(t *testing.T) {
	e := newAccessEnv(t)
	pub := e.registerPublish()
	e.grant(t, secrets.AccessGrant{AnyHost: true})
	_, err := e.run(t, chain("wf-p3", step("b1", "test.publishprobe", "Post", map[string]interface{}{"text": "@secret:vs"})), nil)
	if err == nil {
		t.Errorf("--any-host let a publishing node read a granted secret as its content (issue #1418, P3): want a refusal, got none")
	}
	if n := pub.count(); n != 0 {
		t.Errorf("the publishing node executed %d time(s) with %q", n, pub.runs)
	}
}

// Controls the guard must keep: a run that read nothing publishes, and an
// operator-approved workflow is untouched (it resolves with no grant).
func TestAgentAccessPublishGuardLeavesCleanAndApprovedRunsAlone(t *testing.T) {
	e := newAccessEnv(t)
	pub := e.registerPublish()
	e.grant(t, secrets.AccessGrant{AnyHost: true})
	if _, err := e.run(t, chain("wf-clean", step("b1", "test.publishprobe", "Post", map[string]interface{}{"text": "status: done"})), nil); err != nil {
		t.Errorf("a run that read no secret was refused: %v", err)
	}
	if pub.count() != 1 {
		t.Errorf("the clean run published %d times, want 1", pub.count())
	}
	approved := chain("wf-ok", step("b1", "test.publishprobe", "Post", map[string]interface{}{"text": "@secret:vs"}))
	createSQLWorkflow(t, e.store, approved)
	if err := e.store.ApproveDefinition(e.ctx, approved.ID); err != nil {
		t.Fatal(err)
	}
	cur, _ := e.store.GetWorkflow(e.ctx, approved.ID)
	dag, _ := BuildDAG(cur.Nodes, cur.Connections)
	exec := &WorkflowExecution{ID: "ok1", WorkflowID: approved.ID, ProfileID: "default", TriggerType: "trigger.schedule", TriggerNodeID: approved.ID + "-t"}
	if err := e.store.CreateExecution(e.ctx, exec); err != nil {
		t.Fatal(err)
	}
	if err := RunExecution(e.ctx, exec, cur, dag, e.reg, e.store, nil, NewExpressionEngine(), zerolog.Nop()); err != nil {
		t.Errorf("an operator-approved workflow was refused: %v", err)
	}
}

// The refusal comes before the exec guard (the HIL card): a refused
// publishing node is never offered to it, so an always-allow rule cannot let
// it go and no card holds the value.
func TestAgentAccessPublishRefusalIsBeforeTheExecGuard(t *testing.T) {
	e := newAccessEnv(t)
	pub := e.registerPublish()
	e.grant(t, secrets.AccessGrant{AnyHost: true})
	var offered []string
	SetExecGuard(func(_ context.Context, nodeType string, _ NodeInput, _ map[string]interface{}) ([]NodeOutput, bool, error) {
		offered = append(offered, nodeType)
		return nil, false, nil
	})
	t.Cleanup(func() { SetExecGuard(nil) })
	if _, err := e.run(t, chain("wf-card", step("b1", "test.publishprobe", "Post", map[string]interface{}{"text": "@secret:vs"})), nil); !errors.Is(err, secrets.ErrPublishesSecret) {
		t.Fatalf("want ErrPublishesSecret, got %v", err)
	}
	if len(offered) != 0 || pub.count() != 0 {
		t.Errorf("the guard saw %v and the node ran %d time(s)", offered, pub.count())
	}
}

// An executor that declares nothing publishes (fail closed).
func TestAnExecutorThatDeclaresNothingPublishes(t *testing.T) {
	if !Publishes(&publishProbe{}) {
		t.Error("an undeclared executor must count as publishing")
	}
	if Publishes(echoProbe{}) {
		t.Error("an executor declaring false must not")
	}
}

// Every dispatch point goes through RunExecGuardFor: any reference to
// RunExecGuard (a call, a method value, an alias, in any package) outside
// internal/workflow/exec_guard.go would skip the content guard; and any
// other place that calls a node's Execute(ctx, input, config) is listed with
// why it is guarded or why it is not an agent's path.
func TestNoDispatchPointReferencesRunExecGuardDirectly(t *testing.T) {
	var bad, extra []string
	executes := map[string]int{}
	walk := func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".git", "chrome-extension", "assets", "store-assets", "frontend":
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel("../..", p)
		rel = filepath.ToSlash(rel)
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(token.NewFileSet(), p, nil, 0)
		if perr != nil {
			t.Errorf("%s does not parse: %v", rel, perr)
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.SelectorExpr:
				if x.Sel.Name == "RunExecGuard" && rel != "internal/workflow/exec_guard.go" {
					bad = append(bad, rel)
				}
			case *ast.Ident:
				if x.Name == "RunExecGuard" && rel != "internal/workflow/exec_guard.go" {
					bad = append(bad, rel)
				}
			case *ast.CallExpr:
				if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Execute" && len(x.Args) == 3 {
					executes[rel]++
				}
			}
			return true
		})
		return nil
	}
	_ = filepath.WalkDir("../..", walk)
	if len(bad) > 0 {
		t.Errorf("use workflow.RunExecGuardFor, not RunExecGuard, in: %v", bad)
	}
	// Direct node Execute calls, by file, and why each is not a way around the guard.
	known := map[string]int{
		"internal/workflow/execution_retry.go":    1, // runAttempt: the else branch of RunExecGuardFor
		"internal/workflow/execution_subnodes.go": 1, // a tool call: the else branch of RunExecGuardFor
		"internal/cli/node/run.go":                2, // :387 after RunExecGuardFor; the second saves the guarded node's own scraped profiles
		"internal/cli/people/messages_compose.go": 1, // after RunExecGuardFor
		"internal/mcp/node_run.go":                1, // after RunExecGuardFor
		"internal/nodes/vector/subnodes.go":       1, // the document loader inside vector.embed / vector.store, which are publishing and guarded
		"internal/nodes/evalnode/sheets.go":       1, // the user's own `workflow evaluate` harness reading a dataset
		"internal/evaluation/dataset.go":          1, // the same harness
	}
	for file, n := range executes {
		switch {
		case known[file] == n:
		case strings.Contains(file, "/nodes/") || file == "internal/workflow/execution.go":
			extra = append(extra, file+": a node calls another node's Execute directly; guard it or list it here with why")
		default:
			extra = append(extra, file)
		}
	}
	for file, n := range known {
		if executes[file] != n {
			t.Errorf("%s: %d direct Execute call(s), the list says %d: update the list", file, executes[file], n)
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		t.Errorf("direct Execute(ctx, input, config) calls not in the list: %v", extra)
	}
}

// Review of #1418 slice 1 (P2): the taint flows back up. A child run that
// read a secret taints its parent when the parent consumes the child.
func TestAgentAccessChildRunTaintsItsParent(t *testing.T) {
	e := newAccessEnv(t)
	e.grant(t, secrets.AccessGrant{AnyHost: true})
	eng := newTestEngine(t, e.store, e.reg)
	child := chain("wf-childup", step("c1", "test.echoprobe", "Read", map[string]interface{}{"v": "@secret:vs"}))
	child.Nodes[0].Type = TriggerTypeExecuteWorkflow
	createSQLWorkflow(t, e.store, child)

	parent := secrets.AllowResolving(e.ctx, e.db.DB, "default", secrets.AccessOrigin{WorkflowID: "wf-parent", ExecutionID: "pu1"})
	parent = WithTrigger(vault.ContextWithExecIDs(parent, "wf-parent", "pu1"), "trigger.schedule", map[string]interface{}{})
	id, err := eng.StartSubWorkflow(parent, SubWorkflowStart{ParentExecutionID: "pu1", ParentNodeID: "call", Workflow: "wf-childup", ItemIndex: -1, Wait: true,
		Items: []Item{NewItem(map[string]interface{}{})}})
	if err != nil {
		t.Fatal(err)
	}
	eng.handleExecution(e.ctx, ExecutionRequest{WorkflowID: "wf-childup", ExecutionID: id, TriggerType: TriggerTypeExecuteWorkflow, TriggerNodeID: "wf-childup-t",
		TriggerData: map[string]interface{}{SubWorkflowItemsKey: []interface{}{map[string]interface{}{}}, UnapprovedCallerKey: "wf-parent"}})

	if err := secrets.CheckPublish(parent, "comm.slack", true); err != nil {
		t.Fatalf("before the parent consumes the child: %v", err)
	}
	kids, err := eng.SubWorkflowChildren(parent, "pu1", "call")
	if err != nil || len(kids) != 1 || !kids[0].Done() {
		t.Fatalf("children: %+v %v", kids, err)
	}
	if err := secrets.CheckPublish(parent, "comm.slack", true); !errors.Is(err, secrets.ErrPublishesSecret) {
		t.Errorf("a parent that consumed a child which read a secret still publishes (issue #1418): %v", err)
	}
}

// subWorkflowChain makes executions d0 (the parent's) .. dN, each the child of
// the one before, all finished, and returns the last id.
func (e *accessEnv) subWorkflowChain(t *testing.T, n int, lastStatus string) string {
	t.Helper()
	createSQLWorkflow(t, e.store, chain("wf-chain", step("c1", "test.echoprobe", "Echo", nil)))
	prev := "d0"
	for i := 1; i <= n; i++ {
		id := "d" + strconv.Itoa(i)
		status := "SUCCESS"
		if i == n {
			status = lastStatus
		}
		if err := e.store.CreateExecution(e.ctx, &WorkflowExecution{ID: id, WorkflowID: "wf-chain", ProfileID: "default", Status: status, TriggerType: TriggerTypeExecuteWorkflow}); err != nil {
			t.Fatal(err)
		}
		if _, err := e.db.DB.Exec(`INSERT INTO subworkflow_runs (parent_execution_id, parent_node_id, child_execution_id, item_index, wait) VALUES (?, 'call', ?, -1, 1)`, prev, id); err != nil {
			t.Fatal(err)
		}
		prev = id
	}
	return prev
}

func (e *accessEnv) auditRead(t *testing.T, execID string) {
	t.Helper()
	if _, err := e.db.DB.Exec(`INSERT INTO profile_agent_secret_audit (profile_id, at, workflow_id, execution_id, node_id, node_name, secret_name) VALUES ('default', '2026-01-01T00:00:00Z', 'wf-chain', ?, 'n', 'N', 'vs')`, execID); err != nil {
		t.Fatal(err)
	}
}

func parentCtxOf(e *accessEnv) context.Context {
	parent := secrets.AllowResolving(e.ctx, e.db.DB, "default", secrets.AccessOrigin{WorkflowID: "wf-parent", ExecutionID: "d0"})
	return WithTrigger(vault.ContextWithExecIDs(parent, "wf-parent", "d0"), "trigger.schedule", map[string]interface{}{})
}

// Review of #1418 delta: what a descendant at any depth read taints the top
// run (MaxSubWorkflowDepth is 32), and so does a child that failed or was
// cancelled, whose error text is returned to the parent.
func TestAgentAccessTaintReachesTheTopRunFromAnyDepth(t *testing.T) {
	for name, c := range map[string]struct {
		depth  int
		status string
	}{
		"a grandchild":             {2, "SUCCESS"},
		"a descendant at depth 10": {10, "SUCCESS"},
		"a descendant at depth 31": {31, "SUCCESS"},
		"a failed child":           {1, "FAILED"},
		"a cancelled grandchild":   {2, "CANCELLED"},
	} {
		t.Run(name, func(t *testing.T) {
			e := newAccessEnv(t)
			e.grant(t, secrets.AccessGrant{AnyHost: true})
			eng := newTestEngine(t, e.store, e.reg)
			last := e.subWorkflowChain(t, c.depth, c.status)
			e.auditRead(t, last)
			parent := parentCtxOf(e)
			if _, err := eng.SubWorkflowChildren(parent, "d0", "call"); err != nil {
				t.Fatal(err)
			}
			if err := secrets.CheckPublish(parent, "comm.slack", true); !errors.Is(err, secrets.ErrPublishesSecret) {
				t.Errorf("the top run did not learn what %s read (issue #1418): %v", name, err)
			}
		})
	}
}

// Nothing read, nothing tainted; and a child still running taints nothing yet.
func TestAgentAccessTaintFromChildrenWaitsForAllAndNeedsARead(t *testing.T) {
	e := newAccessEnv(t)
	e.grant(t, secrets.AccessGrant{AnyHost: true})
	eng := newTestEngine(t, e.store, e.reg)
	e.subWorkflowChain(t, 3, "SUCCESS")
	parent := parentCtxOf(e)
	if _, err := eng.SubWorkflowChildren(parent, "d0", "call"); err != nil {
		t.Fatal(err)
	}
	if err := secrets.CheckPublish(parent, "comm.slack", true); err != nil {
		t.Errorf("a chain that read nothing tainted the parent: %v", err)
	}
}

// Review of #1418 delta 2: a waiting poll (some child still running) issues
// no lookup of what the children read; only the poll that sees all of them
// finished does, once.
func TestAgentAccessTaintLookupWaitsForAllChildren(t *testing.T) {
	e := newAccessEnv(t)
	e.grant(t, secrets.AccessGrant{AnyHost: true})
	eng := newTestEngine(t, e.store, e.reg)
	last := e.subWorkflowChain(t, 2, "RUNNING")
	e.auditRead(t, last)
	parent := parentCtxOf(e)
	// d2 (the grandchild) is the one still running; d1 is done. A poll must not
	// look at the audit, so it does not taint yet.
	if _, err := e.db.DB.Exec(`UPDATE workflow_executions SET status = 'RUNNING' WHERE id = 'd1'`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := eng.SubWorkflowChildren(parent, "d0", "call"); err != nil {
			t.Fatal(err)
		}
		if err := secrets.CheckPublish(parent, "comm.slack", true); err != nil {
			t.Fatalf("poll %d with a child still running tainted the run: %v", i, err)
		}
	}
	if _, err := e.db.DB.Exec(`UPDATE workflow_executions SET status = 'SUCCESS' WHERE id = 'd1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.SubWorkflowChildren(parent, "d0", "call"); err != nil {
		t.Fatal(err)
	}
	if err := secrets.CheckPublish(parent, "comm.slack", true); !errors.Is(err, secrets.ErrPublishesSecret) {
		t.Errorf("the poll that sees every child finished did not taint: %v", err)
	}
}
