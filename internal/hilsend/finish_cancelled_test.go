package hilsend

import (
	"context"
	"encoding/json"
	"testing"
)

// A ctx that ends while the send runs must not leave the card in "sending":
// the outcome is written with a ctx that outlives the cancel.
func TestResolveFinishesTheCardAfterTheContextIsCancelled(t *testing.T) {
	f := newHeldFix(t, anyHost())
	id := f.hold(t, "key "+heldValue)
	ctx, cancel := context.WithCancel(f.ctx)
	run := func(context.Context, Run) (json.RawMessage, error) {
		cancel()
		return json.RawMessage(`{}`), nil
	}
	if _, err := Resolve(ctx, f.st, id, true, Edit{}, false, run); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	p, err := f.st.Get(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if p.State != StateSent {
		t.Fatalf("card left %q after the ctx was cancelled", p.State)
	}
}
