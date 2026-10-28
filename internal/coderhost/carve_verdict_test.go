package coderhost

import (
	"errors"
	"testing"
)

type fakeResult struct {
	n   int64
	err error
}

func (r fakeResult) LastInsertId() (int64, error) { return 0, nil }
func (r fakeResult) RowsAffected() (int64, error) { return r.n, r.err }

// Every way the carve statement can match nothing, or fail to say, is told apart.
func TestCarveVerdictBranches(t *testing.T) {
	boom := errors.New("boom")
	still := func(v bool, err error) func() (bool, error) { return func() (bool, error) { return v, err } }
	for _, c := range []struct {
		name string
		res  fakeResult
		sc   func() (bool, error)
		want error
	}{
		{"carved", fakeResult{n: 1}, still(true, nil), nil},
		{"no room", fakeResult{}, still(true, nil), errCarveNoRoom},
		{"reservation lost", fakeResult{}, still(false, nil), errReservationLost},
		{"rows affected failed", fakeResult{err: boom}, still(true, nil), boom},
		{"re-read failed", fakeResult{}, still(true, boom), boom},
	} {
		got := carveVerdict(c.res, c.sc)
		if c.want == nil && got != nil || c.want != nil && !errors.Is(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
		if errors.Is(got, errCarveNoRoom) && c.want != errCarveNoRoom {
			t.Errorf("%s: read as no room", c.name)
		}
	}
}

// A child whose session vanished under the carve is a lost reservation, not a full budget.
func TestCarveOfAVanishedChildIsLostNotFull(t *testing.T) {
	f := newHostFix(t)
	f.st.SetBudget("p", f.lead.ConversationID, 10)
	if err := f.st.carveBudget("p", f.lead.ConversationID, "nobody", 1); !errors.Is(err, errReservationLost) {
		t.Fatalf("carve of a missing child = %v, want errReservationLost", err)
	}
}

// Through the spawn: the child's session disappears before the carve; the task is not told the children are full.
func TestSpawnWithAVanishedChildIsNotChildrenFull(t *testing.T) {
	f := newHostFix(t)
	f.st.SetBudget("p", f.lead.ConversationID, 10)
	if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[0], MarkOptions{BudgetUSD: 3}); err != nil {
		t.Fatal(err)
	}
	// the moment the pending conversation is noted (just before the carve), the child stops being the master's
	if _, err := f.st.db.Exec(`CREATE TRIGGER vanish_child AFTER UPDATE OF pending_conversation_id ON coder_mission_children
		BEGIN UPDATE coder_sessions SET parent_conversation_id = '' WHERE parent_conversation_id = '`+f.lead.ConversationID+`'; END`); err != nil {
		t.Fatal(err)
	}
	_, err := f.st.SpawnForTask(f.scope(), "p", f.se.ID, f.ids[0], "brief")
	if err == nil || errors.Is(err, ErrChildrenFull) || errors.Is(err, ErrLimit) || errors.Is(err, errCarveRefused) {
		t.Fatalf("err = %v, want a plain failure (not full, not a refused carve)", err)
	}
	if r := f.row(t, 0); r.Attempts != 1 {
		t.Fatalf("row = %+v, want the attempt charged (a lost reservation is a failure, not a wait)", r)
	}
}
