package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/opgate"
	taskcli "github.com/monoes/mono-agent/internal/cli/task"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/workflow"
)

// A process inside a coder turn is handled like a marked agent when it publishes a workflow: the
// save, import, restore, template, create and node-edit still happen, but the definition is NOT
// recorded as the operator's, so its program-running nodes stay refused. Only the operator's own
// terminal approves. Each row is a caller that reaches operator.ApprovingContext or the node-edit
// approving(); reverting one of them to a plain approval makes its rows red.

const execWorkflowDoc = `{"name":"Exec","nodes":[{"id":"t","type":"trigger.manual","name":"Start","config":{}},` +
	`{"id":"x","type":"system.execute_command","name":"Run","config":{"command":"true"}}],` +
	`"connections":[{"source":"t","target":"x"}]}`

type approvalFlavour struct {
	name     string
	approves bool
	inTurn   bool   // no marker, yet not approving: the not-approved note is printed
	cause    string // what the note says the cause is
	enter    func(t *testing.T, cfg *globalConfig)
}

func approvalFlavours() []approvalFlavour {
	gate := func(t *testing.T, cfg *globalConfig) { opgate.SetForTest(t, cfg.gateRefusal) }
	return []approvalFlavour{
		{name: "operator", approves: true, enter: func(t *testing.T, cfg *globalConfig) { gate(t, cfg) }},
		{name: "marked agent", enter: func(t *testing.T, cfg *globalConfig) { asAgentProcess(t); gate(t, cfg) }},
		{name: "marked agent without the hook", enter: func(t *testing.T, cfg *globalConfig) { asAgentProcess(t); opgate.SetForTest(t, nil) }},
		{name: "in a turn, markers cleared", inTurn: true, cause: "runs inside a coder session's turn", enter: func(t *testing.T, cfg *globalConfig) {
			if coderhost.ProcStamp(os.Getpid()) == "" {
				t.Skip("process ancestry cannot be read here")
			}
			markThisProcessAsACoderTurn(t, cfg)
			gate(t, cfg)
		}},
		{name: "token holder", inTurn: true, cause: "agent token", enter: func(t *testing.T, cfg *globalConfig) {
			t.Setenv(taskcli.PersonTokenEnv()[0], "x")
			gate(t, cfg)
		}},
		{name: "turn of a registered host, other --db-path", inTurn: true, cause: "runs inside a coder session's turn", enter: func(t *testing.T, cfg *globalConfig) {
			registeredTurnDB(t) // a turn in a database this command does not name
			gate(t, cfg)
		}},
		{name: "unreadable database", inTurn: true, cause: "wait and retry", enter: func(t *testing.T, cfg *globalConfig) {
			bad := filepath.Join(t.TempDir(), "bad.db")
			if err := os.WriteFile(bad, []byte("this is not a database, and it is long enough to be read as a header, or not"), 0o600); err != nil {
				t.Fatal(err)
			}
			opgate.SetForTest(t, (&globalConfig{DBPath: bad}).gateRefusal)
		}},
	}
}

func captureStderrOf(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan string, 1)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	orig := os.Stderr
	os.Stderr = w
	func() {
		defer func() { os.Stderr = orig }()
		fn()
	}()
	w.Close()
	return <-done
}

// seedAIWorkflow stores a workflow the way an agent leaves one: no approval.
func seedAIWorkflow(t *testing.T, cfg *globalConfig, id string, withConn bool) {
	t.Helper()
	db, err := initDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	wf := &workflow.Workflow{ID: id, Name: "Seed " + id, Version: 1, ProfileID: cfg.ProfileID,
		Nodes: []workflow.WorkflowNode{
			{ID: "t", Type: "trigger.manual", Name: "Start", Config: map[string]interface{}{}},
			{ID: "x", Type: "system.execute_command", Name: "Run", Config: map[string]interface{}{"command": "true"}},
		}}
	if withConn {
		wf.Connections = []workflow.WorkflowConnection{{ID: "c1", WorkflowID: id, SourceNodeID: "t", SourceHandle: "main", TargetNodeID: "x", TargetHandle: "main"}}
	}
	for i := range wf.Nodes {
		if err := wf.Nodes[i].MarshalConfig(); err != nil {
			t.Fatal(err)
		}
	}
	if err := openWorkflowStore(db).CreateWorkflow(context.Background(), wf); err != nil {
		t.Fatal(err)
	}
}

