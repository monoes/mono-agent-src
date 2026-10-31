package coderhost

import (
	"context"
	"errors"
	"math"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/tasks"
)

// The coder host's child carves and the worker reservation store (internal/tasks) spend one budget. They see each
// other's holds through v_mission_child_held (migration 150) and tasks.ReservationHeld.

// oldInlineHeld is the child-hold SQL as it stood inline in missionBudgetRefusal before migration 150 moved it
// into the view, kept here as the golden for the view (parameters: profile, lead, session, profile, lead, profile, session).
const oldInlineHeld = `SELECT (SELECT COALESCE(SUM(s.budget_usd), 0) FROM coder_sessions s WHERE s.profile_id = ? AND s.parent_conversation_id = ?
			AND NOT EXISTS (SELECT 1 FROM coder_mission_children c WHERE c.profile_id = s.profile_id AND c.child_conversation_id = s.conversation_id
				AND c.session_id = ? AND c.state IN ('ended', 'failed')
				AND EXISTS (SELECT 1 FROM coder_mission_events e WHERE e.profile_id = c.profile_id AND e.session_id = c.session_id AND e.kind = 'charge'
					AND substr(e.note, 1, length(c.child_conversation_id) + 1) = c.child_conversation_id || ' ')))
		+ (SELECT COALESCE(SUM(reserved_usd), 0) FROM coder_mission_children WHERE profile_id = ? AND lead_conversation_id = ? AND state = 'spawning')
		+ (SELECT COALESCE(SUM(CAST(substr(e.note, instr(e.note, ' $') + 2) AS REAL)), 0) FROM coder_mission_events e WHERE e.profile_id = ? AND e.session_id = ? AND e.kind = 'spend'
			AND NOT EXISTS (SELECT 1 FROM coder_mission_events c WHERE c.profile_id = e.profile_id AND c.session_id = e.session_id AND c.kind = 'charge'
				AND substr(c.note, 1, instr(e.note, ' ')) = substr(e.note, 1, instr(e.note, ' '))))`

// oldLostSQL is the lost-child condition as it stood in Go (lostChildSQL) before the view took it.
const oldLostSQL = `SELECT (EXISTS (SELECT 1 FROM coder_mission_children WHERE coder_mission_children.profile_id = ? AND coder_mission_children.session_id = ? AND
	coder_mission_children.state IN ('spawning', 'spawned', 'ended', 'failed', 'gone') AND ` + rowIdent + ` <> ''
	AND (coder_mission_children.spawned_at <> '' OR coder_mission_children.pending_conversation_id <> '')
	AND NOT EXISTS (SELECT 1 FROM coder_sessions s WHERE s.profile_id = coder_mission_children.profile_id AND s.conversation_id = ` + rowIdent + `)
	AND NOT EXISTS (SELECT 1 FROM coder_mission_events x WHERE x.profile_id = coder_mission_children.profile_id AND x.session_id = coder_mission_children.session_id
		AND x.kind IN ('charge', 'spend', '` + unresolvedKind + `') AND substr(x.note, 1, length(` + rowIdent + `) + 1) = ` + rowIdent + ` || ' '))
	OR EXISTS (SELECT 1 FROM coder_mission_events u WHERE u.profile_id = ? AND u.session_id = ? AND u.kind = '` + unresolvedKind + `'
		AND NOT EXISTS (SELECT 1 FROM coder_mission_events c WHERE c.profile_id = u.profile_id AND c.session_id = u.session_id AND c.kind = 'charge'
			AND substr(c.note, 1, instr(u.note, ' ')) = substr(u.note, 1, instr(u.note, ' ')))))`

