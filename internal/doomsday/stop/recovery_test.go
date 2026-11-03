package stop

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// ----- emergency channel (linux peer credentials) -----

func startEmergency(t *testing.T, dir RunDir, uid uint32) (stops chan uint64, stop func()) {
	t.Helper()
	stops = make(chan uint64, 4)
	srv := &EmergencyServer{Dir: dir, Gen: 7, UID: uid, OnStop: func(g uint64) { stops <- g }}
	ctx, cancel := context.WithCancel(bg)
	go srv.Serve(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(dir.SockPath(7)); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("socket never appeared")
		}
		time.Sleep(time.Millisecond)
	}
	return stops, cancel
}

func TestEmergencyChannel(t *testing.T) {
	dir := newRunDir(t, nil)
	mustNoErr(t, dir.CreateSecret(7))
	stops, cancel := startEmergency(t, dir, uint32(os.Getuid()))
	defer cancel()
	mustNoErr(t, SendEmergency(dir, 7))
	select {
	case g := <-stops:
		if g != 7 {
			t.Fatalf("gen %d", g)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OnStop not called")
	}
	// wrong secret: refused, no stop
	other := RunDir{Dir: dir.Dir + "/x"}
	mustNoErr(t, other.CreateSecret(7))
	bad := RunDir{Dir: other.Dir}
	if err := sendWithSockOf(bad, dir, 7); err == nil {
		t.Fatal("a message signed with another secret must be refused")
	}
	select {
	case <-stops:
		t.Fatal("an unauthenticated message stopped the run")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestEmergencyWrongPeerUIDRefused(t *testing.T) {
	dir := newRunDir(t, nil)
	mustNoErr(t, dir.CreateSecret(7))
	stops, cancel := startEmergency(t, dir, uint32(os.Getuid())+1)
	defer cancel()
	if err := SendEmergency(dir, 7); err == nil {
		t.Fatal("a peer with another uid must be refused")
	}
	select {
	case <-stops:
		t.Fatal("OnStop called for a foreign uid")
	case <-time.After(50 * time.Millisecond):
	}
}

// An unreadable secret is an explicit failure for both sides: the sender falls back, the receiver treats the
// generation as stop-requested.
func TestEmergencySecretUnreadable(t *testing.T) {
	fsys := newFaultFS()
	dir := newRunDir(t, fsys)
	mustNoErr(t, dir.CreateSecret(7))
	stops, cancel := startEmergency(t, dir, uint32(os.Getuid()))
	defer cancel()
	// receiver cannot read the secret
	fsys.failRead["7.secret"] = true
	good := RunDir{Dir: dir.Dir}
	if err := SendEmergency(good, 7); err == nil {
		t.Fatal("the receiver reported an explicit failure; the sender must see it")
	}
	select {
	case <-stops:
	case <-time.After(5 * time.Second):
		t.Fatal("an unreadable secret must still be treated as stop-requested (fail closed)")
	}
	// sender cannot read the secret
	if err := SendEmergency(dir, 7); !errors.Is(err, ErrSecretUnreadable) {
		t.Fatalf("sender: %v", err)
	}
}

func TestEmergencyHandlerSealsAndSetsFlag(t *testing.T) {
	g := NewGate(GateConfig{Generation: 7, Reg: newFakeRegistry(), Clock: newFakeClock()})
	flag := &UnrecordedFlag{}
	kicked := uint64(0)
	NewEmergencyHandler(g, flag, func(gen uint64) { kicked = gen })(7)
	if !g.Sealed() || !flag.IsSet() || kicked != 7 {
		t.Fatalf("sealed=%v flag=%v kicked=%d", g.Sealed(), flag.IsSet(), kicked)
	}
}

// ----- recovery -----

func TestT13RemovingCleanRecordFailsDoesNotStartWork(t *testing.T) {
	fsys := newFaultFS()
	dir := newRunDir(t, fsys)
	mustNoErr(t, dir.Shutdown(nil)) // a clean record exists
	fsys.failRemove = true
	if _, err := dir.StartDirty(); !errors.Is(err, ErrCannotStartWork) {
		t.Fatalf("err %v", err)
	}
	fsys.failRemove = false
	fsys.failDirSync = true
	if _, err := dir.StartDirty(); !errors.Is(err, ErrCannotStartWork) {
		t.Fatalf("fsync of the directory failed: %v", err)
	}
}

func TestStartDirtyReadsCleanOnlyOnce(t *testing.T) {
	dir := newRunDir(t, nil)
	if was, err := dir.StartDirty(); err != nil || was {
		t.Fatalf("first start: clean=%v %v", was, err)
	}
	mustNoErr(t, dir.Shutdown(nil))
	if was, _ := dir.StartDirty(); !was {
		t.Fatal("an orderly exit must read clean at the next start")
	}
	if was, _ := dir.StartDirty(); was {
		t.Fatal("the clean record must be gone (removed durably) after a start")
	}
}

// T12: marker and database both fail, then orderly exit (the fail-closed record cannot be written either),
// then power loss: the next start is dirty and refuses the run.
func TestT12UnrecordedStopThenOrderlyExitThenPowerLoss(t *testing.T) {
	fsys := newFaultFS()
	s, st, _, dir := newStopper(t, fsys)
	_, err := dir.StartDirty()
	mustNoErr(t, err)
	fsys.failCreate["7.stop"] = true
	st.failTo[StateStopRequested] = true
	rep := s.Stop(bg, 7, "ch1", "emergency")
	if rep.Stopped() || !s.Unrecorded.IsSet() {
		t.Fatal("neither the marker nor the intent is durable; the flag must be set")
	}
	fsys.failCreate["fail-closed"] = true
	if err := dir.Shutdown(s.Unrecorded); err == nil {
		t.Fatal("the fail-closed record cannot be written in this scenario")
	}
	if _, err := os.Stat(dir.cleanPath()); err == nil {
		t.Fatal("an orderly shutdown wrote clean while a stop was unrecorded")
	}
	// power loss; next start
	fresh := newFakeRunStore()
	fresh.runs = []RunInfo{{Generation: 7, CharterID: "ch1", State: StateRunning}}
	en := &fakeEnder{store: fresh}
	rc := Recoverer{Dir: RunDir{Dir: dir.Dir}, Store: fresh, Stopper: &Stopper{Dir: RunDir{Dir: dir.Dir}, Store: fresh, Ender: en, Clock: newStepClock()}}
	rr, err := rc.Recover(bg)
	mustNoErr(t, err)
	if rr.WasClean || len(rr.Resumed) != 1 || en.calls != 1 {
		t.Fatalf("the unclean loss must refuse the previously running generation: %+v", rr)
	}
}

// With the fail-closed record written, a following start is also unclean.
func TestFailClosedRecordReadsUnclean(t *testing.T) {
	dir := newRunDir(t, nil)
	flag := &UnrecordedFlag{}
	flag.Set()
	mustNoErr(t, dir.Shutdown(flag))
	if was, _ := dir.StartDirty(); was {
		t.Fatal("a fail-closed record must never read as clean")
	}
}

func TestRecoverCases(t *testing.T) {
	dir := newRunDir(t, nil)
	mustNoErr(t, dir.CreateSecret(1))
	mustNoErr(t, dir.CreateSecret(2))
	mustNoErr(t, dir.CreateSecret(3))
	_, err := dir.WriteMarker(2, "c2", "op") // gen 2: marker, database still running
	mustNoErr(t, err)
	mustNoErr(t, dir.Shutdown(nil)) // clean exit
	st := newFakeRunStore()
	st.runs = []RunInfo{
		{Generation: 1, CharterID: "c1", State: StateRunning},        // clean start, no marker: left alone
		{Generation: 2, CharterID: "c2", State: StateRunning},        // marker wins: resumes Stop
		{Generation: 3, CharterID: "c3", State: StateCleanupPending}, // non-running state: resumes Stop
	}
	en := &fakeEnder{store: st}
	rc := Recoverer{Dir: dir, Store: st, Stopper: &Stopper{Dir: dir, Store: st, Ender: en, Clock: newStepClock()},
		Unbound: func(context.Context) []string { return []string{"monoagent-run-9-aa.service"} }}
	rr, err := rc.Recover(bg)
	mustNoErr(t, err)
	if !rr.WasClean || len(rr.Untouched) != 1 || rr.Untouched[0] != 1 || len(rr.Resumed) != 2 || len(rr.Unbound) != 1 {
		t.Fatalf("%+v", rr)
	}
}

func TestRecoverRefusesWhenRunsUnreadable(t *testing.T) {
	dir := newRunDir(t, nil)
	rc := Recoverer{Dir: dir, Store: errRuns{newFakeRunStore()}, Stopper: &Stopper{}}
	if _, err := rc.Recover(bg); err == nil {
		t.Fatal("recovery that cannot read the runs must not report success")
	}
}

// Clock rollback and an expired lease do not reopen admission: the persisted state is the fence (gate and
// marker never read the clock).
func TestMarkerStopsRegardlessOfClock(t *testing.T) {
	dir := newRunDir(t, nil)
	mustNoErr(t, dir.CreateSecret(7))
	_, err := dir.WriteMarker(7, "ch", "op")
	mustNoErr(t, err)
	old := time.Now().Add(-100 * 24 * time.Hour)
	mustNoErr(t, os.Chtimes(dir.StopPath(7), old, old))
	if !dir.ReadMarker(7, "ch").Stops() {
		t.Fatal("a marker must not expire")
	}
}

type errRuns struct{ *fakeRunStore }

func (errRuns) OpenRuns(context.Context) ([]RunInfo, error) { return nil, errInjected }

// sendWithSockOf signs with the secret of signer but dials the socket of target.
func sendWithSockOf(signer, target RunDir, gen uint64) error {
	secret, err := signer.ReadSecret(gen)
	if err != nil {
		return err
	}
	c, err := net.Dial("unix", target.SockPath(gen))
	if err != nil {
		return err
	}
	defer c.Close()
	fmt.Fprintf(c, "STOP %d %s\n", gen, emergencyMAC(secret, gen))
	reply, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return err
	}
	if strings.TrimSpace(reply) != "OK" {
		return errors.New(strings.TrimSpace(reply))
	}
	return nil
}
