package chat

import (
	"context"
	"errors"
	"github.com/monoes/mono-agent/internal/procbind"
	"strings"
	"testing"
	"time"
)

// The default of the peer-credential setting is ON where the machine can run the binding service. These tests fail if the
// default flips back to off, if an explicit off is lost, or if an unsupported machine is refused instead of degraded.
func machineBinding(t *testing.T, why string) {
	t.Helper()
	old := bindingReadiness
	bindingReadiness = func() string { return why }
	t.Cleanup(func() { bindingReadiness = old })
}

func TestBindingDefaultIsOnWhereTheMachineCanRunIt(t *testing.T) {
	machineBinding(t, "")
	if r := (coderSettings{}).resolveBinding(); !r.On || r.Explicit || r.Source != bindingSourceDefault {
		t.Fatalf("unset on a capable machine: %+v", r)
	}
	if r := (coderSettings{PeercredBinding: bindingSet(false)}).resolveBinding(); r.On || !r.Explicit {
		t.Fatalf("explicit off was not kept: %+v", r)
	}
	dbPath := newChatCLITestDB(t)
	out, code := runCoderCLI(t, dbPath, "status")
	if code != 0 || !strings.Contains(out, `"peercredBindingEffective":true`) || !strings.Contains(out, `"peercredBindingSource":"default"`) {
		t.Fatalf("status (%d): %s", code, out)
	}
}

// An explicit off is stored as false (not dropped like the old omitempty bool) and survives another setting's save, a reload
// and `coder set` of something else; only an explicit on changes it.
func TestExplicitBindingOffIsPreserved(t *testing.T) {
	machineBinding(t, "")
	asOperator(t)
	dbPath := newChatCLITestDB(t)
	if _, code := runCoderCLI(t, dbPath, "set", "--peercred-binding=off"); code != 0 {
		t.Fatalf("set off: exit %d", code)
	}
	if got := storedCoderJSON(t, dbPath); string(got["peercredBinding"]) != "false" {
		t.Fatalf("an explicit off is not stored: %v", got)
	}
	if _, code := runCoderCLI(t, dbPath, "set", "--box=compat"); code != 0 {
		t.Fatalf("set box: exit %d", code)
	}
	out, _ := runCoderCLI(t, dbPath, "status")
	if !strings.Contains(out, `"peercredBinding":false`) || !strings.Contains(out, `"peercredBindingEffective":false`) || !strings.Contains(out, `"peercredBindingSource":"set"`) {
		t.Fatalf("the explicit off did not survive: %s", out)
	}
	g, inner := gateRig(t, `{"enabled":true,"peercredBinding":false}`)
	if _, err := g.Start("p", "c", "t", "m", nil, ""); err != nil || len(inner.calls) != 1 {
		t.Fatalf("explicit off must pass through: %v %d", err, len(inner.calls))
	}
}

// Where the machine cannot run the service the default is the previous behaviour with the honest reason in `coder status`
// (text and JSON) and the host still starts a turn.
func TestBindingDefaultDegradesWhereUnsupported(t *testing.T) {
	machineBinding(t, "the kernel cannot tell which process connected")
	dbPath := newChatCLITestDB(t)
	out, code := runCoderCLI(t, dbPath, "status")
	if code != 0 || !strings.Contains(out, `"peercredBindingEffective":false`) || !strings.Contains(out, "the kernel cannot tell which process connected") {
		t.Fatalf("status (%d): %s", code, out)
	}
	if !strings.Contains(out, `"peercredBindingUnbound":"the kernel cannot tell which process connected"`) {
		t.Fatalf("status json lacks the unbound reason: %s", out)
	}
	if text := bindingStatusText(coderStatus{PeercredBindingSource: bindingSourceDefault, PeercredBindingNote: (coderSettings{}).resolveBinding().Note, PeercredBindingUnbound: "the kernel cannot tell which process connected"}); !strings.HasPrefix(text, "off (") || !strings.Contains(text, "cannot run it") || !strings.Contains(text, "running without peer-credential binding: the kernel cannot tell which process connected") {
		t.Fatalf("status text: %q", text)
	}
	g, inner := gateRig(t, `{"enabled":true}`)
	g.svc = &fakeService{}
	g.reason = func(bool) string { return "the kernel cannot tell which process connected" }
	if _, err := g.Start("p", "c", "t", "m", nil, ""); err != nil || len(inner.calls) != 1 {
		t.Fatalf("a start on an unsupported machine was refused: %v (calls %d)", err, len(inner.calls))
	}
	// an explicit on is still refused, never weakened
	g2, inner2 := gateRig(t, `{"enabled":true,"peercredBinding":true}`)
	g2.reason = g.reason
	if _, err := g2.Start("p", "c", "t", "m", nil, ""); err == nil || len(inner2.calls) != 0 {
		t.Fatalf("an explicit on went through on an unsupported machine: %v", err)
	}
}

// The default is on and capable, but the service fails to come up (or the host has no bound launcher): the turn still starts,
// unbound, with a logged reason; the host is never locked out by a default.
func TestBindingDefaultDegradesWhenTheServiceFailsToInitialise(t *testing.T) {
	machineBinding(t, "")
	g, inner := gateRig(t, `{"enabled":true}`)
	g.svc = &fakeService{}
	var logged []string
	g.logf = func(f string, a ...any) { logged = append(logged, f) }
	g.reason = func(running bool) string {
		if running {
			return ""
		}
		return "its service is not running"
	}
	g.svc = failingService{}
	if _, err := g.Start("p", "c", "t", "m", nil, ""); err != nil || len(inner.calls) != 1 {
		t.Fatalf("start refused although the default could not initialise: %v", err)
	}
	if len(logged) == 0 {
		t.Fatal("the degrade was not logged")
	}
	g2, inner2 := gateRig(t, `{"enabled":true,"peercredBinding":true}`)
	g2.svc, g2.reason = failingService{}, g.reason
	if _, err := g2.Start("p", "c", "t", "m", nil, ""); err == nil || len(inner2.calls) != 0 {
		t.Fatalf("an explicit on must still refuse: %v", err)
	}
}