// checkViewAgainstOld compares the view with the old inline numbers on the fixture's mission.
func (f *hostFix) checkViewAgainstOld(t *testing.T, label string) (held float64, lost bool) {
	t.Helper()
	db, sid, lead := f.st.db, f.se.ID, f.lead.ConversationID
	var raw float64
	if err := db.QueryRow(oldInlineHeld, "p", lead, sid, "p", lead, "p", sid).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var lostN int
	if err := db.QueryRow(oldLostSQL, "p", sid, "p", sid).Scan(&lostN); err != nil {
		t.Fatal(err)
	}
	var budget, spent float64
	if err := db.QueryRow(`SELECT budget_usd, spent_usd FROM task_sessions WHERE id = ?`, sid).Scan(&budget, &spent); err != nil {
		t.Fatal(err)
	}
	want := raw
	if lostN > 0 && budget > 0 {
		want = math.Max(raw, math.Max(budget-spent, 0))
	}
	var got float64
	var gotLost int
	if err := db.QueryRow(`SELECT held_usd, lost FROM v_mission_child_held WHERE profile_id = 'p' AND session_id = ?`, sid).Scan(&got, &gotLost); err != nil {
		t.Fatalf("%s: the view has no row for the mission: %v", label, err)
	}
	if math.Abs(got-want) > 1e-12 || (gotLost > 0) != (lostN > 0) {
		t.Fatalf("%s: view = (%v, lost %d), old inline = (%v, lost %d)", label, got, gotLost, want, lostN)
	}
	return got, lostN > 0
}

func (f *hostFix) setMissionBudget(t *testing.T, usd float64) {
	t.Helper()
	if _, err := f.st.db.Exec(`UPDATE task_sessions SET budget_usd = ? WHERE id = ?`, usd, f.se.ID); err != nil {
		t.Fatal(err)
	}
}

// The view carries exactly the numbers the inline SQL did: nothing, a live child's cap, a reserved carve, an ended
// child already charged, and every lost-child state of the walk table (snapshot, live events, lost, unresolved).
func TestTheViewEqualsTheOldInlineChildHold(t *testing.T) {
	f := newHostFix(t)
	f.setMissionBudget(t, 10)
	if h, lost := f.checkViewAgainstOld(t, "empty"); h != 0 || lost {
		t.Fatalf("empty mission holds %v lost %v", h, lost)
	}
	f.task(t, 0, 4)
	if h, _ := f.checkViewAgainstOld(t, "one live child"); h != 4 {
		t.Fatalf("a live child with a cap of 4 holds %v", h)
	}
	if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[1], MarkOptions{BudgetUSD: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[1]); err != nil {
		t.Fatal(err)
	}
	if h, _ := f.checkViewAgainstOld(t, "a reserved carve"); h <= 4 {
		t.Fatalf("a reserved carve is not in the hold: %v", h)
	}
	var lostSeen bool
	for _, c := range walkCases {
		w := newHostFix(t)
		w.buildWalk(t, c)
		if _, lost := w.checkViewAgainstOld(t, c.name); lost {
			lostSeen = true
		}
	}
	if !lostSeen {
		t.Fatal("no walk case is lost: the golden would pass on nothing")
	}
}

// A child session of the host holds its carve against a worker reservation: tasks.Reserve on the mission sees it,
// and the lost-child freeze refuses a worker too.
func TestAWorkerReservationSeesTheHostsChildCarves(t *testing.T) {
	f := newHostFix(t)
	f.setMissionBudget(t, 10)
	f.task(t, 0, 4)
	ctx := context.Background()
	r, err := f.ts.Reserve(ctx, "p", f.se.ID, "owner-1", "w1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.AmountUSD != 6 {
		t.Fatalf("a worker asking for all that is left got %v of $10 with a $4 child carve: want 6", r.AmountUSD)
	}
	c, err := f.ts.Commitment(ctx, "p", f.se.ID)
	if err != nil || c.Held != 10 {
		t.Fatalf("commitment held = %v, %v: want the carve and the reservation, 10", c.Held, err)
	}

	lost := newHostFix(t)
	lost.buildWalk(t, walkCase{name: "lost", cost: "lost", unresolved: true})
	if _, err := lost.ts.Reserve(ctx, "p", lost.se.ID, "owner-1", "w1", 1); err == nil {
		t.Fatal("a worker reserved on a mission with a lost child")
	}
}

