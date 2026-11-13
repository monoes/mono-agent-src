package doomsday

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
)

const unboundWhy = "this machine cannot run it: the kernel cannot tell which process connected"

// Where binding is off because the machine cannot run the service, a start goes ahead with the warning, and the run's audit
// stream carries a run.unbound row with the reason. Mutants: drop the UnboundWarning check in RequireBinding (the start is
// refused: red); drop the AppendAudit in Enter (no row: red); drop the Warning on the Entry (output red).
func TestStartGoesUnboundWithAWarningAndAnAuditRowWhereBindingCannotRun(t *testing.T) {
	w, db := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	w.w.Binding = func(context.Context) (bool, error) { return false, nil }
	w.w.UnboundReason = func(context.Context) string { return unboundWhy }
	e := w.w.Engine()
	w.deps.Engine = func() Engine { return e }
	ro, rerr := run(t, w.deps, "", "render", "--charter", w.charter)
	if rerr != nil {
		t.Fatal(rerr, ro)
	}
	h := strings.Fields(strings.SplitN(ro, "\n", 2)[0])[1]
	out, err := run(t, w.deps, "", "start", "--charter", w.charter, "--yes-i-lift", "--hash", h)
	if err != nil {
		t.Fatalf("an unbound start on a machine that cannot bind was refused: %v", err)
	}
	if want := "warning: running without peer-credential binding: " + unboundWhy; strings.Count(out, want) < 2 { // before the charter and after the start
		t.Fatalf("the warning is not shown before and after the start: %q", out)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM doomsday_runs`); n != 1 {
		t.Fatalf("%d run rows", n)
	}
	var payload string
	if err := db.DB.QueryRow(`SELECT payload FROM doomsday_audit WHERE kind = 'run.unbound'`).Scan(&payload); err != nil || !strings.Contains(payload, "kernel cannot tell") {
		t.Fatalf("no run.unbound audit row with the reason: %q %v", payload, err)
	}
}

// Off where the machine COULD bind (an explicit off) still refuses; and a bound start writes no unbound row.
func TestStartStillRefusesAnOffWhereTheMachineCouldBind(t *testing.T) {
	w, _ := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	w.w.Binding = func(context.Context) (bool, error) { return false, nil }
	w.w.UnboundReason = func(context.Context) string { return "" }
	e := w.w.Engine()
	if err := e.RequireBinding(context.Background()); err == nil || !strings.Contains(err.Error(), "no run without binding") {
		t.Fatalf("an explicit off on a capable machine must refuse: %v", err)
	}
	if e.UnboundWarning(context.Background()) != "" {
		t.Fatal("a capable machine has no unbound warning")
	}
}

type unboundEngine struct {
	bindingEngine
	warn string
}

func (u *unboundEngine) UnboundWarning(context.Context) string { return u.warn }

func TestProbeAndStatusCarryTheUnboundWarning(t *testing.T) {
	const msg = "running without peer-credential binding: " + unboundWhy
	r := probeOf(t, &unboundEngine{warn: msg})
	if !r.ModeAvailable || r.Warning != msg {
		t.Fatalf("probe: %+v", r)
	}
	if r := probeOf(t, &unboundEngine{}); r.Warning != "" {
		t.Fatalf("a bound machine got a warning: %+v", r)
	}
	d := newRig().deps
	e := &bindingEngine{status: Status{Active: true, RunID: "p/1", State: "running"}}
	d.Engine = func() Engine { return e }
	d.UnboundWarning = func(context.Context) string { return msg }
	out, err := run(t, d, "", "status")
	if err != nil || !strings.Contains(out, "warning: "+msg) {
		t.Fatalf("status text: %q %v", out, err)
	}
	d.JSONOutput = func() bool { return true }
	out, _ = run(t, d, "", "status")
	var s Status
	if json.Unmarshal([]byte(out), &s) != nil || s.Warning != msg {
		t.Fatalf("status json: %q", out)
	}
	d.UnboundWarning = nil
	out, _ = run(t, d, "", "status")
	if strings.Contains(out, "warning") {
		t.Fatalf("a status with no warning source printed one: %q", out)
	}
}

// The host gate lets an unbound turn of the run through only while UnboundReason says the machine cannot bind; nothing else
// about the turn's gate changes (it still takes its ticket). Mutants: drop unboundAllowed in BeforeRelease or requireBound: red.
func TestHostGateAllowsAnUnboundTurnOnlyWhereTheMachineCannotBind(t *testing.T) {
	off := &atomic.Bool{}
	var why atomic.Value
	why.Store("")
	r := newHookRig(t, off)
	r.gate.UnboundReason = func() string { return why.Load().(string) }
	r.start(t)
	r.hl.bound = false
	if err := r.startTurn(); !refused(err) {
		t.Fatalf("an unbound turn on a machine that could bind was not refused: %v", err)
	}
	why.Store(unboundWhy)
	if err := r.startTurn(); err != nil || r.hl.released != 1 {
		t.Fatalf("the unbound turn was refused where the machine cannot bind: %v released=%d", err, r.hl.released)
	}
	if n := r.tickets(t); n != 1 {
		t.Fatalf("the unbound turn must still take its ticket: %d", n)
	}
}
