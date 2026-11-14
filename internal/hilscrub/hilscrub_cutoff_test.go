package hilscrub

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// newCard inserts a card the way the current code writes one: after the cutoff
// was marked, so its row id lies past the bound whatever its timestamp says.
func (f *fix) newCard(t *testing.T, id, text, created string) {
	t.Helper()
	f.card(t, id, text, nil)
	if _, err := f.db.Exec(`UPDATE hil_pending SET created_at = `+created+` WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
}

func (f *fix) again() { // a new process, and the marker gone: the next read runs a pass
	states.Delete(fmt.Sprintf("%p|%s", f.db, "default"))
	_, _ = f.db.Exec(`UPDATE settings SET value = '' WHERE key = ?`, markerKey("default"))
}

// A clock stepped backwards gives a new card an older timestamp than the
// cutoff: the row id bound keeps it out of the scrub.
func TestABackwardClockStepDoesNotMakeANewCardEligible(t *testing.T) {
	f := newFix(t)
	f.card(t, "old", "k "+val, nil)
	Ensure(f.ctx, f.db, "default") // marks the cutoff and its row id bound
	f.newCard(t, "new", "k "+val, `datetime('now','-1 day')`)
	f.again()
	Ensure(f.ctx, f.db, "default")
	if !strings.Contains(f.editable(t, "new"), val) {
		t.Error("a card written after the cutoff was scrubbed because its timestamp is older (clock stepped back)")
	}
	if strings.Contains(f.editable(t, "old"), val) {
		t.Error("the old card was not scrubbed")
	}
}

// A database whose cutoff was written before the row id bound existed gets
// the bound at the next read, from the rows that are older than the cutoff.
func TestACutoffWithoutABoundGetsOne(t *testing.T) {
	f := newFix(t)
	f.card(t, "old", "k "+val, nil)
	if _, err := f.db.Exec(`INSERT INTO settings (key, value) VALUES (?, datetime('now'))`, cutoffKey); err != nil {
		t.Fatal(err)
	}
	Ensure(f.ctx, f.db, "default")
	var v string
	if f.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, rowidKey).Scan(&v) != nil || v == "" || v == "0" {
		t.Errorf("no bound after the read: %q", v)
	}
	if strings.Contains(f.editable(t, "old"), val) {
		t.Error("the old card was not scrubbed")
	}
}

// While a pass has failed the readers mask in memory: only a row the scrub
// would have rewritten (older than the cutoff), never a newer card.
func TestMaskAppliesOnlyToCardsOlderThanTheCutoff(t *testing.T) {
	f := newFix(t)
	f.card(t, "old", "k "+val, nil)
	MarkCutoff(f.ctx, f.db)
	f.newCard(t, "new", "k "+val, `datetime('now','+1 minute')`)
	f.newCard(t, "back", "k "+val, `datetime('now','-1 day')`)
	conn, err := f.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		conn.Close()
	}()
	Ensure(f.ctx, f.db, "default") // cannot write: values are kept for masking
	if got := Mask(f.db, "default", "old", "row: "+val); strings.Contains(got, val) {
		t.Errorf("an old card is shown unmasked: %q", got)
	}
	if got := Mask(f.db, "default", "new", "row: "+val); !strings.Contains(got, val) {
		t.Errorf("a card newer than the cutoff was masked (an oracle for guessing the value): %q", got)
	}
	// A clock stepped back does not make the new card maskable either.
	if got := Mask(f.db, "default", "back", "row: "+val); !strings.Contains(got, val) {
		t.Errorf("a new card with an old timestamp was masked: %q", got)
	}
}
