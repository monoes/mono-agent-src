package finish_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// Against the real store: the reasons the note names for a deadline, a moving subject and a missing network deny are in
// the closed list, and the harness uses them (they were internal-error and subject-changed before).

// T27: a finish that requires the network deny parks with enforcement-required where the platform cannot give it, and no
// criterion is run. Mutant tried: drop the RequireNetDeny check in runCriteria: the program runs and the test is red.
func TestRequiredNetworkDenyThatThePlatformCannotGiveParks(t *testing.T) {
	r := newRig(t, []store.Criterion{{Kind: "command", Spec: "echo hello", Expected: "hello"}})
	scratch := t.TempDir()
	r.h.NewSandbox = func(context.Context, int64) (finish.Sandbox, func(), error) {
		return finish.Sandbox{Scratch: scratch, RequireNetDeny: true, NetDenyAvailable: func() bool { return false },
			Place: func(*exec.Cmd) (func(), error) { r.placed++; return func() {}, nil }}, func() { r.ended++ }, nil
	}
	rep := r.advance()
	if rep.State != finish.StateParked || rep.Reason != finish.ReasonEnforcementRequired {
		t.Fatalf("%+v", rep)
	}
	if r.placed != 0 || r.rows("finish.evidence") != 0 || r.rows("finish.commit") != 0 || r.ended != 1 {
		t.Fatalf("placed=%d evidence=%d commit=%d ended=%d", r.placed, r.rows("finish.evidence"), r.rows("finish.commit"), r.ended)
	}
	if f, err := r.l.Facts(bg); err != nil || f.ParkReason != finish.ReasonEnforcementRequired {
		t.Fatalf("%v %+v", err, f.ParkReason)
	}
}

// Where the denial is available the same finish runs, and the evidence says "enforced". Where it is not, a finish that
// does not require it runs and the evidence says "declared-only".
func TestEvidenceRecordsWhetherTheNetworkDenyWasEnforced(t *testing.T) {
	for _, avail := range []bool{true, false} {
		r := newRig(t, []store.Criterion{{Kind: "command", Spec: "echo hello", Expected: "hello"}})
		scratch := t.TempDir()
		r.h.NewSandbox = func(context.Context, int64) (finish.Sandbox, func(), error) {
			return finish.Sandbox{Scratch: scratch, NetDenyAvailable: func() bool { return avail },
				Place: func(*exec.Cmd) (func(), error) { return func() {}, nil }}, func() {}, nil
		}
		if avail && !netNamespacesWork() {
			continue // the platform cannot do it; the declared case below is what is checked
		}
		rep := r.advance()
		if !rep.Done {
			t.Fatalf("avail=%v: %+v", avail, rep)
		}
		f, _ := r.l.Facts(bg)
		want := finish.NetDeclared
		if avail {
			want = finish.NetEnforced
		}
		var got string
		for _, e := range f.Evidence {
			if e.CriterionID != "" && e.Net != "" {
				got = e.Net
			}
		}
		if got != want {
			t.Fatalf("avail=%v: net = %q, want %q", avail, got, want)
		}
	}
}

func netNamespacesWork() bool {
	c := exec.Command("unshare", "-rn", "true")
	return c.Run() == nil
}

// A subject that keeps changing while it is snapshotted parks with subject-moving (it was subject-changed).
func TestAMovingSubjectParksWithSubjectMoving(t *testing.T) {
	r := newRig(t, []store.Criterion{{Kind: "file", Spec: "a.txt", Expected: "exists"}})
	r.h.FreezeTries = 1
	var stop atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; !stop.Load(); i++ {
			_ = os.WriteFile(filepath.Join(r.subject, "a.txt"), []byte(strconv.Itoa(i)), 0o644)
		}
	}()
	rep := r.advance()
	stop.Store(true)
	<-done
	if rep.State != finish.StateParked || rep.Reason != finish.ReasonSubjectMoving {
		t.Fatalf("%+v", rep)
	}
}

// Every reason of the note's closed list is accepted by the ledger's park; one outside the list is refused.
func TestTheLedgerAcceptsTheDeadlineAndEnforcementReasons(t *testing.T) {
	for _, reason := range []finish.Reason{finish.ReasonDeadline, finish.ReasonSubjectMoving, finish.ReasonEnforcementRequired} {
		r := newRig(t, []store.Criterion{{Kind: "file", Spec: "a.txt", Expected: "exists"}})
		if _, err := r.l.Freeze(bg, "d1", ""); err != nil {
			t.Fatal(err)
		}
		if err := r.l.Park(bg, finish.Verdict{Outcome: finish.Park, Reason: reason, Detail: "x"}); err != nil {
			t.Fatalf("%s: %v", reason, err)
		}
		if f, _ := r.l.Facts(bg); f.ParkReason != reason {
			t.Fatalf("%s: read back %q", reason, f.ParkReason)
		}
	}
	r := newRig(t, []store.Criterion{{Kind: "file", Spec: "a.txt", Expected: "exists"}})
	if _, err := r.l.Freeze(bg, "d1", ""); err != nil {
		t.Fatal(err)
	}
	if err := r.l.Park(bg, finish.Verdict{Outcome: finish.Park, Reason: "made-up"}); err == nil {
		t.Fatal("a reason outside the closed list was recorded")
	}
}
