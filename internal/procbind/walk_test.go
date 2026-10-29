package procbind

import (
	"fmt"
	"testing"
)

// tree builds init(1) -> W(100, start 10) -> M(200, 20) -> C(300, 30).
func tree() (w *world, W, M, C *fproc) {
	w = &world{}
	W = w.spawn(100, 1, 10)
	M = w.spawn(200, 100, 20)
	C = w.spawn(300, 200, 30)
	return
}

func walk(w *world, caller, worker *fproc) Result {
	return Walk(fprocs{w}, w.handle(caller), w.handle(worker))
}

func TestWalkAllowsTheWorkerItselfAndItsDescendants(t *testing.T) {
	w, W, M, C := tree()
	for _, c := range []struct {
		name   string
		caller *fproc
	}{{"the worker itself", W}, {"a child", M}, {"a grandchild", C}} {
		if r := walk(w, c.caller, W); !r.Allowed {
			t.Errorf("%s: %v", c.name, r)
		}
	}
}

func TestWalkDeniesAnOutsider(t *testing.T) {
	w, W, _, _ := tree()
	z := w.spawn(500, 1, 5)
	s := w.spawn(400, 500, 40) // a sibling tree under another parent
	if r := walk(w, s, W); r.Allowed || r.Reason != ReasonReparented {
		t.Errorf("sibling: %v", r)
	}
	if r := walk(w, z, W); r.Allowed {
		t.Errorf("an unrelated process: %v", r)
	}
	// an ancestor of the worker is not a descendant of it
	w2, W2, _, _ := tree()
	if r := Walk(fprocs{w2}, w2.handle(W2), w2.handle(w2.spawn(900, 1, 1))); r.Allowed {
		t.Errorf("the worker walking to an unrelated worker: %v", r)
	}
}

func TestWalkDepthBoundary(t *testing.T) {
	// a chain of n processes below the worker: allowed up to MaxHops-1 below, denied beyond
	for _, c := range []struct {
		below int
		allow bool
	}{{MaxHops - 1, true}, {MaxHops, false}, {MaxHops + 6, false}} {
		w := &world{}
		W := w.spawn(100, 1, 1)
		prev := W
		for i := 1; i <= c.below; i++ {
			prev = w.spawn(100+i, prev.pid, uint64(1+i))
		}
		r := walk(w, prev, W)
		if r.Allowed != c.allow {
			t.Errorf("%d below the worker: %v", c.below, r)
		}
		if !c.allow && r.Reason != ReasonNoMatch {
			t.Errorf("%d below: reason %q", c.below, r.Reason)
		}
	}
}

func TestWalkDeniesAParentThatStartedAfterItsChild(t *testing.T) {
	w, W, M, C := tree()
	M.start = 99
	if r := walk(w, C, W); r.Allowed || r.Reason != ReasonStartOrder {
		t.Errorf("%v", r)
	}
	_ = W
}

func TestWalkFailsClosedOnUnreadableTable(t *testing.T) {
	for _, op := range []string{"parent", "start", "open", "pid"} {
		w, W, _, C := tree()
		w.fail = map[string]error{op: errFake}
		if r := walk(w, C, W); r.Allowed {
			t.Errorf("%s unreadable: %v", op, r)
		}
	}
}

var errFake = fmt.Errorf("unreadable")

func TestWalkClosesWhatItOpened(t *testing.T) {
	w, W, _, C := tree()
	if r := walk(w, C, W); !r.Allowed {
		t.Fatal(r)
	}
	if w.closed != 2 { // M and W's twin, opened by number on the way up
		t.Errorf("closed %d handles, want 2", w.closed)
	}
	w2, W2, M2, C2 := tree()
	M2.start = 99
	w2.closed = 0
	_ = walk(w2, C2, W2)
	if w2.closed != 1 {
		t.Errorf("a denied walk closed %d handles, want 1", w2.closed)
	}
}

