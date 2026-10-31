package coderhost

import (
	"context"
	"database/sql"
	"errors"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/tasks"
)

// The host's admission of a carve (and of a raise of a child's cap) is the reservation store's own: the carve is a
// hold on the mission's session, and the session's whole commitment (spend, worker reservations, task-session
// children at any depth, the host's child holds) and that of every budgeted ancestor must carry it.

// childTaskSession makes a draft task session below parent with the given budget (0: none, as session_spawn makes it).
func (f *hostFix) childTaskSession(t *testing.T, parent int64, title string, budget float64, conv string) int64 {
	t.Helper()
	se, err := f.ts.CreateSession(context.Background(), "p", tasks.SessionInput{Title: title, ParentSessionID: parent, BudgetUSD: budget, ConversationID: conv}, opActor)
	if err != nil {
		t.Fatal(err)
	}
	return se.ID
}

func (f *hostFix) wantCarveRefused(t *testing.T, session int64, carve float64, what string) {
	t.Helper()
	why, err := f.st.missionBudgetRefusal(f.st.db, "p", session, f.lead.ConversationID, carve)
	if err != nil {
		t.Fatal(err)
	}
	if why == "" {
		t.Fatalf("%s: a carve of %v was accepted", what, carve)
	}
}

// codex scenario 1: a $10 mission whose open child task session has a budget of $8; a carve of $5 does not fit.
func TestACarveSeesTheOpenTaskSessionChildrenOfTheMission(t *testing.T) {
	f := newHostFix(t)
	f.setMissionBudget(t, 10)
	f.childTaskSession(t, f.se.ID, "kid", 8, "")
	f.wantCarveRefused(t, f.se.ID, 5, "an $8 child session under $10")
	if why, err := f.st.missionBudgetRefusal(f.st.db, "p", f.se.ID, f.lead.ConversationID, 2); err != nil || why != "" {
		t.Fatalf("a carve of 2 into the $2 left: %q, %v", why, err)
	}
	// through Reserve itself
	if err := f.st.SetBudget("p", f.lead.ConversationID, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[0], MarkOptions{BudgetUSD: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0]); !errors.Is(err, ErrLimit) {
		t.Fatalf("Reserve of a $5 carve with $8 held by a child session: %v", err)
	}
}

// codex scenario 2: a $2 coder child plus a worker reservation of $8 on a descendant task session leave nothing.
func TestACarveSeesAWorkerReservationOnADescendant(t *testing.T) {
	f := newHostFix(t)
	f.setMissionBudget(t, 10)
	f.task(t, 0, 2)
	kid := f.childTaskSession(t, f.se.ID, "kid", 0, "")
	grandkid := f.childTaskSession(t, kid, "grandkid", 0, "")
	if _, err := f.ts.Reserve(context.Background(), "p", grandkid, "owner-1", "w1", 8); err != nil {
		t.Fatal(err)
	}
	f.wantCarveRefused(t, f.se.ID, 8, "$2 child + $8 on a descendant under $10")
}

// codex scenario 3: a child task session with no budget of its own is held by its budgeted ancestors.
func TestACarveOnAnUnbudgetedChildIsHeldByItsBudgetedAncestors(t *testing.T) {
	f := newHostFix(t)
	f.setMissionBudget(t, 10)
	kid := f.childTaskSession(t, f.se.ID, "kid", 0, "")
	if why, err := f.st.missionBudgetRefusal(f.st.db, "p", kid, f.lead.ConversationID, 3); err != nil || why != "" {
		t.Fatalf("a carve of 3 under a mission with $10 free: %q, %v", why, err)
	}
	if _, err := f.ts.Reserve(context.Background(), "p", f.se.ID, "owner-1", "w1", 0); err != nil { // all of it
		t.Fatal(err)
	}
	f.wantCarveRefused(t, kid, 3, "the mission's whole budget is held by a worker")
}

// A raise of a child's cap is admitted like a carve.
func TestARaiseSeesTheTaskSessionTreeToo(t *testing.T) {
	f := newHostFix(t)
	f.setMissionBudget(t, 10)
	f.task(t, 0, 2)
	f.childTaskSession(t, f.se.ID, "kid", 6, "")
	b := BudgetRequest{ProfileID: "p", SessionID: f.se.ID, LeadConversationID: f.lead.ConversationID, Target: TargetChild}
	if why, err := f.st.budgetRaiseRefusal(f.st.db, b, 3); err != nil || why == "" {
		t.Fatalf("a raise of $3 with $2 free (carve 2, child session 6, mission 10) was accepted: %q, %v", why, err)
	}
}

