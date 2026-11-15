package workflowmanage

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/account/accounttest"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// seedScheduled saves a workflow with the given trigger node type and activates it.
func seedScheduled(t *testing.T, cfg *testConfig, id, triggerType string, active bool) {
	t.Helper()
	db, err := storage.NewDatabase(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	node := workflow.WorkflowNode{ID: id + "-t", Type: triggerType, Name: "T", Config: map[string]interface{}{"cron": "0 0 9 * * *"}}
	if err := node.MarshalConfig(); err != nil {
		t.Fatal(err)
	}
	wf := &workflow.Workflow{ID: id, Name: id, Version: 1, ProfileID: "default", Nodes: []workflow.WorkflowNode{node}}
	if err := testDeps(cfg).CreateWorkflow(context.Background(), sqliteStore(db), wf, false); err != nil {
		t.Fatal(err)
	}
	if active {
		if _, err := db.DB.Exec(`UPDATE workflows SET is_active = 1 WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
	}
}

func listPaused(t *testing.T, cfg *testConfig) map[string]bool {
	t.Helper()
	out, err := run(t, cfg, "list")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		ID              string `json:"id"`
		SchedulesPaused *bool  `json:"schedules_paused"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("list printed %q: %v", out, err)
	}
	got := map[string]bool{}
	for _, r := range rows {
		if r.SchedulesPaused == nil {
			t.Fatalf("row %s has no schedules_paused field: %s", r.ID, out)
		}
		got[r.ID] = *r.SchedulesPaused
	}
	return got
}

func TestWorkflowListMarksPausedSchedules(t *testing.T) {
	cfg := newTestDB(t, "")
	seedScheduled(t, cfg, "sched", "trigger.schedule", true)
	seedScheduled(t, cfg, "off", "trigger.schedule", false)
	seedScheduled(t, cfg, "manual", "trigger.manual", true)

	signedIn := accounttest.Install(t, accounttest.SignedIn)
	account.InstallForTest(t, signedIn)
	if got := listPaused(t, cfg); got["sched"] || got["off"] || got["manual"] {
		t.Fatalf("signed in: %v; want nothing paused", got)
	}

	account.InstallForTest(t, nil) // strict, no guard: the sign-in is missing
	got := listPaused(t, cfg)
	if !got["sched"] || got["off"] || got["manual"] {
		t.Fatalf("signed out: %v; want only the active scheduled workflow paused", got)
	}
}

func TestWorkflowListTextNoticeWhenPaused(t *testing.T) {
	cfg := newTestDB(t, "")
	seedScheduled(t, cfg, "sched", "trigger.schedule", true)
	cfg.JSONOutput = false
	notice := func() string {
		d := testDeps(cfg)
		root := &cobra.Command{Use: "workflow", SilenceUsage: true, SilenceErrors: true}
		root.AddCommand(d.NewListCmd())
		var errOut bytes.Buffer
		root.SetErr(&errOut)
		root.SetArgs([]string{"list"})
		captureStdout(t, func() {
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
		})
		return errOut.String()
	}
	account.InstallForTest(t, accounttest.Install(t, accounttest.SignedIn))
	if n := notice(); n != "" {
		t.Fatalf("signed in: notice %q", n)
	}
	account.InstallForTest(t, nil)
	n := notice()
	if !strings.Contains(n, "paused") || !strings.Contains(n, "monoagentcli account login") {
		t.Fatalf("signed out: notice %q", n)
	}
}
