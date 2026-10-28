package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// `workflow get` prints the review_hash that `workflow activate --expect` takes: the operator
// approves exactly what they looked at. A workflow that changed since is refused, and nothing is
// activated or approved; the hash that was read goes through and records the approval with the flag.
func TestActivateExpectApprovesExactlyWhatWasReviewed(t *testing.T) {
	_, cfg := persistTestEnv(t)
	asOperatorProcess(t)
	// The chat builds it, so nothing is approved yet (the operator's own edits would approve each step).
	t.Setenv(browserctl.EnvAICaller, "chat")
	var created workflow.Workflow
	if err := json.Unmarshal([]byte(runWorkflowSubcmd(t, cfg, "create", "reviewed")), &created); err != nil {
		t.Fatal(err)
	}
	runWorkflowSubcmd(t, cfg, "node", "add", created.ID, "--type", "trigger.manual", "--name", "Start")
	t.Setenv(browserctl.EnvAICaller, "")

	reviewHash := func() string {
		t.Helper()
		var got struct {
			ReviewHash string `json:"review_hash"`
			IsActive   bool   `json:"is_active"`
		}
		if err := json.Unmarshal([]byte(runWorkflowSubcmd(t, cfg, "get", created.ID)), &got); err != nil || got.ReviewHash == "" {
			t.Fatalf("workflow get prints no review_hash: %v", err)
		}
		return got.ReviewHash
	}
	seen := reviewHash()
	if again := reviewHash(); again != seen {
		t.Fatalf("the review_hash of an unchanged workflow moved: %s then %s", seen, again)
	}

	// The chat changes it after the operator looked.
	t.Setenv(browserctl.EnvAICaller, "chat")
	runWorkflowSubcmd(t, cfg, "node", "add", created.ID, "--type", "core.set", "--name", "Added later")
	t.Setenv(browserctl.EnvAICaller, "")
	now := reviewHash()
	if now == seen {
		t.Fatal("a changed workflow kept its review_hash")
	}

	db, err := storage.NewDatabase(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	state := func() (active, approvals int) {
		t.Helper()
		if err := db.DB.QueryRow(`SELECT is_active FROM workflows WHERE id = ?`, created.ID).Scan(&active); err != nil {
			t.Fatal(err)
		}
		if err := db.DB.QueryRow(`SELECT COUNT(*) FROM workflow_approvals WHERE workflow_id = ?`, created.ID).Scan(&approvals); err != nil {
			t.Fatal(err)
		}
		return
	}

	stale := newWorkflowCmd(cfg)
	stale.SetArgs([]string{"activate", created.ID, "--expect", seen})
	var err2 error
	captureStdout(t, func() { err2 = stale.Execute() })
	if err2 == nil || !strings.Contains(err2.Error(), "changed since") || !strings.Contains(err2.Error(), "review_hash") {
		t.Fatalf("--expect naming an older definition: %v", err2)
	}
	if active, approvals := state(); active != 0 || approvals != 0 {
		t.Fatalf("a refused activation left active=%d approvals=%d", active, approvals)
	}

	runWorkflowSubcmd(t, cfg, "activate", created.ID, "--expect", now)
	if active, approvals := state(); active != 1 || approvals != 1 {
		t.Fatalf("the reviewed hash: active=%d approvals=%d, want the flag and the approval together", active, approvals)
	}
}