type failingService struct{}

func (failingService) Ensure(_ context.Context, _ string) error { return errors.New("no socket") }
func (failingService) Running(string) bool                      { return false }

// A default that could not initialise degrades only when it is known that no open-ended run needs bound turns: when the run
// table cannot be read the turn is refused (fail closed) and the message says why. With an open run the legacy launcher is NOT
// handed out silently by the refuse path either. Revert proof: returning g.inner on an OpenRunCount error turns the first red.
func TestBindingDefaultFailsClosedAndSaysWhyWhenTheRunTableCannotBeRead(t *testing.T) {
	machineBinding(t, "")
	g, inner := gateRig(t, `{"enabled":true}`)
	g.svc = failingService{}
	g.reason = func(running bool) string {
		if running {
			return ""
		}
		return "its service is not running"
	}
	if _, err := g.db.Exec(`ALTER TABLE doomsday_runs RENAME TO doomsday_runs_x`); err != nil {
		t.Fatal(err)
	}
	_, err := g.Start("p", "c", "t", "m", nil, "")
	if err == nil || len(inner.calls) != 0 || !strings.Contains(err.Error(), "whether an open-ended run is open could not be read") {
		t.Fatalf("err=%v calls=%d", err, len(inner.calls))
	}
}

func addRun(t *testing.T, g *gatedLauncher, unbound bool) {
	t.Helper()
	if _, err := g.db.Exec(`INSERT OR IGNORE INTO profiles (id, name) VALUES ('p', 'P')`); err != nil {
		t.Fatal(err)
	}
	if _, err := g.db.Exec(`INSERT INTO doomsday_runs (profile, generation, charter_id, state, mac) VALUES ('p', 1, 'c1', 'running', x'01')`); err != nil {
		t.Fatal(err)
	}
	if unbound {
		if _, err := g.db.Exec(`INSERT INTO doomsday_audit (profile, generation, seq, kind, payload, prev, mac) VALUES ('p', 1, 1, 'run.unbound', 'unbound: x', x'00', x'00')`); err != nil {
			t.Fatal(err)
		}
	}
}

// A run that started BOUND is never handed the legacy launcher because the machine check fails during the run (route refuses),
// while a run recorded as unbound at its start, and no run at all, keep the legacy launcher. Revert proof: dropping boundRunsOpen
// in route() turns the first case red.
func TestRouteRefusesUnderABoundRunAndGoesOnUnderAnUnboundOne(t *testing.T) {
	machineBinding(t, "")
	for name, c := range map[string]struct {
		run, unbound bool
		refused      bool
	}{"no run": {false, false, false}, "bound run": {true, false, true}, "run that started unbound": {true, true, false}} {
		g, inner := gateRig(t, `{"enabled":true}`)
		g.reason = func(bool) string { return "the machine check failed once" } // flapping: capable at the start, not now
		if c.run {
			addRun(t, g, c.unbound)
		}
		_, err := g.Start("p", "c", "t", "m", nil, "")
		if c.refused && (err == nil || !strings.Contains(err.Error(), "doomsday stop")) { // the way out of a stuck bound run is named
			t.Fatalf("%s: the refusal does not name `doomsday stop`: %v", name, err)
		}
		if c.refused != (err != nil) || (c.refused && len(inner.calls) != 0) || (!c.refused && len(inner.calls) != 1) {
			t.Fatalf("%s: err=%v calls=%d", name, err, len(inner.calls))
		}
	}
}

// The START decision probes uncached, twice: one failed probe (a flapping temp-dir socket on Windows) gives a bound run on a machine
// that could bind; only two failures with the same reason give an unbound run with the warning. Revert proof: a single uncached probe
// (or the cached one) in startReadiness turns the first case red.
func TestTheStartDecisionNeedsTwoMatchingFailedProbes(t *testing.T) {
	procbind.ForTestSetIssuance(t, true)
	g, _ := gateRig(t, "")
	var pauses []time.Duration
	old := startProbePause
	startProbePause = func(d time.Duration) { pauses = append(pauses, d) } // no real sleeping
	t.Cleanup(func() { startProbePause = old })
	for name, c := range map[string]struct {
		probes   []error
		unbound  bool
		wantOn   bool
		wantCall int
	}{
		"one failure then success":   {[]error{errors.New("temp dir"), nil}, false, true, 2},
		"two matching failures":      {[]error{errors.New("temp dir"), errors.New("temp dir")}, true, false, 2},
		"two different failures":     {[]error{errors.New("temp dir"), errors.New("listen")}, false, true, 2},
		"success is asked only once": {[]error{nil}, false, true, 1},
	} {
		calls := 0
		pauses = nil
		procbind.ForTestSetReadyFunc(t, func() error { e := c.probes[calls%len(c.probes)]; calls++; return e })
		st, err := PeercredBindingState(g.db)
		if err != nil || st.On == c.unbound || (st.UnboundReason != "") != c.unbound || st.On != c.wantOn || calls != c.wantCall {
			t.Fatalf("%s: %+v calls=%d err=%v", name, st, calls, err)
		}
		if wantPause := c.wantCall - 1; len(pauses) != wantPause || (wantPause == 1 && pauses[0] != 250*time.Millisecond) { // a pause only between the two probes
			t.Fatalf("%s: pauses %v", name, pauses)
		}
	}
}
