package verifier

import (
	"context"
	"sync"
	"testing"
	"time"
)

// S7c1a seam (a): the opt-in is per check, on the request, never a static switch of the verifier.
func TestAllowUnconfinedIsPerRequestAndNeverBeatsAConfiner(t *testing.T) {
	needsRealSandbox(t)
	cmd := crit("c1", "command", "true", "exit:0")

	// no confiner, no opt-in: refused; with the opt-in on the request: runs, loudly
	r := newRig(t, true)
	r.cfg.NetDenyAvailable, r.cfg.Confiner = nil, nil
	if got := r.run(cmd); got.Outcome != OutcomeCannotCheck || got.Reason != ReasonNoConfinement {
		t.Fatalf("no opt-in: %s", dump(got))
	}
	r.unconfined = true
	if got := r.run(cmd); got.Outcome != OutcomePassedMachine || got.Containment != ContainmentUnconfined {
		t.Fatalf("opt-in on the request: %s", dump(got))
	}
	// the same checker, the next request without the opt-in: refused again (nothing is remembered)
	r.unconfined = false
	if got := r.run(cmd); got.Reason != ReasonNoConfinement {
		t.Fatalf("the opt-in leaked into the next request: %s", dump(got))
	}

	// a confiner whose self test fails is not a way into the opt-in (no silent fallback)
	r = newRig(t, true)
	r.cfg.NetDenyAvailable = nil
	r.conf.TestErr = context.DeadlineExceeded
	r.unconfined = true
	if got := r.run(cmd); got.Outcome != OutcomeCannotCheck || got.Reason != ReasonNoConfinement || r.conf.Placed != 0 {
		t.Fatalf("failed self test with the opt-in: %s", dump(got))
	}
	// a confiner that holds wins over the opt-in: the result says confined
	r = newRig(t, true)
	r.cfg.NetDenyAvailable = nil
	r.unconfined = true
	if got := r.run(cmd); got.Containment == ContainmentUnconfined || r.conf.Placed != 1 {
		t.Fatalf("a working confiner was ignored: %s", dump(got))
	}
}

// hider is a confiner whose per-check copy knows the live root.
type hider struct {
	FakeConfiner
	mu    sync.Mutex
	roots []string
	hid   *hidden
}

type hidden struct {
	FakeConfiner
	root string
}

func (h *hider) Hide(root string) Confiner {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.roots = append(h.roots, root)
	h.hid = &hidden{root: root}
	return h.hid
}

// S7c1a seam (b): a confiner that can hide the live root is asked to, per check, and the copy is what places the programs.
func TestACheckAsksTheConfinerToHideTheLiveRootAndPlacesWithTheCopy(t *testing.T) {
	needsRealSandbox(t)
	r := newRig(t, true)
	r.cfg.NetDenyAvailable = nil
	h := &hider{}
	r.cfg.Confiner = h
	got := r.run(crit("c1", "command", "true", "exit:0"))
	if got.Outcome != OutcomePassedMachine {
		t.Fatalf("%s", dump(got))
	}
	if len(h.roots) != 1 || h.roots[0] != r.root {
		t.Fatalf("Hide was called with %v, want the request's root %q", h.roots, r.root)
	}
	if h.Placed != 0 || h.hid == nil || h.hid.Placed != 1 {
		t.Fatalf("the program was placed by the unhidden confiner (%d) instead of the copy (%d)", h.Placed, h.hid.Placed)
	}
	if h.Tests != 1 {
		t.Fatalf("self test ran %d times on the original, want 1", h.Tests)
	}
	// a confiner without Hide still works as before
	r = newRig(t, true)
	r.cfg.NetDenyAvailable = nil
	if got := r.run(crit("c1", "command", "true", "exit:0")); got.Outcome != OutcomePassedMachine || r.conf.Placed != 1 {
		t.Fatalf("%s", dump(got))
	}
}

// S7c1a seam (c): a cancelled check removes its scratch area promptly and does not leave the copy running.
func TestACancelledCheckLeavesNoScratchAreaBehind(t *testing.T) {
	r := newRig(t, true)
	k := r.checker()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := k.Run(ctx, r.req(fileExists("c1")))
	if err != nil || res.Outcome != OutcomeAbandoned {
		t.Fatalf("%v %s", err, dump(res))
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(scratchEntries(t, r.base)) != 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if e := scratchEntries(t, r.base); len(e) != 0 {
		t.Fatalf("left behind: %v", e)
	}
}