// The carve and a worker on a descendant race for the last dollars over two connections: one wins.
func TestHostCarveAndADescendantWorkerRace(t *testing.T) {
	rng := rand.New(rand.NewSource(11600))
	for round := 0; round < 12; round++ {
		f := newHostFix(t)
		f.setMissionBudget(t, 8)
		if err := f.st.SetBudget("p", f.lead.ConversationID, 10); err != nil {
			t.Fatal(err)
		}
		f.setPolicy(t, f.scope(), PolicyPatch{SharePct: ip(80)})
		if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[0], MarkOptions{BudgetUSD: 8}); err != nil {
			t.Fatal(err)
		}
		kid := f.childTaskSession(t, f.se.ID, "kid", 0, "")
		workers := tasks.NewStore(openSecondHandle(t, f).DB)
		hostFirst := rng.Intn(2) == 0
		jitter := time.Duration(rng.Intn(300)) * time.Microsecond
		var wg sync.WaitGroup
		var hostErr, workerErr error
		var granted float64
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			if !hostFirst {
				time.Sleep(jitter)
			}
			_, hostErr = f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0])
		}()
		go func() {
			defer wg.Done()
			<-start
			if hostFirst {
				time.Sleep(jitter)
			}
			var r tasks.Reservation
			r, workerErr = workers.Reserve(context.Background(), "p", kid, "owner-w", "w1", 8)
			granted = r.AmountUSD
		}()
		close(start)
		wg.Wait()
		if (hostErr == nil) == (workerErr == nil) {
			t.Fatalf("round %d: host err %v, worker err %v: exactly one must win", round, hostErr, workerErr)
		}
		var carved float64
		if hostErr == nil {
			if err := f.st.db.QueryRow(`SELECT reserved_usd FROM coder_mission_children WHERE task_id = ?`, f.ids[0]).Scan(&carved); err != nil {
				t.Fatal(err)
			}
		} else {
			granted = 0
		}
		if carved+granted > 8+1e-9 {
			t.Fatalf("round %d: %v carved + %v granted pass 8", round, carved, granted)
		}
	}
}

// A linked child (session_spawn makes a coder child and its task session) is one commitment, not two.
func TestALinkedChildIsCountedOnce(t *testing.T) {
	f := newHostFix(t)
	f.setMissionBudget(t, 10)
	if err := f.st.SetBudget("p", f.lead.ConversationID, 10); err != nil {
		t.Fatal(err)
	}
	out, err := f.spawnSession(req("linked", f.bigScope()))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	total := func() float64 {
		c, err := f.ts.Commitment(ctx, "p", f.se.ID)
		if err != nil {
			t.Fatal(err)
		}
		return c.Total()
	}
	carve := out.BudgetUSD
	if carve <= 0 {
		t.Fatalf("spawned %+v", out)
	}
	if got := total(); got != carve {
		t.Fatalf("a linked child with no task budget: commitment %v, want its carve %v", got, carve)
	}
	// the operator gives its task session a budget equal to the carve: still one commitment
	if _, err := f.ts.UpdateSession(ctx, "p", out.TaskSessionID, tasks.SessionUpdate{BudgetUSD: &carve}, opActor); err != nil {
		t.Fatal(err)
	}
	if got := total(); got != carve {
		t.Fatalf("a linked child with a task budget of its carve: commitment %v, want %v (counted twice?)", got, carve)
	}
	// a larger task budget wins: the larger of the two, never the sum
	big := carve + 1
	if _, err := f.ts.UpdateSession(ctx, "p", out.TaskSessionID, tasks.SessionUpdate{BudgetUSD: &big}, opActor); err != nil {
		t.Fatal(err)
	}
	if got := total(); got != big {
		t.Fatalf("a task budget of %v over a carve of %v: commitment %v, want %v", big, carve, got, big)
	}
}

// An unlinked coder child and an independent task-session child are two commitments.
func TestUnlinkedChildrenAreCountedSeparately(t *testing.T) {
	f := newHostFix(t)
	f.setMissionBudget(t, 10)
	f.task(t, 0, 2)
	f.childTaskSession(t, f.se.ID, "independent", 3, "")
	c, err := f.ts.Commitment(context.Background(), "p", f.se.ID)
	if err != nil || c.Total() != 5 {
		t.Fatalf("commitment = %+v, %v: want 2 + 3", c, err)
	}
}

