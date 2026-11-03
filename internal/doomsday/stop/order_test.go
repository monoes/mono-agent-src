package stop

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

var bg = context.Background()

func newStopper(t *testing.T, fsys FS) (*Stopper, *fakeRunStore, *fakeEnder, RunDir) {
	t.Helper()
	dir := newRunDir(t, fsys)
	mustNoErr(t, dir.CreateSecret(7))
	st := newFakeRunStore()
	en := &fakeEnder{store: st}
	return &Stopper{Dir: dir, Store: st, Ender: en, Host: noHost{}, Clock: newStepClock(), Retries: 3, Unrecorded: &UnrecordedFlag{}}, st, en, dir
}

func joined(r Report) string { return strings.Join(r.Lines(), "\n") }

func TestStopHappyPathIsQuiescentAndOrdered(t *testing.T) {
	s, st, _, dir := newStopper(t, nil)
	var order []string
	dir.Hook = func(p string) {}
	s.Hook = func(p string) {
		if p == "marker.recorded" {
			order = append(order, "marker")
			st.record("MARKER-SEEN")
		}
	}
	rep := s.Stop(bg, 7, "ch1", "operator")
	if !rep.Stopped() || rep.ExitCode() != 0 || !strings.Contains(joined(rep), "stopped (quiescent)") {
		t.Fatalf("report: %s", joined(rep))
	}
	want := []string{"MARKER-SEEN", "advance:stop-requested", "revoke", "cancel", "advance:authority-revoked", "end", "advance:quiescent"}
	if strings.Join(st.log, ",") != strings.Join(want, ",") {
		t.Fatalf("order\n got %v\nwant %v", st.log, want)
	}
	if s.Unrecorded.IsSet() {
		t.Fatal("the stop-unrecorded flag must be cleared once marker and intent are durable")
	}
}

// (a) the marker write fails: NOT RECORDED, still steps 2-4, never "stopped".
func TestStopMarkerFailureIsNotRecordedButContinues(t *testing.T) {
	fsys := newFaultFS()
	s, st, en, dir := newStopper(t, fsys)
	fsys.failCreate["7.stop"] = true
	g := NewGate(GateConfig{Generation: 7, Reg: newFakeRegistry(), Clock: s.Clock})
	s.Gate = g
	rep := s.Stop(bg, 7, "ch1", "operator")
	out := joined(rep)
	if !strings.Contains(out, "NOT RECORDED") || rep.Stopped() || rep.ExitCode() == 0 || strings.Contains(out, "generation 7 stopped") {
		t.Fatalf("report: %s", out)
	}
	if !g.Sealed() || en.calls != 1 || !contains(st.log, "revoke") {
		t.Fatalf("the later steps must still run: sealed=%v end=%d log=%v", g.Sealed(), en.calls, st.log)
	}
	if !s.Unrecorded.IsSet() {
		t.Fatal("the flag must stay set while the marker is not durable")
	}
	_ = dir
}

// (b) the database intent fails: its own state is printed, the barrier is closed, nothing reports success.
func TestStopIntentFailureKeepsBarrierClosed(t *testing.T) {
	s, st, _, _ := newStopper(t, nil)
	st.failTo[StateStopRequested] = true
	g := NewGate(GateConfig{Generation: 7, Reg: newFakeRegistry(), Clock: s.Clock})
	s.Gate = g
	rep := s.Stop(bg, 7, "ch1", "operator")
	if rep.Stopped() || !strings.Contains(joined(rep), "database intent: not done") || !g.Sealed() {
		t.Fatalf("report: %s", joined(rep))
	}
	if st.advCalls < 3 {
		t.Fatalf("bounded retry: %d attempts", st.advCalls)
	}
	if !s.Unrecorded.IsSet() {
		t.Fatal("flag must stay set")
	}
}

// (c) the revocation fails: "authority not yet revoked", not quiescent.
func TestStopRevocationFailure(t *testing.T) {
	s, st, _, _ := newStopper(t, nil)
	st.failRev = true
	rep := s.Stop(bg, 7, "ch1", "operator")
	if rep.Stopped() || !strings.Contains(joined(rep), "authority not yet revoked") {
		t.Fatalf("report: %s", joined(rep))
	}
	if st.state == StateQuiescent || st.state == StateCleanupPending {
		t.Fatalf("state %s moved past a failed revocation", st.state)
	}
}