func isApprovedNow(t *testing.T, cfg *globalConfig, id string) bool {
	t.Helper()
	db, err := initDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := openWorkflowStore(db)
	wf, err := store.GetWorkflow(context.Background(), id)
	if err != nil || wf == nil {
		t.Fatalf("workflow %s: %v", id, err)
	}
	ok, err := workflow.IsApproved(context.Background(), store, wf)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

// A run of the workflow's program node, as the engine judges it: an unapproved definition is an
// unapproved run, and RefuseUnapprovedProgram is what the node asks before it starts anything.
func execNodeMayRun(t *testing.T, approved bool, id string) error {
	t.Helper()
	ctx := context.Background()
	if !approved {
		ctx = workflow.WithUnapproved(ctx, t.TempDir(), "approve it")
	}
	return workflow.RefuseUnapprovedProgram(ctx, "system.execute_command", id)
}

type approvalCaller struct {
	name string
	// run returns the id of the workflow the caller published, what it printed, and its error.
	run func(t *testing.T, cfg *globalConfig) (id string, out string, err error)
	// blocksWhenNotApproving: the caller refuses outright when it cannot approve (an activation).
	refuses bool
}

func cliOf(t *testing.T, cfg *globalConfig, args ...string) (string, error) {
	t.Helper()
	cmd := newWorkflowCmd(cfg)
	cmd.SetArgs(args)
	var err error
	out := captureStdout(t, func() { err = cmd.Execute() })
	return out, err
}

func idOf(t *testing.T, out string, keys ...string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("not JSON: %q: %v", out, err)
	}
	for _, k := range keys {
		if s, _ := m[k].(string); s != "" {
			return s
		}
	}
	t.Fatalf("no id in %q", out)
	return ""
}

func writeDoc(t *testing.T) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "wf.json")
	if err := os.WriteFile(f, []byte(execWorkflowDoc), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

func approvalCallers() []approvalCaller {
	return []approvalCaller{
		{name: "activate", refuses: true, run: func(t *testing.T, cfg *globalConfig) (string, string, error) {
			seedAIWorkflow(t, cfg, "wf-act", false)
			out, err := cliOf(t, cfg, "activate", "wf-act")
			return "wf-act", out, err
		}},
		{name: "save", run: func(t *testing.T, cfg *globalConfig) (string, string, error) {
			out, err := cliOf(t, cfg, "save", "--file", writeDoc(t))
			if err != nil {
				return "", out, err
			}
			return idOf(t, out, "id"), out, nil
		}},
		{name: "import", run: func(t *testing.T, cfg *globalConfig) (string, string, error) {
			out, err := cliOf(t, cfg, "import", "--file", writeDoc(t))
			if err != nil {
				return "", out, err
			}
			return idOf(t, out, "id"), out, nil
		}},
		{name: "versions restore", run: func(t *testing.T, cfg *globalConfig) (string, string, error) {
			seedAIWorkflow(t, cfg, "wf-ver", false)
			if _, err := cliOf(t, cfg, "node", "add", "wf-ver", "--type", "core.set", "--name", "More"); err != nil {
				t.Fatal(err)
			}
			out, err := cliOf(t, cfg, "versions", "restore", "wf-ver", "1")
			return "wf-ver", out, err
		}},
		{name: "templates use", run: func(t *testing.T, cfg *globalConfig) (string, string, error) {
			out, err := cliOf(t, cfg, "templates", "use", "hello")
			if err != nil {
				return "", out, err
			}
			return idOf(t, out, "id"), out, nil
		}},
		{name: "create", run: func(t *testing.T, cfg *globalConfig) (string, string, error) {
			out, err := cliOf(t, cfg, "create", "blank")
			if err != nil {
				return "", out, err
			}
			return idOf(t, out, "id"), out, nil
		}},
		{name: "node add", run: func(t *testing.T, cfg *globalConfig) (string, string, error) {
			seedAIWorkflow(t, cfg, "wf-add", false)
			out, err := cliOf(t, cfg, "node", "add", "wf-add", "--type", "system.execute_command", "--name", "Again", "--config", `{"command":"true"}`)
			return "wf-add", out, err
		}},
		{name: "node set", run: func(t *testing.T, cfg *globalConfig) (string, string, error) {
			seedAIWorkflow(t, cfg, "wf-set", false)
			out, err := cliOf(t, cfg, "node", "set", "wf-set", "x", "--config", `{"command":"false"}`)
			return "wf-set", out, err
		}},
		{name: "node remove", run: func(t *testing.T, cfg *globalConfig) (string, string, error) {
			seedAIWorkflow(t, cfg, "wf-rm", false)
			out, err := cliOf(t, cfg, "node", "remove", "wf-rm", "x")
			return "wf-rm", out, err
		}},
		{name: "templates run", refuses: true, run: func(t *testing.T, cfg *globalConfig) (string, string, error) {
			// The sample template fetches a public page: no route out, so it fails fast and nothing leaves.
			for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
				t.Setenv(k, "http://127.0.0.1:9")
			}
			out, err := cliOf(t, cfg, "templates", "run", "hello", "--keep")
			id := ""
			var m map[string]any
			if json.Unmarshal([]byte(out), &m) == nil {
				id, _ = m["workflow_id"].(string)
			}
			if id == "" {
				id = findOnlyWorkflow(t, cfg)
			}
			return id, out, err
		}},
	}
}