// The statement that takes a carve is followed by a check of the whole tree: a carve that the statement alone lets
// through (it knows the host's rows and workers on the mission, not the task-session tree) is found over budget.
func TestOvercommitIsFoundAfterTheStatementTookTheCarve(t *testing.T) {
	f := newHostFix(t)
	f.setMissionBudget(t, 10)
	f.childTaskSession(t, f.se.ID, "kid", 8, "")
	f.mark(t, 0)
	won, err := takeReservation(f.st.db, "p", f.se.ID, f.ids[0], f.lead.ConversationID, 5, 5, -1, "2026-10-31T10:00:00Z")
	if err != nil || !won {
		t.Fatalf("the statement alone should take a $5 carve of $10: %v, %v", won, err)
	}
	why, err := tasks.Overcommitted(context.Background(), ctxQuerier{f.st.db}, "p", f.se.ID)
	if err != nil || why == "" {
		t.Fatalf("$5 carve + $8 child session on $10 not found over budget: %q, %v", why, err)
	}
	// and an ancestor, seen from a descendant
	kid := f.childTaskSession(t, f.se.ID, "kid2", 0, "")
	if why, err := tasks.Overcommitted(context.Background(), ctxQuerier{f.st.db}, "p", kid); err != nil || why == "" {
		t.Fatalf("an overcommitted ancestor is not seen from its descendant: %q, %v", why, err)
	}
}

// spawnLinked spawns a session (coder child plus task session) with a carve of usd under a mission of 10.
func (f *hostFix) spawnLinked(t *testing.T, usd float64) SessionSpawned {
	t.Helper()
	f.setMissionBudget(t, 10)
	if err := f.st.SetBudget("p", f.lead.ConversationID, 10); err != nil {
		t.Fatal(err)
	}
	r := req("linked", f.bigScope())
	r.BudgetUSD = usd
	out, err := f.spawnSession(r)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (f *hostFix) setTaskBudget(t *testing.T, id int64, usd float64) {
	t.Helper()
	if _, err := f.ts.UpdateSession(context.Background(), "p", id, tasks.SessionUpdate{BudgetUSD: &usd}, opActor); err != nil {
		t.Fatal(err)
	}
}

// A raise that the linked child's task budget already covers changes nothing: the child's contribution is the larger
// of its cap and its task hold, so raising the cap from 2 to 3 under a task budget of 10 adds no commitment.
func TestACoveredRaiseOfALinkedChildIsAdmitted(t *testing.T) {
	f := newHostFix(t)
	out := f.spawnLinked(t, 2)
	f.setTaskBudget(t, out.TaskSessionID, 10)
	why, err := f.st.missionBudgetRefusalFor(f.st.db, "p", f.se.ID, f.lead.ConversationID, 1, out.ConversationID)
	if err != nil || why != "" {
		t.Fatalf("a raise of 1 covered by the child's task budget of 10: %q, %v", why, err)
	}
	// not covered: a task budget of 3 and a cap of 2 raised by 9 passes the mission's 10
	f.setTaskBudget(t, out.TaskSessionID, 3)
	why, err = f.st.missionBudgetRefusalFor(f.st.db, "p", f.se.ID, f.lead.ConversationID, 9, out.ConversationID)
	if err != nil || why == "" {
		t.Fatalf("a raise to a cap of 11 under a mission of 10 was accepted: %q, %v", why, err)
	}
	// a raise of an unlinked child is a new hold, as before
	why, err = f.st.missionBudgetRefusalFor(f.st.db, "p", f.se.ID, f.lead.ConversationID, 9, "no-such-child")
	if err != nil || why == "" {
		t.Fatalf("a raise of 9 on top of 3 and 2 passed: %q, %v", why, err)
	}
}

// A sub-cent carve is valid (it is not a worker reservation, which has a $0.01 minimum): in a budgeted tree too.
func TestASubCentCarveIsAdmittedInABudgetedTree(t *testing.T) {
	f := newHostFix(t)
	f.setMissionBudget(t, 0.008) // room for $0.005, none for a worker's $0.01 minimum
	kid := f.childTaskSession(t, f.se.ID, "kid", 0, "")
	for _, sid := range []int64{f.se.ID, kid} {
		if why, err := f.st.missionBudgetRefusal(f.st.db, "p", sid, f.lead.ConversationID, 0.005); err != nil || why != "" {
			t.Fatalf("a carve of $0.005 on session %d: %q, %v", sid, why, err)
		}
	}
	// the bound still holds at the edge
	f.wantCarveRefused(t, f.se.ID, 0.009, "past the budget")
}

// Two task sessions below the mission with the conversation of one coder child are still one child; an archived
// sibling does not count, and an open one is preferred to a closed one.
func TestTwoSiblingTaskSessionsOfOneCoderChildCountItsCapOnce(t *testing.T) {
	f := newHostFix(t)
	out := f.spawnLinked(t, 2)
	ctx := context.Background()
	twin, err := f.ts.CreateSession(ctx, "p", tasks.SessionInput{Title: "twin", ParentSessionID: f.se.ID, ConversationID: out.ConversationID}, opActor)
	if err != nil {
		t.Skipf("a second task session cannot share the conversation: %v", err)
	}
	c, err := f.ts.Commitment(ctx, "p", f.se.ID)
	if err != nil || c.Total() != 2 {
		t.Fatalf("two sibling task sessions of one $2 child: %+v, %v: want 2", c, err)
	}
	if _, err := f.ts.ArchiveSession(ctx, "p", twin.ID, opActor); err != nil {
		t.Fatal(err)
	}
	if c, err = f.ts.Commitment(ctx, "p", f.se.ID); err != nil || c.Total() != 2 {
		t.Fatalf("with one sibling archived: %+v, %v: want 2", c, err)
	}
	var linked int
	if err := f.st.db.QueryRow(`SELECT COUNT(*) FROM v_mission_child_linked WHERE conversation_id = ?`, out.ConversationID).Scan(&linked); err != nil || linked != 2 {
		t.Fatalf("the link view lists %d task sessions for the conversation (every match, grouped by the walk), %v", linked, err)
	}
}

// The tree-wide check after the statement is part of Reserve and of a grant: undoing it lets a carve through.
func TestReserveAndGrantRunTheOvercommitCheck(t *testing.T) {
	f := newHostFix(t)
	f.setMissionBudget(t, 10)
	if err := f.st.SetBudget("p", f.lead.ConversationID, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[0], MarkOptions{BudgetUSD: 4}); err != nil {
		t.Fatal(err)
	}
	// something the checks before the statement cannot see appears with the carve in place
	f.st.afterCarve = func(tx *sql.Tx) {
		if _, err := tx.Exec(`UPDATE task_sessions SET budget_usd = 0.5 WHERE id = ?`, f.se.ID); err != nil {
			t.Error(err)
		}
	}
	if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0]); !errors.Is(err, ErrLimit) || !strings.Contains(err.Error(), "would be committed") {
		t.Fatalf("Reserve with the mission over budget after the carve: %v", err)
	}
	var state string
	if err := f.st.db.QueryRow(`SELECT state FROM coder_mission_children WHERE task_id = ?`, f.ids[0]).Scan(&state); err != nil || state != "marked" {
		t.Fatalf("the refused reservation left the row %q, %v", state, err)
	}
	f.st.afterCarve = nil

	g, child := budgetFix(t)
	g.st.afterCarve = func(tx *sql.Tx) {
		if _, err := tx.Exec(`UPDATE task_sessions SET budget_usd = 0.5 WHERE id = ?`, g.se.ID); err != nil {
			t.Error(err)
		}
	}
	if _, err := g.st.db.Exec(`UPDATE task_sessions SET budget_usd = 20 WHERE id = ?`, g.se.ID); err != nil {
		t.Fatal(err)
	}
	b := g.ask(t, TargetChild, 1)
	if _, err := g.st.GrantBudget(OperatorScope{}, "p", b.ID, 0); !errors.Is(err, ErrLimit) || !strings.Contains(err.Error(), "would be committed") {
		t.Fatalf("a grant with the mission over budget after the raise: %v", err)
	}
	if g.capOf(t, child) != 4 {
		t.Fatalf("the refused grant changed the cap to %v", g.capOf(t, child))
	}
}

