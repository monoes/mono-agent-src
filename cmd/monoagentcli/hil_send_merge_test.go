package main

import "testing"

// An agent that runs the same send node again while its card still waits is
// told the same card: approving it sends once, and the same send after the
// card is resolved is a new decision, so a new card.
func TestIdenticalNodeRunSendsShareOneCardUntilItIsResolved(t *testing.T) {
	cfg := sendEnv(t)
	calls := swapRunner(t)
	first := queueViaNodeRun(t, cfg, sendConfig)
	if second := queueViaNodeRun(t, cfg, sendConfig); second != first {
		t.Fatalf("two cards for one send: %s and %s", first, second)
	}
	asOperatorProcess(t)
	if items, err := runHIL(t, cfg, "list"); err != nil || len(items) != 1 || items[0].ID != first {
		t.Fatalf("hil list: %v %+v", err, items)
	}
	for _, verb := range [][]string{{"resolve", first, "--approve"}, {"resolve", first, "--approve"}, {"approve", first}} {
		if _, err := runSendCLI(t, cfg, newHILCmd(cfg), verb...); err != nil {
			t.Fatalf("%v: %v", verb, err)
		}
	}
	if len(*calls) != 1 || (*calls)[0].ApprovalID != first {
		t.Fatalf("the shared card sent %d times: %+v", len(*calls), *calls)
	}

	after := queueViaNodeRun(t, cfg, sendConfig)
	if after == first {
		t.Fatal("a send queued after the card was sent joined it")
	}
	asOperatorProcess(t)
	if _, err := runSendCLI(t, cfg, newHILCmd(cfg), "resolve", after, "--reject"); err != nil {
		t.Fatal(err)
	}
	if last := queueViaNodeRun(t, cfg, sendConfig); last == after || last == first {
		t.Fatalf("a send queued after the card was rejected joined a resolved card: %s", last)
	}
	if len(*calls) != 1 {
		t.Fatalf("sent %d times", len(*calls))
	}
}