func TestStopSurvivorKeepsCleanupPendingAndNeverSaysStopped(t *testing.T) {
	s, st, en, _ := newStopper(t, nil)
	en.out = UnitOutcome{Kind: UnitStragglers, Survivors: []Survivor{{PID: 99, Comm: "tool", Sub: "t-1", Why: "D state"}}}
	rep := s.Stop(bg, 7, "ch1", "operator")
	out := joined(rep)
	if rep.Stopped() || st.state != StateCleanupPending || !strings.Contains(out, "survivor: pid 99") || strings.Contains(out, "generation 7 stopped") {
		t.Fatalf("state %s\n%s", st.state, out)
	}
}

func TestStopUnknownProcessAndLiveMembersBlockQuiescent(t *testing.T) {
	s, st, en, _ := newStopper(t, nil)
	en.out = UnitOutcome{Kind: UnitEndedWithUnknown, Unknown: []Survivor{{PID: 5, Why: "no registry row"}}}
	rep := s.Stop(bg, 7, "ch1", "operator")
	if rep.Stopped() || st.state != StateCleanupPending || !strings.Contains(joined(rep), "unknown process: pid 5") {
		t.Fatalf("%s %s", st.state, joined(rep))
	}
	s2, st2, _, _ := newStopper(t, nil)
	st2.members = []string{"session:abc"}
	rep = s2.Stop(bg, 7, "ch1", "operator")
	if rep.Stopped() || st2.state != StateCleanupPending || !strings.Contains(joined(rep), "live member: session:abc") {
		t.Fatalf("%s %s", st2.state, joined(rep))
	}
}

func TestStopUnprovenUnitIsReportedNotStopped(t *testing.T) {
	s, st, en, _ := newStopper(t, nil)
	en.out = UnitOutcome{Kind: UnitNotProven, Note: "boom"}
	rep := s.Stop(bg, 7, "ch1", "operator")
	if rep.Stopped() || st.state != StateCleanupPending || !strings.Contains(joined(rep), "identity not proven, nothing was signalled") {
		t.Fatalf("%s", joined(rep))
	}
}

// The barrier closes at the marker, before the database intent (one ordering for every writer).
func TestStopSealsGateBeforeDatabaseIntent(t *testing.T) {
	s, st, _, _ := newStopper(t, nil)
	g := NewGate(GateConfig{Generation: 7, Reg: newFakeRegistry(), Clock: s.Clock})
	s.Gate = g
	var sealedAtIntent bool
	st.onAdvance = func(to RunState) {
		if to == StateStopRequested {
			sealedAtIntent = g.Sealed()
		}
	}
	rep := s.Stop(bg, 7, "ch1", "operator")
	if !sealedAtIntent || !rep.Stopped() {
		t.Fatalf("sealed at intent=%v\n%s", sealedAtIntent, joined(rep))
	}
}

// Q11: a begun merge finishes at Stop (its sub-unit is excluded from the kill and the run is not quiescent
// until it has); an unbegun one never starts.
func TestStopExcludesBegunOperationAndStaysPending(t *testing.T) {
	s, st, en, _ := newStopper(t, nil)
	fc := newFakeClock()
	s.Clock = fc
	reg := newFakeRegistry()
	g := NewGate(GateConfig{Generation: 7, Reg: reg, Clock: fc})
	s.Gate = g
	first := startOpOn(t, g)
	done := make(chan Report, 1)
	go func() { done <- s.Stop(bg, 7, "ch1", "operator") }()
	waitSealed(t, g)
	fc.Advance(DefaultTClose)
	var rep Report
	select {
	case rep = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return")
	}
	if len(en.exclude) != 1 || en.exclude[0] != first.ID() || rep.Stopped() || st.state != StateCleanupPending {
		t.Fatalf("exclude=%v state=%s\n%s", en.exclude, st.state, joined(rep))
	}
	if rep.Gate == nil || rep.Gate.Kind != ClosedWithOperation {
		t.Fatalf("gate outcome %+v", rep.Gate)
	}
	if _, err := g.AdmitContinuation("other", "x"); err == nil {
		t.Fatal("an unbegun operation must not start")
	}
}

