package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/publication"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// A workflow the chat built calls one the user approved, which registers a
// publication: the call is the chat's doing, so the approved child's register
// needs an approved send, as the chat running it directly would. Once the
// user approves the caller too, the same call registers as before (#831
// round 5, F2). The refusal names the caller as the workflow to approve.
func TestAnUnapprovedWorkflowsSubWorkflowIsAnAIRun(t *testing.T) {
	_, cfg := persistTestEnv(t)
	asOperatorProcess(t)
	build := func(name string, nodes ...[2]string) string {
		t.Helper()
		var wf workflow.Workflow
		if err := json.Unmarshal([]byte(runWorkflowSubcmd(t, cfg, "create", name)), &wf); err != nil {
			t.Fatal(err)
		}
		prev := ""
		for _, n := range nodes {
			var node workflow.WorkflowNode
			if err := json.Unmarshal([]byte(runWorkflowSubcmd(t, cfg, "node", "add", wf.ID, "--type", n[0], "--name", n[0], "--config", n[1])), &node); err != nil {
				t.Fatal(err)
			}
			if prev != "" {
				runWorkflowSubcmd(t, cfg, "connect", wf.ID, "--from", prev, "--to", node.ID)
			}
			prev = node.ID
		}
		return wf.ID
	}
	child := build("registers", [2]string{workflow.TriggerTypeExecuteWorkflow, `{}`},
		[2]string{"publication.register", `{"platform":"email","kind":"post","body":"hello"}`})
	t.Setenv(browserctl.EnvAICaller, "chat") // the chat builds the caller
	parent := build("calls", [2]string{"trigger.manual", `{}`},
		[2]string{"core.execute_workflow", `{"workflowId":"` + child + `"}`})
	activateUnapprovedForTest(t, cfg, parent)
	t.Setenv(browserctl.EnvAICaller, "")

	published := func() int {
		t.Helper()
		db, err := storage.NewDatabase(cfg.DBPath)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		entries, err := publication.NewStore(db.DB, "default").List(context.Background(), publication.Filter{})
		if err != nil {
			t.Fatal(err)
		}
		return len(entries)
	}
	cfg.JSONOutput = false
	_, out, _ := runLogsFor(t, cfg, parent)
	if n := published(); n != 0 {
		t.Fatalf("a workflow the chat built had an approved one register %d publication(s) with no approved send:\n%s", n, out)
	}
	if !strings.Contains(out, "needs an approved send") {
		t.Errorf("the called workflow's register was not refused for want of an approved send:\n%s", out)
	}
	// It names the caller as the workflow to approve (#831 round 6).
	if !strings.Contains(out, "the workflow that called this one, "+parent+",") ||
		!strings.Contains(out, "`monoagentcli --profile default workflow activate "+parent+"`") {
		t.Errorf("the refusal does not say to approve the calling workflow %s:\n%s", parent, out)
	}

	cfg.JSONOutput = true
	runWorkflowSubcmd(t, cfg, "activate", parent) // the user approves the caller
	cfg.JSONOutput = false
	if _, out, err := runLogsFor(t, cfg, parent); err != nil {
		t.Fatalf("the approved caller: %v\n%s", err, out)
	}
	if n := published(); n != 1 {
		t.Fatalf("the user's approved workflows registered %d publication(s), want 1", n)
	}
}
