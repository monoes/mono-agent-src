package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai/chat"
	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/storage"
)

// A workflow the chat builds is not the operator's: no command records it
// as theirs, an activation from the chat's run neither, and the operator's
// own `workflow activate` does — the review that makes its runs the
// operator's (#831 review C; the engine side is internal/workflow's
// TestRunsOfDefinitionsTheOperatorNeverApprovedAreAIRuns).
func TestAChatBuiltWorkflowIsTheOperatorsOnlyOnceTheyActivateIt(t *testing.T) {
	_, cfg := persistTestEnv(t)
	asOperatorProcess(t)
	db, err := storage.NewDatabase(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	tools := chat.NewMonoagentTools(db.DB, "", "default")
	out, err := tools.Execute("create_workflow", `{"name":"From the chat"}`)
	if err != nil {
		t.Fatal(err)
	}
	var created struct {
		ID string `json:"workflow_id"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil || created.ID == "" {
		t.Fatalf("create_workflow: %s %v", out, err)
	}
	if _, err := tools.Execute("add_workflow_node", `{"workflow_id":"`+created.ID+`","node_type":"trigger.manual","name":"Start"}`); err != nil {
		t.Fatal(err)
	}
	approvals := func() int {
		t.Helper()
		var n int
		if err := db.DB.QueryRow(`SELECT COUNT(*) FROM workflow_approvals WHERE workflow_id = ?`, created.ID).Scan(&n); err != nil && err != sql.ErrNoRows {
			t.Fatal(err)
		}
		return n
	}
	if n := approvals(); n != 0 {
		t.Fatalf("the chat's workflow has %d approvals", n)
	}
	// The chat's run cannot activate it: activating arms its triggers, and nobody approved the
	// definition. Nothing is switched on or approved.
	t.Setenv(browserctl.EnvAICaller, "chat")
	chatCmd := newWorkflowCmd(cfg)
	chatCmd.SetArgs([]string{"activate", created.ID})
	var activateErr error
	captureStdout(t, func() { activateErr = chatCmd.Execute() })
	t.Setenv(browserctl.EnvAICaller, "")
	if activateErr == nil || !strings.Contains(activateErr.Error(), "not approved by you") {
		t.Fatalf("an activation from the chat's run: %v", activateErr)
	}
	if n := approvals(); n != 0 {
		t.Fatalf("an activation from the chat's run approved it (%d)", n)
	}
	var active int
	if err := db.DB.QueryRow(`SELECT is_active FROM workflows WHERE id = ?`, created.ID).Scan(&active); err != nil || active != 0 {
		t.Fatalf("a refused activation switched the workflow on: %d %v", active, err)
	}
	runWorkflowSubcmd(t, cfg, "activate", created.ID)
	if n := approvals(); n != 1 {
		t.Fatalf("the operator's activation recorded %d approvals, want 1", n)
	}
}

// activateUnapprovedForTest leaves the workflow active and unapproved, the state it is in when the
// operator once activated it and the chat or an agent has changed it since: the chat's own run
// cannot activate an unapproved definition, so the state is built as it arises in life. The
// operator activates it (approval and flag together), then the approval goes, as an agent's edit
// would remove it. The caller's agent marker is put back.
func activateUnapprovedForTest(t *testing.T, cfg *globalConfig, id string) {
	t.Helper()
	caller := os.Getenv(browserctl.EnvAICaller)
	t.Setenv(browserctl.EnvAICaller, "")
	runWorkflowSubcmd(t, cfg, "activate", id)
	t.Setenv(browserctl.EnvAICaller, caller)
	db, err := storage.NewDatabase(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.DB.Exec(`DELETE FROM workflow_approvals WHERE workflow_id = ?`, id); err != nil {
		t.Fatal(err)
	}
}

// `workflow validate` takes publication.register's approval, an expression
// per item, as a field of the node (#831).
func TestWorkflowValidateKnowsTheRegisterApproval(t *testing.T) {
	_, cfg := persistTestEnv(t)
	file := filepath.Join(t.TempDir(), "wf.json")
	doc := `{"name":"Publish","nodes":[{"id":"t","type":"trigger.manual","name":"Start"},` +
		`{"id":"r","type":"publication.register","name":"Register","config":{"platform":"x","kind":"post","body":"hi","approval":"{{ $json.hil_id }}"}}],` +
		`"connections":[{"source":"t","target":"r"}]}`
	if err := os.WriteFile(file, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	var rep struct {
		Valid  bool `json:"valid"`
		Issues []struct {
			Field, Code string
		} `json:"issues"`
	}
	out := runWorkflowSubcmd(t, cfg, "validate", "--file", file)
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("validate --json: %v\n%s", err, out)
	}
	if !rep.Valid {
		t.Errorf("invalid: %s", out)
	}
	for _, is := range rep.Issues {
		if is.Field == "approval" && is.Code == "unknown_field" {
			t.Errorf("approval is not a field of publication.register: %s", out)
		}
	}
}
