package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/monoes/mono-agent/internal/aitask"
	"github.com/monoes/mono-agent/internal/cli/clicore"
)

// Who may run each `aitask` command, and what each flag is. A new command or
// flag fails TestEveryAitaskFlagIsClassified until it is listed here, which
// is the moment to decide whether only the operator may use it.
//
//	operator: an agent, the chat or a workflow is refused (exit 3, operator_only), even with a task's token
//	either:   the operator, or the task's own session for its own task
//	holder:   only the holder of the task's token (bad_token otherwise), the operator included
var aitaskPolicy = map[string]struct {
	who   string
	flags map[string]string
	args  []string
}{
	"submit": {"operator", map[string]string{
		"kind": "routing", "goal": "input", "context": "input", "application": "input",
		"access": "routing", "runtime": "routing", "unattended": "routing", "queue-only": "routing", "budget-usd": "routing"}, []string{"--goal", "x"}},
	"submit-page": {"operator", map[string]string{
		"kind": "routing", "browser": "input", "tab": "input", "stdin-json": "input"}, []string{"--kind", "extension-fill", "--tab", "7", "--stdin-json"}},
	"start":     {"operator", map[string]string{}, []string{"{other}"}},
	"wait":      {"operator", map[string]string{"timeout": "input"}, []string{"{other}", "--timeout", "1s"}},
	"supervise": {"operator", map[string]string{}, []string{"{other}"}},
	"status":    {"either", map[string]string{}, []string{"{id}"}},
	"result":    {"either", map[string]string{}, []string{"{id}"}},
	"cancel":    {"either", map[string]string{}, []string{"{id}"}},
	"list":      {"either", map[string]string{"kind": "filter", "application": "filter"}, nil},
	"complete":  {"holder", map[string]string{"result": "input", "result-json": "input"}, []string{"{id}", "--result", "{file}"}},
	// "nested": has subcommands of its own, classified in aitask_budget_flags_test.go.
	"budget": {"nested", map[string]string{}, nil},
	// "anyone": read-only, opens nothing and asks for no operator (it lists what can be asked for).
	"kinds": {"anyone", map[string]string{}, nil},
	"fail":  {"holder", map[string]string{"reason": "input"}, []string{"{id}", "--reason", "cannot"}},
}

func TestEveryAitaskFlagIsClassified(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range newAitaskCmd(sendEnv(t)).Commands() {
		name := c.Name()
		seen[name] = true
		p, ok := aitaskPolicy[name]
		if !ok {
			t.Errorf("aitask %s is not classified: decide who may run it, then add it to aitaskPolicy", name)
			continue
		}
		c.Flags().VisitAll(func(f *pflag.Flag) {
			if f.Name == "help" {
				return
			}
			kind, ok := p.flags[f.Name]
			if !ok {
				t.Errorf("aitask %s --%s is not classified: decide whether only the operator may use it", name, f.Name)
			}
			// Anything that raises what a task may do or changes where it runs is the operator's.
			if kind == "routing" && p.who != "operator" {
				t.Errorf("aitask %s --%s changes routing or access but the command is not operator-only", name, f.Name)
			}
		})
		for fname := range p.flags {
			if c.Flags().Lookup(fname) == nil {
				t.Errorf("aitaskPolicy lists aitask %s --%s, which does not exist", name, fname)
			}
		}
	}
	for name := range aitaskPolicy {
		if !seen[name] {
			t.Errorf("aitaskPolicy lists aitask %s, which does not exist", name)
		}
	}
}

type aitaskRig struct {
	cfg        *globalConfig
	own, other string
	ownToken   string
	otherToken string
	resultFile string
}