// Sibling task sessions that carry one coder child's conversation, with different holds: the cap counts once, whatever
// the order the sessions were made in. Cap 4 over holds of 1 and 4 is 5 (the holds add; the cap is inside them).
func TestUnequalSiblingHoldsGiveTheSameCommitmentInEitherOrder(t *testing.T) {
	for _, tc := range []struct{ first, second float64 }{{1, 4}, {4, 1}} {
		f := newHostFix(t)
		out := f.spawnLinked(t, 4)
		ctx := context.Background()
		twin, err := f.ts.CreateSession(ctx, "p", tasks.SessionInput{Title: "twin", ParentSessionID: f.se.ID, ConversationID: out.ConversationID}, opActor)
		if err != nil {
			t.Fatal(err)
		}
		f.setTaskBudget(t, out.TaskSessionID, tc.first)
		f.setTaskBudget(t, twin.ID, tc.second)
		c, err := f.ts.Commitment(ctx, "p", f.se.ID)
		if err != nil || c.Total() != 5 {
			t.Fatalf("holds %v then %v under a cap of 4: commitment %+v, %v: want 5", tc.first, tc.second, c, err)
		}
		// holds under the cap: the cap is what the child holds
		f.setTaskBudget(t, out.TaskSessionID, 1)
		f.setTaskBudget(t, twin.ID, 2)
		if c, err = f.ts.Commitment(ctx, "p", f.se.ID); err != nil || c.Total() != 4 {
			t.Fatalf("holds 1 and 2 under a cap of 4: commitment %+v, %v: want 4", c, err)
		}
	}
}