func findOnlyWorkflow(t *testing.T, cfg *globalConfig) string {
	t.Helper()
	db, err := initDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var id string
	if err := db.DB.QueryRow(`SELECT id FROM workflows ORDER BY created_at DESC LIMIT 1`).Scan(&id); err != nil {
		t.Fatalf("no workflow was left behind: %v", err)
	}
	return id
}

func TestOnlyTheOperatorsOwnTerminalApprovesWhatItPublishes(t *testing.T) {
	for _, c := range approvalCallers() {
		for _, f := range approvalFlavours() {
			t.Run(c.name+"/"+f.name, func(t *testing.T) {
				_, cfg := persistTestEnv(t)
				asOperatorProcess(t)
				coderhost.SetRegistryDirForTest(t, filepath.Join(t.TempDir(), "coder-hosts"))
				seedMigratedDB(t, cfg.DBPath)
				f.enter(t, cfg)

				var id, out string
				var err error
				stderr := captureStderrOf(t, func() { id, out, err = c.run(t, cfg) })

				if c.refuses && !f.approves {
					if err == nil || !strings.Contains(err.Error(), "not approved by you") && !errors.Is(err, workflow.ErrApprovalRequired) &&
						!strings.Contains(err.Error(), "operator's approval") {
						t.Fatalf("%s by a caller that does not approve went through: %v\n%s", c.name, err, out)
					}
				} else if c.name == "templates run" {
					// the run itself may fail (no network); it must not be refused for approval
					if err != nil && strings.Contains(err.Error(), "operator's approval") {
						t.Fatalf("the operator's template run was refused for approval: %v", err)
					}
				} else if err != nil {
					t.Fatalf("%s: %v\n%s", c.name, err, out)
				}
				if id == "" {
					t.Fatal("no workflow to look at")
				}
				approved := isApprovedNow(t, cfg, id)
				if approved != f.approves {
					t.Fatalf("approved = %v, want %v (output: %s)", approved, f.approves, out)
				}
				// the end result through the engine's own question
				ran := execNodeMayRun(t, approved, id)
				if f.approves && ran != nil {
					t.Fatalf("an operator-approved workflow's program node is refused: %v", ran)
				}
				if !f.approves && !errors.Is(ran, workflow.ErrUnapprovedProgram) {
					t.Fatalf("an unapproved workflow's program node is not refused: %v", ran)
				}
				// not silent for the in-turn case, byte-identical for the others
				noted := strings.Contains(stderr, "NOT approved")
				if noted != (f.inTurn && !c.refuses) {
					t.Fatalf("note on stderr = %v, in-turn = %v, stderr: %q", noted, f.inTurn, stderr)
				}
				if f.inTurn {
					want := "NOT approved"
					if c.refuses {
						want = "cannot approve a workflow"
					}
					if c.name != "node disconnect" && (!strings.Contains(stderr, want) || !strings.Contains(stderr, f.cause)) {
						t.Fatalf("stderr does not say %q with the cause %q: %q", want, f.cause, stderr)
					}
				}
				if f.inTurn && !c.refuses {
					var m map[string]any
					if err := json.Unmarshal([]byte(out), &m); err != nil || m["approved"] != false || m["approval_note"] == nil {
						t.Fatalf("the JSON result of an in-turn caller does not say it is not approved: %s", out)
					}
				}
				if !f.inTurn && strings.Contains(out, "approval_note") {
					t.Fatalf("a caller that is not in a turn got an approval note: %s", out)
				}
			})
		}
	}
}

// The wiring of the real command tree: registering the gate (what buildRootCmd does outside tests)
// also installs the check ApprovingContext consults, and without it a process judges by marker alone.
func TestRegisteringTheGateInstallsTheApprovalHook(t *testing.T) {
	persistTestEnv(t)
	asOperatorProcess(t)
	opgate.SetForTest(t, nil) // restores whatever the build registers after the test
	t.Setenv(taskcli.PersonTokenEnv()[0], "x")
	if operator.NotApproving() {
		t.Fatal("with no gate registered a process is judged by marker alone")
	}
	prev := inTestBinary
	inTestBinary = func() bool { return false } // what the production binary answers
	t.Cleanup(func() { inTestBinary = prev })
	newRootCmd() // buildRootCmd(true): the tree main runs
	if !operator.NotApproving() {
		t.Fatal("building the real command tree did not install the approval hook: a token holder approves")
	}
}

