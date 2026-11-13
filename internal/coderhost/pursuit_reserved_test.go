package coderhost

import (
	"strings"
	"testing"
)

// The pursuit- turn id is the host's own, like master- and answer-p-: a caller-supplied id with that prefix (the CLI's --turn, the MCP and
// the HTTP sends all end in Enqueue) is refused, whoever the caller says it is; only the pursuit loop's own queue functions take it.
// Revert proof: without the refusal in enqueueOn the forged send is queued.
func TestAPursuitTurnIDIsRefusedToACallerAndOnlyTheLoopsOwnQueueTakesIt(t *testing.T) {
	w := newPursuitWorld(t)
	for _, id := range []string{PursuitTurnPrefix + "x", PursuitTurnPrefix + "accept-1-x", PursuitTurnPrefix + "1-abc"} {
		for _, via := range map[string]func(Command) error{
			"Enqueue":       func(c Command) error { _, err := w.st.Enqueue(c); return err },
			"WithCheckin":   func(c Command) error { _, err := w.st.EnqueueWithCheckin(c, Checkin{}); return err },
			"issuer-tagged": func(c Command) error { c.Issuer = "m"; _, err := w.st.Enqueue(c); return err },
		} {
			err := via(Command{ProfileID: "p", ConversationID: "m", Kind: KindSend, TurnID: id, Message: "forged"})
			if err == nil || !strings.Contains(err.Error(), PursuitTurnPrefix) {
				t.Errorf("%s: a caller's %q was not refused: %v", id, id, err)
			}
		}
	}
	if len(w.wakes()) != 0 {
		t.Fatalf("a forged pursuit send is queued: %+v", w.wakes())
	}
	// the loop's own wake-up still queues
	w.firstEpoch()
	if len(w.wakes()) != 1 {
		t.Fatalf("the loop's own wake-up was refused: %d", len(w.wakes()))
	}
}

// A command that fails without a pursuit- turn id leaves the wake count alone: only a wake-up the loop counted is given back.
// Revert proof: without the prefix test in refundWake every failed send takes a count back.
func TestAFailedSendThatIsNoWakeUpLeavesTheWakeCountAlone(t *testing.T) {
	w := newPursuitWorld(t)
	w.queueWake()
	c, err := w.st.Enqueue(Command{ProfileID: "p", ConversationID: "m", Kind: KindSend, TurnID: "t-plain", Message: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := w.wakeCounters()
	h := &Host{Store: w.st, L: w.launched()}
	h.end(c, CmdFailed, "launch refused before any process")
	if after, _ := w.wakeCounters(); after != before {
		t.Fatalf("a failed plain send changed the wake count: %d -> %d", before, after)
	}
}