// matchCall is the index of the call that completed the match in a clean walk: the last read of a
// process number. Everything after it is the final validation. The cutoff comes from the call
// sequence of the search, not from which checks the final validation happens to make, so a walk
// that drops its final validation is judged by the same cutoff.
func matchCall(w *world) int {
	m := -1
	for i, c := range w.log {
		if c.op == "pid" {
			m = i
		}
	}
	return m
}

// An exit of any process of the chain before the search ends must deny, wherever it falls.
func TestWalkExitAtEveryCall(t *testing.T) {
	base, W, _, C := tree()
	if r := walk(base, C, W); !r.Allowed {
		t.Fatal(r)
	}
	cutoff := matchCall(base)
	if cutoff < 8 {
		t.Fatalf("suspiciously short search: %d", cutoff)
	}
	for name, idx := range map[string]int{"caller": 2, "middle": 1, "worker": 0} {
		for k := 0; k <= cutoff; k++ {
			w, Wk, _, Ck := tree()
			victim := w.procs[idx]
			w.hook = func(n int) {
				if n == k {
					w.exit(victim)
				}
			}
			if r := walk(w, Ck, Wk); r.Allowed {
				t.Errorf("exit of the %s before call %d was allowed", name, k)
			}
		}
	}
}

// readStable and sameLive bracket their reads by number with liveness checks.
func TestReadStableDeniesAnExitBetweenTheReadsAndTheCheck(t *testing.T) {
	w, _, _, C := tree()
	w.hook = func(n int) {
		// the liveness check that follows the reads: the process is gone by then
		if n > 0 && w.log[len(w.log)-1].op == "alive" && w.log[len(w.log)-2].op == "start" {
			w.exit(C)
		}
	}
	if _, _, r := readStable(fprocs{w}, w.handle(C)); r != ReasonExited {
		t.Errorf("reason %q, want an exit", r)
	}
}

func TestSameLiveDeniesAnExitAfterTheNumbersAreRead(t *testing.T) {
	w, W, _, C := tree()
	pids := 0
	w.hook = func(n int) {
		if w.log[len(w.log)-1].op == "pid" {
			if pids++; pids == 2 {
				w.exit(C) // after the first number was read, as the second is read
			}
		}
	}
	if _, r := sameLive(w.handle(C), w.handle(W)); r != ReasonExited {
		t.Errorf("reason %q, want an exit", r)
	}
}

// The worker's number is recycled by an unrelated process at any point, and the middle process's
// number is taken over by a later process that the caller is then reparented to.
func TestWalkRecycledNumbersAtEveryCall(t *testing.T) {
	base, W, _, C := tree()
	walk(base, C, W)
	cutoff := matchCall(base)
	for k := 0; k <= cutoff; k++ {
		w, Wk, Mk, Ck := tree()
		w.hook = func(n int) {
			if n == k {
				w.exit(Wk)
				w.spawn(100, 1, 99) // an unrelated process now holds the worker's number
			}
		}
		if r := walk(w, Ck, Wk); r.Allowed {
			t.Errorf("worker number recycled before call %d: %v", k, r)
		}
		w, Wk, Mk, Ck = tree()
		w.hook = func(n int) {
			if n == k {
				w.exit(Mk)
				w.spawn(200, 1, 99) // a later process takes the number
				Ck.ppid = 200       // and the caller is reparented to it
			}
		}
		if r := walk(w, Ck, Wk); r.Allowed {
			t.Errorf("middle number recycled and taken over before call %d: %v", k, r)
		}
	}
}

// The caller moves under a different parent between the two reads of its parent.
func TestWalkDeniesAParentThatMovesDuringTheRead(t *testing.T) {
	w, W, _, C := tree()
	other := w.spawn(250, 100, 15)
	moved := false
	w.hook = func(n int) {
		// after the first parent read (a "start" follows it) and before the second
		if !moved && n > 0 && w.log[len(w.log)-1].op == "open" {
			C.ppid = other.pid
			moved = true
		}
	}
	if r := walk(w, C, W); r.Allowed {
		t.Errorf("a caller that changed parent while it was read was allowed: %v", r)
	}
}
