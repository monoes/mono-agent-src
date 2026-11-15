package doctor

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/health"
	"github.com/monoes/mono-agent/internal/workflow"
)

func TestSchedulesPausedInfoFollowsTheSignIn(t *testing.T) {
	ctx := context.Background()
	env, db, store := workflowDoctorEnv(t)
	wf := &workflow.Workflow{ID: "wf-s", Name: "s", ProfileID: "default", IsActive: true,
		Nodes: []workflow.WorkflowNode{{ID: "s-trig", WorkflowID: "wf-s", Type: "trigger.schedule", Name: "s-trig",
			Config: map[string]any{"cron": "0 0 9 * * *"}}}}
	if err := store.SaveWorkflow(ctx, wf); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`UPDATE workflows SET is_active = 1 WHERE id = ?`, "wf-s"); err != nil {
		t.Fatal(err)
	}
	open := func() (*workflow.HybridWorkflowStore, error) { return store, nil }
	out := account.Status{State: account.StateLocked, Reason: account.ReasonNotLoggedIn, Enforced: true}
	in := account.Status{State: account.StateOK, Enforced: true}

	got, err := schedulesPausedInfo(ctx, env, open, out)
	if err != nil || !got.Paused || got.Workflows != 1 || got.Reason != "not_logged_in" {
		t.Fatalf("signed out: %+v %v", got, err)
	}
	if got, err = schedulesPausedInfo(ctx, env, open, in); err != nil || got.Paused || got.Workflows != 0 {
		t.Fatalf("signed in: %+v %v", got, err)
	}
	// The row is a warning at worst, whatever the verdict.
	for _, st := range []account.Status{out, in} {
		info, _ := schedulesPausedInfo(ctx, env, open, st)
		env.SchedulesPaused = func(context.Context) (health.SchedulesPausedInfo, error) { return info, nil }
		for _, c := range health.Default().Checks() {
			if c.ID == health.CheckSchedulesPaused {
				if r := c.Run(ctx, env); r.Status == health.StatusFail || c.Required {
					t.Fatalf("%+v", r)
				}
			}
		}
	}
}
