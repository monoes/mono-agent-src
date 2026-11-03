package stop

import "testing"

// A Release that fails after Started killed the child: the ticket must not stay STARTED.
func TestAReleaseErrorAfterStartedDoesNotLeaveTheTicketStarted(t *testing.T) {
	f := newGateFixture(t)
	held := &releaseFails{}
	tk, err := f.g.StartHeld(Spec{}, func() (Held, error) { return held, nil })
	if err == nil || !held.killed.Load() {
		t.Fatalf("err %v killed %v", err, held.killed.Load())
	}
	if got := f.g.State(tk); got != Aborted {
		t.Fatalf("state %v, want ABORTED (the child is dead)", got)
	}
	if _, ok := f.reg.abortedApproval(tk.ID()); !ok {
		t.Fatal("the abort was not recorded")
	}
}

type releaseFails struct{ fakeHeld }

func (h *releaseFails) Release() error { return errInjected }
