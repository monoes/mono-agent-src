package coderhost

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
)

const denyTreeSettings = `{"agent_approval":"lead","max_depth":2}`

// giantItem makes the giant draft C of a mission and the person item its lead raised.
func giantItem(t *testing.T) (st *Store, ts *tasks.Store, c tasks.Session, it PersonItem) {
	t.Helper()
	st = newStoreT(t)
	c, ts = missionTree(t, st, denyTreeSettings)
	if _, err := ts.ApproveSession(context.Background(), "p", c.ID, approvalLeadActor); err == nil {
		t.Fatal("level 3 is giant under a working depth of 2")
	}
	it, ok, err := st.PlanApprovalItem("p", c.ID)
	if err != nil || !ok {
		t.Fatalf("item: %v %v", ok, err)
	}
	return st, ts, c, it
}

func count(t *testing.T, st *Store, q string, args ...any) int {
	t.Helper()
	var n int
	if err := st.db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func denyFacts(t *testing.T, st *Store, session int64) (cmds, events int) {
	t.Helper()
	cmds = count(t, st, `SELECT COUNT(*) FROM coder_session_commands WHERE conversation_id = 'lead-p'`)
	events = count(t, st, `SELECT COUNT(*) FROM task_session_events WHERE session_id = ? AND note LIKE 'the person turned the plan down%'`, session)
	return cmds, events
}

func TestDenyPlanClosesRecordsAndTellsTheLeadOnce(t *testing.T) {
	st, ts, c, it := giantItem(t)
	ctx := context.Background()
	cmd, err := st.DenyPlan(ctx, it, "you", "split it in two")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cmd.TurnID, "deny-") || !strings.Contains(cmd.Message, "split it in two") {
		t.Fatalf("command = %+v", cmd)
	}
	if cmds, events := denyFacts(t, st, c.ID); cmds != 1 || events != 1 {
		t.Fatalf("commands %d, events %d", cmds, events)
	}
	if open, _ := st.PersonItemRaised(ctx, "p", c.ID); open {
		t.Fatal("the item must be closed")
	}
	if se, _ := ts.GetSession(ctx, "p", c.ID, opActor); se.State != tasks.SessionDraft {
		t.Fatalf("%+v", se)
	}
	// A second deny of the same item has nothing to close and tells nobody.
	if _, err := st.DenyPlan(ctx, it, "you", "again"); err == nil {
		t.Fatal("a closed item was denied again")
	}
	if cmds, events := denyFacts(t, st, c.ID); cmds != 1 || events != 1 {
		t.Fatalf("a repeat wrote: commands %d, events %d", cmds, events)
	}
	// The message is keyed by item and round: telling again queues nothing new.
	again, err := st.tellDenied(it, 1, "split it in two")
	if err != nil || again.ID != cmd.ID {
		t.Fatalf("a retry must find the same command: %+v %v", again, err)
	}
	if cmds, _ := denyFacts(t, st, c.ID); cmds != 1 {
		t.Fatalf("a retry queued a duplicate: %d", cmds)
	}
}

// The item the person read can be stale by the time deny runs: the session was approved in between.
// Nobody is told "turned down" about a plan that runs.
func TestDenyPlanOfAPlanApprovedMeanwhileWritesAndSendsNothing(t *testing.T) {
	st, ts, c, it := giantItem(t)
	ctx := context.Background()
	if _, err := ts.ApproveSession(ctx, "p", c.ID, opActor); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DenyPlan(ctx, it, "you", "too late"); err == nil {
		t.Fatal("denied a plan that runs")
	}
	if cmds, events := denyFacts(t, st, c.ID); cmds != 0 || events != 0 {
		t.Fatalf("commands %d, events %d", cmds, events)
	}
}

func TestDenyPlanWithNoChatToTellChangesNothing(t *testing.T) {
	st, _, c, it := giantItem(t)
	it.ConversationID = ""
	if _, err := st.DenyPlan(context.Background(), it, "you", "no"); err == nil {
		t.Fatal("no chat to tell")
	}
	if cmds, events := denyFacts(t, st, c.ID); cmds != 0 || events != 0 {
		t.Fatalf("commands %d, events %d", cmds, events)
	}
	if open, _ := st.PersonItemRaised(context.Background(), "p", c.ID); !open {
		t.Fatal("the item must stay open")
	}
}

// deny and approve at the same instant: whichever commits first wins, and the other leaves no trace.
// A plan the person approved is never also reported as turned down.
func TestDenyPlanRacingAnApprovalIsConsistent(t *testing.T) {
	ctx := context.Background()
	for i := 0; i < 12; i++ {
		st, ts, c, it := giantItem(t)
		// The person approves what they read: the revision of the inbox card, as `person approve --rev` and the
		// app send it. A deny writes a note, which moves the revision, so a stale approve is refused (plain
		// `session approve` after a deny is a second, deliberate act of the operator).
		pv, err := ts.PlanOf(ctx, "p", c.ID, opActor)
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = st.DenyPlan(ctx, it, "you", "no") }()
		go func() {
			defer wg.Done()
			_, _ = ts.ApproveSessionAt(ctx, "p", c.ID, opActor, tasks.PlanGuard{Rev: pv.Rev, Digest: pv.Digest})
		}()
		wg.Wait()
		cmds, events := denyFacts(t, st, c.ID)
		se, _ := ts.GetSession(ctx, "p", c.ID, opActor)
		switch {
		case cmds != events:
			t.Fatalf("round %d: lead told %d times, history says %d", i, cmds, events)
		case cmds > 1:
			t.Fatalf("round %d: lead told %d times", i, cmds)
		case cmds == 0 && se.State != tasks.SessionRunning:
			t.Fatalf("round %d: nothing won: %+v", i, se)
		case cmds == 1 && se.ApprovalBasis == tasks.ApprovalPerson:
			t.Fatalf("round %d: the plan was told as turned down and approved by the person", i)
		}
	}
}
