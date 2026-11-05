package coderhost

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
)

// A spawn's carve is a hold on the lead's task session and on every budgeted session above it. The cap arithmetic
// of the coder host reads the lead's coder session only, and the lead's room is read before the carve, so these
// tests take the room away in between (a gate that runs a competitor at the dispatch point) and put the lead below
// a budgeted session it has no budget under.

// handFunc is a DispatchGate that runs before once, then the dispatched function.
type handFunc struct{ before func() }

func (h *handFunc) Hand(_ DispatchPoint, fn func(context.Context) error) error {
	if h.before != nil {
		h.before()
	}
	return fn(context.Background())
}
func (h *handFunc) Start(DispatchPoint, func() (StartedProc, error)) error { return nil }

func setSessionBudget(t *testing.T, f *mcFix, usd float64) {
	t.Helper()
	if _, err := f.ts.UpdateSession(context.Background(), "p", f.se.ID, tasks.SessionUpdate{BudgetUSD: &usd}, opActor); err != nil {
		t.Fatal(err)
	}
}

func (f *mcFix) overcommitted(t *testing.T) string {
	t.Helper()
	why, err := tasks.Overcommitted(context.Background(), ctxQuerier{f.st.db}, "p", f.se.ID)
	if err != nil {
		t.Fatal(err)
	}
	return why
}

func (f *mcFix) assertNothingLeft(t *testing.T) {
	t.Helper()
	if n := countRows(t, f.st, `SELECT COUNT(*) FROM coder_sessions WHERE role = 'child'`); n != 0 {
		t.Errorf("%d child conversations left by the refused spawn", n)
	}
	if n := countRows(t, f.st, `SELECT COUNT(*) FROM task_sessions WHERE parent_session_id = ?`, f.se.ID); n != 0 {
		t.Errorf("%d task sessions left by the refused spawn", n)
	}
}

// (1) The room is taken between the lead's share being worked out and the carve: the carve is refused, the spawn is
// undone, and the session is not committed past its budget.
func TestSpawnSessionRefusesACarveThatNoLongerFitsWhenItIsTaken(t *testing.T) {
	f := newMC(t, true)
	setSessionBudget(t, f, 10)
	f.st.SetDispatchGate(&handFunc{before: func() {
		if _, err := f.ts.Reserve(context.Background(), "p", f.se.ID, "owner-1", "w1", 8); err != nil {
			t.Errorf("the competing reservation: %v", err)
		}
	}})
	r := req("Docs", f.bigScope())
	r.BudgetUSD = 4 // 40 percent of the $10 that were left when the share was worked out
	if _, err := f.spawnSession(r); !errors.Is(err, ErrLimit) {
		t.Fatalf("a $4 carve with $2 left: err = %v, want ErrLimit", err)
	}
	f.assertNothingLeft(t)
	if why := f.overcommitted(t); why != "" {
		t.Errorf("committed past the budget: %s", why)
	}
}

// (2) A lead with no budget of its own below a budgeted session (a row older than the rule that a child has one)
// cannot carve more than the session above it has left.
func TestSpawnSessionIsRefusedWhenABudgetedAncestorHasNoRoom(t *testing.T) {
	f := newMC(t, true)
	setSessionBudget(t, f, 10)
	lead, _ := master(t, f.st, "workspace")
	d, err := f.ts.CreateSession(context.Background(), "p", tasks.SessionInput{Title: "direction", Goal: "g", ConversationID: lead.ConversationID,
		ParentSessionID: f.se.ID, Kind: tasks.SessionKindDirection, MaxTasks: 10, BudgetUSD: 5}, opActor)
	if err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"one", "two"} {
		if _, err := f.ts.AddSessionTask(context.Background(), "p", d.ID, tasks.SessionTaskInput{Title: title, Acceptance: "ok"}, opActor); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.ts.ApproveSession(context.Background(), "p", d.ID, opActor); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.db.Exec(`UPDATE task_sessions SET budget_usd = 0 WHERE id = ?`, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.db.Exec(`UPDATE task_sessions SET spent_usd = 9 WHERE id = ?`, f.se.ID); err != nil {
		t.Fatal(err)
	}
	r := req("Child", taskIDs(t, f.st, d.ID))
	r.BudgetUSD = 4
	if _, err := f.st.SpawnSession(context.Background(), NewLeadScope(lead, BindingNone), "p", r); !errors.Is(err, ErrLimit) {
		t.Fatalf("a $4 carve under a session with $1 left: err = %v, want ErrLimit", err)
	}
	if n := countRows(t, f.st, `SELECT COUNT(*) FROM task_sessions WHERE parent_session_id = ?`, d.ID); n != 0 {
		t.Errorf("%d task sessions left by the refused spawn", n)
	}
	if why := f.overcommitted(t); why != "" {
		t.Errorf("committed past the budget: %s", why)
	}
}

// (3) A spawn and a worker reservation racing for the last dollars never leave the session committed past its budget.
func TestConcurrentSpawnAndReservationNeverOvercommit(t *testing.T) {
	for round := 0; round < 8; round++ {
		f := newMC(t, true)
		setSessionBudget(t, f, 10)
		r := req("Docs", f.bigScope())
		r.BudgetUSD = 4
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			if _, err := f.spawnSession(r); err != nil && !errors.Is(err, ErrLimit) {
				t.Errorf("round %d: the spawn failed with %v", round, err)
			}
		}()
		go func() {
			defer wg.Done()
			<-start
			_, _ = f.ts.Reserve(context.Background(), "p", f.se.ID, "owner-1", "w1", 8)
		}()
		close(start)
		wg.Wait()
		if why := f.overcommitted(t); why != "" {
			t.Fatalf("round %d: committed past the budget: %s", round, why)
		}
	}
}