func startOpOn(t *testing.T, g *Gate) *Ticket {
	t.Helper()
	tk, err := g.StartHeld(Spec{Op: "merge", Steps: []string{"a", "b"}}, func() (Held, error) { return &fakeHeld{}, nil })
	mustNoErr(t, err)
	return tk
}

// A hung starter makes the host terminate itself (crash-only), continuation starters included.
func TestEnforceHungExits(t *testing.T) {
	fc := newFakeClock()
	g := NewGate(GateConfig{Generation: 7, Reg: newFakeRegistry(), Clock: fc})
	tk := mustAdmitStart(t, g, Spec{Kind: KindE1})
	got := make(chan []uint64, 1)
	go EnforceHung(bg, g, fc, time.Second, func(h []uint64) { got <- h })
	for i := 0; i < 40; i++ {
		fc.Advance(time.Second)
		select {
		case h := <-got:
			if len(h) != 1 || h[0] != tk.ID() {
				t.Fatalf("hung %v", h)
			}
			return
		case <-time.After(5 * time.Millisecond):
		}
	}
	t.Fatal("the host never terminated itself")
}

// "stopped" is never printed for a report with an unrecorded step, whatever the state says.
func TestReportStoppedRequiresEveryStep(t *testing.T) {
	r := Report{Generation: 1, Final: StateQuiescent, Steps: []Step{{Name: StepMarker, Done: false, Err: errInjected}}}
	if r.Stopped() || r.ExitCode() == 0 {
		t.Fatal("an unrecorded step must prevent Stopped")
	}
	if !strings.Contains(joined(r), "NOT RECORDED") || strings.Contains(joined(r), "generation 1 stopped") {
		t.Fatalf("%s", joined(r))
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// ----- outside dispatchers -----

type fakeOutside struct {
	refuse bool
	reg    []string
	ended  []string
}

func (f *fakeOutside) RegisterAdmitted(_ context.Context, _ uint64, id string) error {
	if f.refuse {
		return errors.New("run is not running")
	}
	f.reg = append(f.reg, id)
	return nil
}
func (f *fakeOutside) Ended(_ context.Context, _ uint64, id string) error {
	f.ended = append(f.ended, id)
	return nil
}

func TestOutsideDispatchRegistersThenReadsMarker(t *testing.T) {
	dir := newRunDir(t, nil)
	mustNoErr(t, dir.CreateSecret(7))
	reg := &fakeOutside{}
	acted := false
	mustNoErr(t, OutsideDispatch(bg, reg, dir, 7, "ch", "o1", func() error { acted = true; return nil }))
	if !acted || len(reg.ended) != 1 {
		t.Fatalf("acted=%v ended=%v", acted, reg.ended)
	}
	// T26: registered, then refused by the marker: it writes ended itself and does not act.
	_, err := dir.WriteMarker(7, "ch", "x")
	mustNoErr(t, err)
	reg2 := &fakeOutside{}
	acted = false
	if err := OutsideDispatch(bg, reg2, dir, 7, "ch", "o2", func() error { acted = true; return nil }); !errors.Is(err, ErrOutsideRefused) || acted {
		t.Fatalf("err %v acted %v", err, acted)
	}
	if len(reg2.ended) != 1 {
		t.Fatal("a refused dispatcher must write ended itself, otherwise it blocks quiescence")
	}
	// T18: registration refused after Stop's commit.
	reg3 := &fakeOutside{refuse: true}
	if err := OutsideDispatch(bg, reg3, dir, 7, "ch", "o3", func() error { acted = true; return nil }); !errors.Is(err, ErrOutsideRefused) {
		t.Fatalf("err %v", err)
	}
	if len(reg3.ended) != 0 {
		t.Fatal("nothing registered, nothing to end")
	}
}

// ----- markers -----

func TestMarkerIdempotentUnsignedCorruptAndStale(t *testing.T) {
	dir := newRunDir(t, nil)
	mustNoErr(t, dir.CreateSecret(7))
	r1, err := dir.WriteMarker(7, "ch", "op")
	mustNoErr(t, err)
	r2, err := dir.WriteMarker(7, "ch", "op")
	if err != nil || !r2.Existed || r2.Status != MarkerValid || r1.Status != MarkerValid {
		t.Fatalf("repeat: %+v %v", r2, err)
	}
	// stale marker of an old generation cannot stop a new one
	if dir.ReadMarker(8, "ch2") != MarkerAbsent {
		t.Fatal("a marker of generation 7 stopped generation 8")
	}
	// the same bytes placed under another generation's name are corrupt for it (fails closed there)
	b, _ := os.ReadFile(dir.StopPath(7))
	mustNoErr(t, os.WriteFile(dir.StopPath(9), b, 0o600))
	if dir.ReadMarker(9, "") != MarkerCorrupt {
		t.Fatal("a copied marker must read corrupt for the other generation")
	}
	// corrupt content stops its generation
	mustNoErr(t, os.WriteFile(dir.StopPath(10), []byte("garbage"), 0o600))
	if s := dir.ReadMarker(10, ""); s != MarkerCorrupt || !s.Stops() {
		t.Fatalf("corrupt: %v", s)
	}
	res, err := dir.WriteMarker(10, "", "op") // repeated Stop hits the corrupt marker: reported, still a stop
	if err != nil || res.Status != MarkerCorrupt || !res.Existed {
		t.Fatalf("%+v %v", res, err)
	}
	// forged MAC
	mustNoErr(t, os.WriteFile(dir.StopPath(7), []byte(`{"v":1,"generation":7,"charter_id":"ch","cause":"op","mac":"00"}`), 0o600))
	if dir.ReadMarker(7, "ch") != MarkerCorrupt {
		t.Fatal("a marker with a wrong MAC must not read valid")
	}
}

// T14: an unreadable secret produces an unsigned marker that stops its generation.
func TestMarkerUnsignedWhenSecretUnreadable(t *testing.T) {
	fsys := newFaultFS()
	dir := newRunDir(t, fsys)
	mustNoErr(t, dir.CreateSecret(7))
	fsys.failRead["7.secret"] = true
	res, err := dir.WriteMarker(7, "ch", "op")
	if err != nil || res.Status != MarkerUnsigned || !res.Status.Stops() {
		t.Fatalf("%+v %v", res, err)
	}
	if dir.ReadMarker(7, "ch") != MarkerUnsigned {
		t.Fatal("the host must read it as unsigned and still stopped")
	}
	// a missing secret behaves the same
	dir2 := newRunDir(t, nil)
	res, err = dir2.WriteMarker(3, "ch", "op")
	if err != nil || res.Status != MarkerUnsigned {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestMarkerFsyncFailureIsNotRecordedAndLeavesNothing(t *testing.T) {
	for name, set := range map[string]func(*faultFS){
		"file fsync": func(f *faultFS) { f.failFileSyn = true },
		"dir fsync":  func(f *faultFS) { f.failDirSync = true },
	} {
		t.Run(name, func(t *testing.T) {
			fsys := newFaultFS()
			dir := newRunDir(t, fsys)
			mustNoErr(t, dir.CreateSecret(7))
			set(fsys)
			if _, err := dir.WriteMarker(7, "ch", "op"); err == nil {
				t.Fatal("a failed fsync must be an error (NOT RECORDED)")
			}
			if _, err := os.Stat(dir.StopPath(7)); err == nil {
				t.Fatal("a marker that is not durable must not be left behind as a record")
			}
		})
	}
}

func TestReconcileDisagreements(t *testing.T) {
	cases := []struct {
		db     RunState
		marker MarkerStatus
		want   Action
	}{
		{StateRunning, MarkerValid, ActionAdvanceDB},
		{StateRunning, MarkerCorrupt, ActionAdvanceDB},
		{StateRunning, MarkerUnsigned, ActionAdvanceDB},
		{StateStopRequested, MarkerAbsent, ActionRecreateMarker},
		{StateQuiescent, MarkerAbsent, ActionRecreateMarker},
		{StateRunning, MarkerAbsent, ActionNone},
		{StateCleanupPending, MarkerValid, ActionNone},
	}
	for _, c := range cases {
		if got := Reconcile(c.db, c.marker); got != c.want {
			t.Errorf("Reconcile(%s,%s) = %v, want %v", c.db, c.marker, got, c.want)
		}
	}
}

// noHost is the HostLink of a test with no host process: nothing holds the generation's gate.
type noHost struct{}

func (noHost) CloseHost(context.Context, uint64) HostOutcome { return HostOutcome{Kind: HostNone} }