// The host's check adds the workers' holds: a carve that fits the mission without them does not fit with them.
func TestTheHostsCarveSeesTheWorkersHolds(t *testing.T) {
	f := newHostFix(t)
	f.setMissionBudget(t, 10)
	if why, err := f.st.missionBudgetRefusal(f.st.db, "p", f.se.ID, f.lead.ConversationID, 7); err != nil || why != "" {
		t.Fatalf("a carve of 7 of 10 with nothing held: %q, %v", why, err)
	}
	if _, err := f.ts.Reserve(context.Background(), "p", f.se.ID, "owner-1", "w1", 5); err != nil {
		t.Fatal(err)
	}
	why, err := f.st.missionBudgetRefusal(f.st.db, "p", f.se.ID, f.lead.ConversationID, 7)
	if err != nil || why == "" {
		t.Fatalf("a carve of 7 passed with $5 held by a worker: %q, %v", why, err)
	}
	if why, err := f.st.missionBudgetRefusal(f.st.db, "p", f.se.ID, f.lead.ConversationID, 5); err != nil || why != "" {
		t.Fatalf("a carve of 5 against $5 left: %q, %v", why, err)
	}
}

// takeReservation's own statement is the gate, not only the pre-check: with a worker's hold on the mission it takes
// nothing that does not fit, and takes it again once the worker is gone.
func TestTakeReservationItselfCountsTheWorkersHolds(t *testing.T) {
	f := newHostFix(t)
	f.setMissionBudget(t, 10)
	f.mark(t, 0)
	r, err := f.ts.Reserve(context.Background(), "p", f.se.ID, "owner-1", "w1", 6)
	if err != nil {
		t.Fatal(err)
	}
	take := func(carve float64) bool {
		t.Helper()
		won, err := takeReservation(f.st.db, "p", f.se.ID, f.ids[0], f.lead.ConversationID, 5, carve, -1, "2026-10-31T10:00:00Z")
		if err != nil {
			t.Fatal(err)
		}
		return won
	}
	if take(5) {
		t.Fatal("takeReservation took a carve of 5 with $6 of 10 held by a worker")
	}
	if !take(4) {
		t.Fatal("takeReservation refused a carve of 4 with $4 left")
	}
	if err := f.ts.ReleaseReservation(context.Background(), "p", r.ID, "owner-1"); err != nil {
		t.Fatal(err)
	}
}

// openSecondHandle opens another *sql.DB on the fixture's database file: another process, as far as SQLite sees.
func openSecondHandle(t *testing.T, f *hostFix) *storage.Database {
	t.Helper()
	var path string
	rows, err := f.st.db.Query(`PRAGMA database_list`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var seq int
		var name, file string
		if err := rows.Scan(&seq, &name, &file); err != nil {
			t.Fatal(err)
		}
		if name == "main" {
			path = file
		}
	}
	rows.Close()
	d, err := storage.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// The last dollar: the host's carve and a worker's reservation race over two connections to one file, in random
// order. Exactly one wins and what they hold never passes the mission's budget.
func TestHostCarveAndWorkerRaceForTheLastDollar(t *testing.T) {
	rng := rand.New(rand.NewSource(1160))
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
		other := openSecondHandle(t, f)
		workers := tasks.NewStore(other.DB)
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
			r, workerErr = workers.Reserve(context.Background(), "p", f.se.ID, "owner-w", "w1", 8)
			granted = r.AmountUSD
		}()
		close(start)
		wg.Wait()
		var carved float64
		if hostErr == nil {
			if err := f.st.db.QueryRow(`SELECT reserved_usd FROM coder_mission_children WHERE task_id = ?`, f.ids[0]).Scan(&carved); err != nil {
				t.Fatal(err)
			}
		} else if !errors.Is(hostErr, ErrLimit) {
			t.Fatalf("round %d: the host's refusal is %v", round, hostErr)
		}
		if workerErr != nil {
			granted = 0
		}
		if (hostErr == nil) == (workerErr == nil) {
			t.Fatalf("round %d: host err %v, worker err %v: exactly one must win", round, hostErr, workerErr)
		}
		if carved+granted > 8+1e-9 {
			t.Fatalf("round %d: %v carved + %v granted pass the budget 8", round, carved, granted)
		}
	}
}
