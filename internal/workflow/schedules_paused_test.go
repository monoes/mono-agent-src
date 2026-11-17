package workflow

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/account"
)

var (
	signedOutStatus = account.Status{State: account.StateLocked, Reason: account.ReasonNotLoggedIn, Enforced: true}
	signedInStatus  = account.Status{State: account.StateOK, Enforced: true}
)

func TestPausedTriggerWorkflows(t *testing.T) {
	ctx := context.Background()
	store := newFullEngineStore(t)
	manual := WorkflowNode{ID: "m", Type: "trigger.manual", Name: "m", Config: map[string]interface{}{}}
	saveTriggerWorkflow(t, ctx, store, "sched", scheduleNode("s", "0 0 9 * * *", ""))
	saveTriggerWorkflow(t, ctx, store, "hook", webhookNode("w", "/x", "POST"))
	saveTriggerWorkflow(t, ctx, store, "manual", manual)
	saveTriggerWorkflow(t, ctx, store, "idle", scheduleNode("s2", "0 0 9 * * *", "")) // never activated
	for _, id := range []string{"sched", "hook", "manual"} {
		if err := store.SetWorkflowActive(ctx, id, true); err != nil {
			t.Fatal(err)
		}
	}
	wfs, err := store.ListWorkflows(ctx, "default")
	if err != nil {
		t.Fatal(err)
	}
	ids := func(st account.Status) []string {
		got, err := PausedTriggerWorkflows(ctx, store, wfs, st)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	got := ids(signedOutStatus)
	if len(got) != 2 || !(got[0] == "sched" && got[1] == "hook" || got[0] == "hook" && got[1] == "sched") {
		t.Fatalf("signed out: %v; want the active schedule and webhook workflows only", got)
	}
	if got := ids(signedInStatus); len(got) != 0 {
		t.Fatalf("signed in: %v; want none", got)
	}
	// Gate not enforced yet: nothing is dropped, so nothing is paused.
	if got := ids(account.Status{State: account.StateLocked, Reason: account.ReasonNotLoggedIn}); len(got) != 0 {
		t.Fatalf("not enforced: %v; want none", got)
	}
	// Grace still allows work.
	if got := ids(account.Status{State: account.StateGrace, Enforced: true}); len(got) != 0 {
		t.Fatalf("grace: %v; want none", got)
	}
}