// Only two places turn the operator's approval on: operator.ApprovingContext and the node edit's
// approving(), both behind operator.NotApproving. MCP, the HTTP API, the app and the daemon never
// approve: a new caller of workflow.WithApproval anywhere else fails here until it is routed through them.
func TestOnlyTheOperatorHelpersTurnTheApprovalOn(t *testing.T) {
	allowed := map[string]bool{
		"internal/operator/operator.go":               true,
		"internal/cli/workflowedit/node.go":           true, // approving()
		"internal/cli/workflowedit/save.go":           true,
		"internal/cli/workflowio/import.go":           true,
		"internal/cli/workflowmanage/manage.go":       true,
		"internal/cli/workflowmanage/versions.go":     true,
		"internal/cli/workflowtemplates/templates.go": true,
		"internal/cli/workflowrun/run.go":             true,
		"internal/workflow/publish.go":                true, // the definition
	}
	root := filepath.Join("..", "..")
	for _, base := range []string{"cmd", "internal", "wails-app"} {
		err := filepath.WalkDir(filepath.Join(root, base), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			b, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if (strings.Contains(string(b), "WithApproval(") || strings.Contains(string(b), "ApprovingContext(")) && !allowed[rel] {
				t.Errorf("%s turns the operator's approval on; route it through operator.ApprovingContext", rel)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// Nothing written, nothing to say: an import of a definition that is already there and unchanged prints
// no "imported but NOT approved" note and gains no approval_note.
func TestImportOfAnUnchangedDefinitionSaysNothingAboutApproval(t *testing.T) {
	if coderhost.ProcStamp(os.Getpid()) == "" {
		t.Skip("process ancestry cannot be read here")
	}
	_, cfg := persistTestEnv(t)
	asOperatorProcess(t)
	seedMigratedDB(t, cfg.DBPath)
	file := writeDoc(t)
	opgate.SetForTest(t, cfg.gateRefusal)
	if _, err := cliOf(t, cfg, "import", "--file", file); err != nil {
		t.Fatal(err)
	}
	markThisProcessAsACoderTurn(t, cfg)
	var out string
	var err error
	stderr := captureStderrOf(t, func() { out, err = cliOf(t, cfg, "import", "--file", file) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "unchanged") || strings.Contains(stderr, "NOT approved") || strings.Contains(out, "approval_note") {
		t.Fatalf("an unchanged import talks about approval: out %s stderr %q", out, stderr)
	}
}

// The node edits ask once: a verdict that flips between two asks (a transient lock) must not leave the
// approval and the note disagreeing. The hook is installed straight on operator, so each edit asks it
// directly; re-asking in approving() makes this red.
func TestNodeEditsAskTheVerdictOnce(t *testing.T) {
	edits := map[string][]string{
		"node add":    {"node", "add", "wf-ff", "--type", "core.set", "--name", "More"},
		"node set":    {"node", "set", "wf-ff", "x", "--config", `{"command":"false"}`},
		"node remove": {"node", "remove", "wf-ff", "x"},
	}
	for name, args := range edits {
		for _, order := range [][]string{{"", "in a turn"}, {"in a turn", ""}} {
			t.Run(name+"/"+order[0]+"|"+order[1], func(t *testing.T) {
				_, cfg := persistTestEnv(t)
				asOperatorProcess(t)
				seedMigratedDB(t, cfg.DBPath)
				opgate.SetForTest(t, nil)
				seedAIWorkflow(t, cfg, "wf-ff", false)
				calls := 0
				operator.SetTurnVerdict(func() string { a := order[min(calls, 1)]; calls++; return a })
				t.Cleanup(func() { operator.SetTurnVerdict(nil) })
				var err error
				stderr := captureStderrOf(t, func() { _, err = cliOf(t, cfg, args...) })
				operator.SetTurnVerdict(nil)
				if err != nil {
					t.Fatal(err)
				}
				approved, noted := isApprovedNow(t, cfg, "wf-ff"), strings.Contains(stderr, "NOT approved")
				if approved == noted || approved != (order[0] == "") {
					t.Fatalf("approved = %v, note = %v (first answer %q): %q", approved, noted, order[0], stderr)
				}
			})
		}
	}
}
