package coderhost

import (
	"context"
	"errors"
	"math/rand"
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
