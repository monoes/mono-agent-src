package hilsend

import (
	"context"
	"encoding/json"
	"testing"
)

// If the card cannot be updated after the send ran (a scrub keeps rewriting
// it under both tries of finish), Resolve says so: it never answers success
// while the card is stuck in "sending".
func TestResolveReportsACardItCouldNotFinish(t *testing.T) {
	f := newHeldFix(t, anyHost())
	id := f.hold(t, "key "+heldValue)
	// Every update that would finish the card is skipped, as a row rewritten
	// under the read is: the guarded UPDATE touches no row.
	if _, err := f.db.Exec(`CREATE TRIGGER skip_finish BEFORE UPDATE ON hil_pending
		WHEN OLD.status = 'sending' AND NEW.status IN ('sent','failed') BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	runs := 0
	run := func(context.Context, Run) (json.RawMessage, error) { runs++; return json.RawMessage(`{}`), nil }
	p, err := Resolve(f.ctx, f.st, id, true, Edit{}, false, run)
	if err == nil {
		t.Fatalf("Resolve answered success with the card still %q", p.State)
	}
	if runs != 1 {
		t.Errorf("the send ran %d times", runs)
	}
	// A second approval neither runs the send again nor reports success.
	if _, err := Resolve(f.ctx, f.st, id, true, Edit{}, false, run); runs != 1 {
		t.Errorf("a second approval ran the send again (%d runs, err %v)", runs, err)
	}
}
