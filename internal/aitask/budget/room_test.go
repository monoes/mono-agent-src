package budget

import (
	"context"
	"testing"
)

// Room only reads: it counts what is recorded and what the journal adds to it, and records neither. A follow-up
// turn's admission still records the spend.
func TestRoomWritesNothing(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, 0)
	if err := r.l.SetCap(ScopeGlobal, "", 10, "operator"); err != nil {
		t.Fatal(err)
	}
	a := r.task("c1", "generic", 8)
	recorded := func() (rows int, spent float64) {
		t.Helper()
		if err := r.l.DB.QueryRow(`SELECT COUNT(*), COALESCE(SUM(spent_usd), 0) FROM ai_budget_spend`).Scan(&rows, &spent); err != nil {
			t.Fatal(err)
		}
		return rows, spent
	}
	room := func(wantSpent float64, wantRows int) {
		t.Helper()
		got, err := r.l.Room(ctx, a)
		if err != nil || got.SpentUSD != wantSpent || got.CapUSD != 8 || !got.Set {
			t.Errorf("room %+v (%v), want $%v spent of $8", got, err, wantSpent)
		}
		if rows, _ := recorded(); rows != wantRows {
			t.Errorf("Room left %d spend rows, want %d", rows, wantRows)
		}
	}
	room(0, 0) // never ran: no $0 row appears
	if _, err := r.l.Admit(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := r.l.ReleaseUnspent(ctx, a.TaskID); err != nil {
		t.Fatal(err)
	}
	var released int
	if err := r.l.DB.QueryRow(`SELECT released FROM ai_budget_reservations WHERE task_id = ?`, a.TaskID).Scan(&released); err != nil || released != 1 {
		t.Errorf("after a read, a failed start kept its reservation: released=%d %v", released, err)
	}

	r.usage("c1", "turn-1", "", 1)
	r.usage("c1", "turn-1", "w1", 2)
	room(3, 0)
	if err := r.l.Sync(ctx, a.TaskID, "c1"); err != nil {
		t.Fatal(err)
	}
	room(3, 2) // recorded and journaled are the same spend, counted once
	r.usage("c1", "turn-2", "w1", 1.5)
	room(4.5, 2)
	if _, spent := recorded(); spent != 3 {
		t.Errorf("Room rewrote the recorded spend: $%v", spent)
	}
	if _, err := r.l.AdmitTurn(ctx, a, 0); err != nil {
		t.Fatal(err)
	}
	if _, spent := recorded(); spent != 4.5 {
		t.Errorf("a follow-up turn's admission recorded $%v, want $4.5", spent)
	}
	// What is recorded counts also once its journal is gone.
	if _, err := r.l.DB.Exec(`DELETE FROM ai_chat_events WHERE conversation_id = 'c1' AND turn_id = 'turn-1'`); err != nil {
		t.Fatal(err)
	}
	room(2.5, 2)
}
