package coderhost

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
)

// Review of the launcher: a deleted child's spend is not lost (P1-1), and an uncharged row is not forgotten with
// age (P1-2).

// deleteChild deletes the child's conversation the way `chat delete` does: the spend is recorded in the delete's
// own transaction (guarded true), or not (a delete that bypasses the guard).
func (f *hostFix) deleteChild(t *testing.T, conv string, guarded bool) {
	t.Helper()
	store, err := ai.NewAIStore(f.st.db)
	if err != nil {
		t.Fatal(err)
	}
	if !guarded {
		if err := store.DeleteConversation(conv, "p"); err != nil {
			t.Fatal(err)
		}
		return
	}
	err = store.DeleteConversationGuarded(conv, "p", func(ctx context.Context, c *sql.Conn) error {
		return SnapshotChildSpendTx(ctx, c, "p", conv)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (f *hostFix) eventsOfKind(t *testing.T, kind string) []string {
	t.Helper()
	rows, err := f.st.db.Query(`SELECT note FROM coder_mission_events WHERE kind = ? AND session_id = ? ORDER BY id`, kind, f.se.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

func TestADeletedChildIsStillChargedWhatItSpent(t *testing.T) {
	f := newHostFix(t)
	conv := f.endedChild(t, 0, 1.25) // a turn finished, its row is not settled yet
	f.deleteChild(t, conv, true)
	if got, _ := f.st.Spent("p", conv); got != 0 {
		t.Fatalf("the deletion left %v of usage: the test would prove nothing", got)
	}
	f.h.missionChildPass()
	f.h.missionChildPass()
	f.h.settleEnded()
	f.h.settleChild("p", conv)
	if got := f.missionSpent(t); got != 1.25 {
		t.Fatalf("mission spent = %v, want the deleted child's 1.25", got)
	}
	if n := f.chargeRows(t); n != 1 {
		t.Fatalf("%d charge rows, want 1 (not a second, not a zero charge): %v", n, f.eventsOfKind(t, "charge"))
	}
	if ev := f.eventsOfKind(t, "charge"); len(ev) != 1 || !strings.HasPrefix(ev[0], conv+" $1.250000") {
		t.Fatalf("charge row = %v", ev)
	}
}

func TestADeletedChildWhoseSpendWasNotRecordedIsNotChargedZero(t *testing.T) {
	f := newHostFix(t)
	conv := f.endedChild(t, 0, 1.25) // wish 4
	f.deleteChild(t, conv, false)
	f.h.missionChildPass()
	f.h.missionChildPass()
	if got := f.missionSpent(t); got != 4 {
		t.Fatalf("mission spent = %v, want the child's budget (4) as the conservative figure", got)
	}
	if ev := f.eventsOfKind(t, "charge"); len(ev) != 1 || !strings.Contains(ev[0], "assumed") {
		t.Fatalf("charge row = %v: the assumption must be written down", ev)
	}
}

func TestAnUnchargedRowOlderThanAWeekIsStillChargedAndStillBlocksARemark(t *testing.T) {
	f := newHostFix(t)
	rv := &fakeRevoker{err: fmt.Errorf("db busy")}
	f.h.ChildTokens = rv
	conv := f.endedChild(t, 0, 1.25)
	f.h.missionChildPass() // ended; the revoke fails, so nothing is charged
	if _, err := f.st.db.Exec(`UPDATE coder_mission_children SET ended_at = '2020-01-01T00:00:00.000Z' WHERE child_conversation_id = ?`, conv); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[0], MarkOptions{BudgetUSD: 2}); err == nil {
		t.Fatal("a re-mark overwrote the conversation of an old uncharged row")
	}
	if r := f.row(t, 0); r.ChildConversationID != conv {
		t.Fatalf("row = %+v", r)
	}
	rv.mu.Lock()
	rv.err = nil
	rv.mu.Unlock()
	f.h.settleEnded()
	if got := f.missionSpent(t); got != 1.25 {
		t.Fatalf("mission spent = %v: the old row was not recovered", got)
	}
	if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[0], MarkOptions{BudgetUSD: 2}); err != nil {
		t.Fatalf("re-mark after the charge: %v", err)
	}
}

func TestTheSweepSettlesABacklogInBoundedPasses(t *testing.T) {
	f := newHostFix(t)
	total := chargeBatch*2 + 7
	for i := 0; i < total; i++ {
		c := fmt.Sprintf("backlog-%03d", i)
		if _, err := f.st.db.Exec(`INSERT INTO coder_mission_children (profile_id, session_id, task_id, lead_conversation_id, child_conversation_id, origin, state, end_reason, created_at, updated_at, ended_at)
			VALUES ('p', ?, ?, ?, ?, 'lead', 'ended', 'done', 'x', 'x', '2020-01-01T00:00:00.000Z')`, f.se.ID, 1000+i, f.lead.ConversationID, c); err != nil {
			t.Fatal(err)
		}
	}
	f.h.settleEnded()
	if n := f.chargeRows(t); n != chargeBatch {
		t.Fatalf("one pass wrote %d charge rows, want the bound %d", n, chargeBatch)
	}
	for i := 0; i < 5; i++ {
		f.h.settleEnded()
	}
	if n := f.chargeRows(t); n != total {
		t.Fatalf("after more passes %d charge rows, want %d: the backlog is not worked off", n, total)
	}
	var first string
	_ = f.st.db.QueryRow(`SELECT note FROM coder_mission_events WHERE kind = 'charge' ORDER BY id LIMIT 1`).Scan(&first)
	if !strings.HasPrefix(first, "backlog-000 ") {
		t.Fatalf("the first charge was %q: oldest rows go first", first)
	}
}

func TestAChargeThatCanNeverSucceedEndsInASkippedRowWithItsReason(t *testing.T) {
	f := newHostFix(t)
	rv := &fakeRevoker{err: fmt.Errorf("db busy")}
	f.h.ChildTokens = rv
	conv := f.endedChild(t, 0, 1.25)
	f.h.missionChildPass()
	// the mission's session is gone for good (its row id no longer names one)
	if _, err := f.st.db.Exec(`UPDATE coder_mission_children SET ended_at = '2020-01-01T00:00:00.000Z', session_id = 987654 WHERE child_conversation_id = ?`, conv); err != nil {
		t.Fatal(err)
	}
	rv.mu.Lock()
	rv.err = nil
	rv.mu.Unlock()
	f.h.settleEnded()
	rows, err := f.st.db.Query(`SELECT note FROM coder_mission_events WHERE kind = 'charge' AND session_id = 987654`)
	if err != nil {
		t.Fatal(err)
	}
	var ev []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		ev = append(ev, n)
	}
	rows.Close()
	if len(ev) != 1 || !strings.Contains(ev[0], "skipped: the mission session is gone") {
		t.Fatalf("charge rows = %v: want one terminal row with the reason", ev)
	}
	f.h.settleEnded()
	left, err := f.st.childrenWhere(strings.TrimSpace(notCharged) + ` AND child_conversation_id = '` + conv + `'`)
	if err != nil || len(left) != 0 {
		t.Fatalf("still uncharged after the terminal row: %v %v", left, err)
	}
}
