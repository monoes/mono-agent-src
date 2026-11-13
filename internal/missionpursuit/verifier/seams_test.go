package verifier

import (
	"context"
	"errors"
	"os/exec"
	"strings"
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

// P3: a cancellation that arrives while the snapshot is finishing is "abandoned: cancelled", never a snapshot or digest complaint.
func TestACancellationDuringTheSnapshotIsAbandonedNotRefusedOrStale(t *testing.T) {
	// the cancel arrives while the copy finishes, and the check looks at its channels only after the copy has ended, so both the
	// finished snapshot and the cancellation are ready: whichever the select takes, the answer is "abandoned: cancelled"
	for i := 0; i < 24; i++ {
		r := newRig(t, true)
		ctx, cancel := context.WithCancel(context.Background())
		r.cfg.afterCopy = cancel // the copy is done, the live digest is next
		r.cfg.beforeWait = func() { time.Sleep(20 * time.Millisecond) }
		k := r.checker()
		res, err := k.Run(ctx, r.req(fileExists("c1")))
		if err != nil || res.Outcome != OutcomeAbandoned || res.Reason != ReasonCancelled {
			t.Fatalf("run %d: %v %s", i, err, dump(res))
		}
		for deadline := time.Now().Add(5 * time.Second); len(scratchEntries(t, r.base)) != 0 && time.Now().Before(deadline); {
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// infraConf reports, per Place, what its unit did wrong (a unit that never started, killed from outside): the criterion must not
// read that as the program's exit code.
type infraConf struct {
	FakeConfiner
	mu    sync.Mutex
	takes int
	infra []string
}

func (c *infraConf) TakeInfra() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.takes++
	out := c.infra
	c.infra = nil
	return out
}

func (c *infraConf) Place(cmd *exec.Cmd) (func(), error) {
	rel, err := c.FakeConfiner.Place(cmd)
	return func() {
		c.mu.Lock()
		c.infra = append(c.infra, "the unit did not start (status 226)")
		c.mu.Unlock()
		rel()
	}, err
}

func TestAnInfrastructureFailureOfTheConfinerIsAnErrorNotACriterionVerdict(t *testing.T) {
	needsRealSandbox(t)
	r := newRig(t, true)
	r.cfg.NetDenyAvailable = nil
	c := &infraConf{}
	r.cfg.Confiner = c
	// "false" with exit:1 would PASS as a program; the unit never having started must not let it
	got := r.run(crit("c1", "command", "false", "exit:1"))
	cr := got.Criteria[0]
	if cr.Result != ResError || got.Outcome != OutcomeError || !strings.Contains(cr.Detail, "confinement") || !strings.Contains(cr.Detail, "226") {
		t.Fatalf("%s", dump(got))
	}
	if c.takes < 2 {
		t.Fatalf("the failures are cleared before the run and read after it (%d takes)", c.takes)
	}
	// without the failure, the same criterion passes as a program
	r = newRig(t, true)
	r.cfg.NetDenyAvailable = nil
	if got := r.run(crit("c1", "command", "false", "exit:1")); got.Criteria[0].Result != ResPass {
		t.Fatalf("%s", dump(got))
	}
}

// The reason a self test failed is in the result, bounded and stripped, beside the closed reason.
func TestASelfTestFailureKeepsItsReasonInTheResult(t *testing.T) {
	r := newRig(t, true)
	r.conf.TestErr = errors.New("canary check failed: C4 (secrets by path): broken: the program ran and was not refused\x1b[31m")
	got := r.run(crit("c1", "command", "true", "exit:0"))
	if got.Reason != ReasonNoConfinement || !strings.Contains(got.ConfinementDetail, "C4") || strings.ContainsAny(got.ConfinementDetail, "\x1b") || !strings.Contains(got.Message(), "C4") {
		t.Fatalf("%s %q", dump(got), got.Message())
	}
}