func newAitaskRig(t *testing.T) *aitaskRig {
	t.Helper()
	cfg := sendEnv(t)
	db, err := initDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := &aitask.Store{DB: db.DB, Profile: cfg.ProfileID}
	r := &aitaskRig{cfg: cfg, ownToken: aitask.NewToken(), otherToken: aitask.NewToken()}
	mk := func(token string) string {
		task, err := store.Insert(aitask.Task{ID: aitask.NewID(), Kind: "generic", ConversationID: "c", Status: aitask.StatusRunning, Goal: "g"})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SetToken(task.ID, token); err != nil {
			t.Fatal(err)
		}
		return task.ID
	}
	r.own, r.other = mk(r.ownToken), mk(r.otherToken)
	r.resultFile = filepath.Join(t.TempDir(), "result.json")
	if err := os.WriteFile(r.resultFile, []byte(`{"summary":"done"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *aitaskRig) run(t *testing.T, name string, extra ...string) (string, error) {
	t.Helper()
	p := aitaskPolicy[name]
	args := []string{name}
	for _, a := range append(append([]string{}, p.args...), extra...) {
		a = strings.NewReplacer("{id}", r.own, "{other}", r.other, "{file}", r.resultFile).Replace(a)
		args = append(args, a)
	}
	return runSendCLI(t, r.cfg, newAitaskCmd(r.cfg), args...)
}

func errCode(err error) string {
	var coded interface{ JSONErrorFields() map[string]any }
	if errors.As(err, &coded) {
		c, _ := coded.JSONErrorFields()["code"].(string)
		return c
	}
	return ""
}

// asPlainOperator and asPlainAgent also drop a task session left by an earlier step.
func asPlainOperator(t *testing.T) {
	t.Helper()
	asOperatorProcess(t)
	t.Setenv(aitask.EnvTaskID, "")
	t.Setenv(aitask.EnvToken, "")
}

func asPlainAgent(t *testing.T) {
	t.Helper()
	asAgentProcess(t)
	t.Setenv(aitask.EnvTaskID, "")
	t.Setenv(aitask.EnvToken, "")
}

func asTaskSession(t *testing.T, id, token string) {
	t.Helper()
	asAgentProcess(t)
	t.Setenv(aitask.EnvTaskID, id)
	t.Setenv(aitask.EnvToken, token)
}

func TestAitaskOperatorCommandsRefuseEveryAgentEvenATaskSession(t *testing.T) {
	r := newAitaskRig(t)
	for name, p := range aitaskPolicy {
		if p.who != "operator" {
			continue
		}
		asPlainAgent(t)
		if _, err := r.run(t, name); clicore.ExitCodeFor(err) != 3 || errCode(err) != "operator_only" {
			t.Errorf("an agent ran aitask %s: %v", name, err)
		}
		asTaskSession(t, r.own, r.ownToken)
		if _, err := r.run(t, name); clicore.ExitCodeFor(err) != 3 || errCode(err) != "operator_only" {
			t.Errorf("a task's own session ran aitask %s: %v", name, err)
		}
	}
}

func TestAitaskASessionReachesOnlyItsOwnTask(t *testing.T) {
	r := newAitaskRig(t)
	for name, p := range aitaskPolicy {
		if p.who != "either" || name == "list" {
			continue
		}
		asPlainAgent(t)
		if _, err := r.run(t, name); errCode(err) != "operator_only" {
			t.Errorf("an agent with no token ran aitask %s: %v", name, err)
		}
		asTaskSession(t, r.other, r.otherToken) // another task's session, naming this task
		if _, err := r.run(t, name); errCode(err) != "operator_only" {
			t.Errorf("another task's session ran aitask %s on this task: %v", name, err)
		}
		asTaskSession(t, r.own, "not-the-token")
		if _, err := r.run(t, name); errCode(err) != "operator_only" {
			t.Errorf("a wrong token ran aitask %s: %v", name, err)
		}
	}
	asTaskSession(t, r.own, r.ownToken)
	if out, err := r.run(t, "status"); err != nil || !strings.Contains(out, r.own) {
		t.Fatalf("own status: %v %s", err, out)
	}
	// list: nothing for a session with no token; only its own task for one with it.
	asPlainAgent(t)
	if _, err := r.run(t, "list"); errCode(err) != "operator_only" {
		t.Fatalf("an agent listed tasks: %v", err)
	}
	asTaskSession(t, r.own, r.ownToken)
	out, err := r.run(t, "list")
	var doc struct{ Tasks []struct{ TaskID string } }
	if err != nil || json.Unmarshal([]byte(out), &doc) != nil || len(doc.Tasks) != 1 || doc.Tasks[0].TaskID != r.own {
		t.Fatalf("a task's session listed %s (%v)", out, err)
	}
	if _, err := r.run(t, "cancel"); err != nil {
		t.Fatalf("a session cancelling its own task: %v", err)
	}
	asPlainOperator(t)
	if out, err := r.run(t, "list"); err != nil || strings.Count(out, `"taskId"`) != 2 {
		t.Fatalf("the operator lists both: %v %s", err, out)
	}
}

func TestAitaskCompleteAndFailNeedTheTaskToken(t *testing.T) {
	r := newAitaskRig(t)
	for _, name := range []string{"complete", "fail"} {
		asPlainOperator(t)
		if _, err := r.run(t, name); clicore.ExitCodeFor(err) != 3 || errCode(err) != aitask.CodeBadToken {
			t.Errorf("the operator ran aitask %s without the token: %v", name, err)
		}
		asTaskSession(t, r.own, r.otherToken)
		if _, err := r.run(t, name); errCode(err) != aitask.CodeBadToken {
			t.Errorf("another task's token ran aitask %s: %v", name, err)
		}
	}
	asTaskSession(t, r.own, r.ownToken)
	if err := os.WriteFile(r.resultFile, []byte(`{"output":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.run(t, "complete"); clicore.ExitCodeFor(err) != 3 || errCode(err) != aitask.CodeSchemaInvalid ||
		!strings.Contains(err.Error(), `missing required property "summary"`) {
		t.Fatalf("a result that does not match the schema: %v", err)
	}
	if err := os.WriteFile(r.resultFile, []byte(`{"summary":"done"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := r.run(t, "complete")
	if err != nil || !strings.Contains(out, `"status": "complete"`) && !strings.Contains(out, `"status":"complete"`) {
		t.Fatalf("complete: %v %s", err, out)
	}
}

// --unattended with a full-access level is refused by the command itself,
// before anything is created.
func TestAitaskSubmitUnattendedFullAccessIsRefused(t *testing.T) {
	r := newAitaskRig(t)
	for _, access := range []string{"coding", "workspace"} {
		_, err := runSendCLI(t, r.cfg, newAitaskCmd(r.cfg), "submit", "--goal", "x", "--unattended", "--access", access)
		if clicore.ExitCodeFor(err) != 3 || errCode(err) != aitask.CodeUnattendedFull {
			t.Errorf("--unattended --access %s: %v", access, err)
		}
	}
}

// An agent may list the kinds; nothing is written (no task rows appear).
func TestAitaskKindsIsOpenToAnAgentAndWritesNothing(t *testing.T) {
	r := newAitaskRig(t)
	asPlainAgent(t)
	out, err := r.run(t, "kinds")
	if err != nil || !strings.Contains(out, `"workflow-generate"`) || !strings.Contains(out, `"application-apply"`) {
		t.Fatalf("%v %s", err, out)
	}
	var v struct{ Kinds []map[string]any }
	if err := json.Unmarshal([]byte(out), &v); err != nil || len(v.Kinds) < 5 {
		t.Fatalf("%v %s", err, out)
	}
	db, err := initDB(r.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM ai_tasks`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("tasks = %d (%v), want the 2 the rig made", n, err)
	}
}
