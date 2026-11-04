//go:build doomsday

package doomsday

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/doomsday/governor"
)

func fillTwo(t *testing.T, r *govRig) {
	t.Helper()
	for _, id := range []string{"a", "b"} {
		tn, err := r.startTurn(id)
		if err != nil {
			t.Fatal(err)
		}
		go func() { _ = tn.Wait() }()
	}
}

type panicKeys struct {
	inner governor.Keyer
	on    atomic.Bool
}

func (k *panicKeys) MAC(d string, p ...[]byte) ([]byte, error) {
	if k.on.Load() {
		panic("keyer boom")
	}
	return k.inner.MAC(d, p...)
}

// A panic in a tick is recovered and must leave the admission lock free: the next launch returns.
func TestATickPanicDoesNotWedgeAdmission(t *testing.T) {
	r := newGovRig(t, "")
	pk := &panicKeys{inner: r.gw.Keys}
	r.gw.Keys = pk
	r.start(t)
	fillTwo(t, r)
	if _, err := r.startTurn("c"); !errors.Is(err, coderhost.ErrDeferred) {
		t.Fatal(err)
	}
	pk.on.Store(true)
	r.gw.Tick(context.Background())
	pk.on.Store(false)
	done := make(chan error, 1)
	go func() { _, err := r.startTurn("d"); done <- err }()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("a launch after a recovered tick panic blocks forever: the admission lock stayed held")
	}
}

func sendRow(t *testing.T, r *govRig, conv, turn string) {
	t.Helper()
	if _, err := r.host.Enqueue(coderhost.Command{ProfileID: "p1", ConversationID: conv, Kind: coderhost.KindSend, TurnID: turn, Message: "go"}); err != nil {
		t.Fatal(err)
	}
}

// A deferred send that is cancelled afterwards leaves no reservation: the pump cancels its row instead of pinning a slot.
func TestACancelledDeferredSendPinsNoSlot(t *testing.T) {
	r := newGovRig(t, "")
	r.start(t)
	fillTwo(t, r)
	sendRow(t, r, "conv-c", "t-c")
	if _, err := r.startTurn("c"); !errors.Is(err, coderhost.ErrDeferred) {
		t.Fatal(err)
	}
	if _, err := r.host.CancelPendingSends("p1", "conv-c", 1<<40, "cancelled by the person"); err != nil {
		t.Fatal(err)
	}
	r.l.turn(0).end()
	waitUntil(t, "a freed", func() bool { return r.liveSlots(t) == 1 })
	if _, err := r.startTurn("d"); err != nil {
		t.Fatalf("a live send was deferred behind a cancelled one (live slots %d): %v", r.liveSlots(t), err)
	}
	if n := count(t, r.db, `SELECT COUNT(*) FROM doomsday_queue WHERE state = 'queued'`); n != 0 {
		t.Fatalf("%d rows still queued", n)
	}
}

// After a host restart the same pending send must not add a second queue row for the same launch.
func TestARestartedHostDoesNotEnqueueTheSameSendTwice(t *testing.T) {
	r := newGovRig(t, "")
	r.start(t)
	fillTwo(t, r)
	if _, err := r.startTurn("c"); !errors.Is(err, coderhost.ErrDeferred) {
		t.Fatal(err)
	}
	gw2 := &GovernorWire{DB: r.db.DB, Profile: "p1", Keys: r.gw.Keys, Runs: r.gw.Runs, RunDir: r.gw.RunDir, Sampler: r.smp,
		Alive: func(conv, turn string) bool { return conv == "conv-a" || conv == "conv-b" }}
	l2 := gw2.GovernLauncher(r.l)
	if _, err := l2.Start("p1", "conv-c", "t-c", "hi", nil, ""); !errors.Is(err, coderhost.ErrDeferred) {
		t.Fatal(err)
	}
	if n := count(t, r.db, `SELECT COUNT(*) FROM doomsday_queue WHERE payload LIKE '%conv-c|t-c%'`); n != 1 {
		t.Fatalf("%d queue rows for one send", n)
	}
}

// A send picked up after the claim time, with its slot still reserved for it, starts: it is not refused (the host would
// mark it failed) and not deferred behind a slot of its own.
func TestALateClaimStartsTheSend(t *testing.T) {
	r := newGovRig(t, "")
	r.start(t)
	fillTwo(t, r)
	for _, id := range []string{"c", "d", "e"} { // e stays queued, so the queue is not empty at the late claim
		if _, err := r.startTurn(id); !errors.Is(err, coderhost.ErrDeferred) {
			t.Fatal(err)
		}
	}
	r.l.turn(0).end()
	r.l.turn(1).end()
	waitUntil(t, "a, b freed", func() bool { return r.liveSlots(t) == 0 })
	r.gw.Tick(context.Background()) // the pump reserves for c and d
	rg := r.gw.cur
	m := memberID("conv-c", "t-c")
	rg.liveMu.Lock()
	if _, ok := rg.admitted[m]; !ok {
		rg.liveMu.Unlock()
		t.Fatalf("not reserved: %v", rg.admitted)
	}
	rg.admitted[m] = time.Now().Add(-govClaimTTL - time.Second)
	rg.liveMu.Unlock()
	if _, err := r.startTurn("c"); err != nil {
		t.Fatalf("a late claim: %v", err)
	}
	if r.liveSlots(t) != 2 {
		t.Fatalf("%d slots for c and d", r.liveSlots(t))
	}
}

// A reservation nobody claims in time stops counting as live, and the reconcile gives its slot back.
func TestAnUnclaimedReservationIsGivenBackAfterTheClaimTime(t *testing.T) {
	r := newGovRig(t, "")
	r.start(t)
	fillTwo(t, r)
	if _, err := r.startTurn("c"); !errors.Is(err, coderhost.ErrDeferred) {
		t.Fatal(err)
	}
	r.l.turn(0).end()
	waitUntil(t, "a freed", func() bool { return r.liveSlots(t) == 1 })
	r.gw.Tick(context.Background())
	if r.liveSlots(t) != 2 {
		t.Fatalf("the pump did not reserve for c: %d slots", r.liveSlots(t))
	}
	rg := r.gw.cur
	m := memberID("conv-c", "t-c")
	rg.liveMu.Lock()
	rg.admitted[m] = time.Now().Add(-govClaimTTL - time.Second)
	rg.liveMu.Unlock()
	rg.mu.Lock()
	rg.lastRec = time.Time{}
	rg.mu.Unlock()
	// the queue row is gone (admitted) and nobody claims: the next pass frees the slot
	r.gw.Tick(context.Background())
	if r.liveSlots(t) != 1 {
		t.Fatalf("%d slots: the unclaimed reservation was kept past the claim time", r.liveSlots(t))
	}
}

type countKeys struct {
	inner governor.Keyer
	n     atomic.Int64
}

func (k *countKeys) MAC(d string, p ...[]byte) ([]byte, error) {
	k.n.Add(1)
	return k.inner.MAC(d, p...)
}

// Waiting sends that are asked again on every host poll with nothing changed do not each pump and verify the queue.
func TestWaitingSendsAreNotPumpedOnEveryPoll(t *testing.T) {
	r := newGovRig(t, "")
	ck := &countKeys{inner: r.gw.Keys}
	r.gw.Keys = ck
	r.start(t)
	fillTwo(t, r)
	ids := []string{"c", "d", "e", "f", "g", "h", "i", "j"}
	for _, id := range ids {
		if _, err := r.startTurn(id); !errors.Is(err, coderhost.ErrDeferred) {
			t.Fatal(err)
		}
	}
	before := ck.n.Load()
	for pass := 0; pass < 5; pass++ {
		for _, id := range ids {
			if _, err := r.startTurn(id); !errors.Is(err, coderhost.ErrDeferred) {
				t.Fatal(err)
			}
		}
	}
	if used := ck.n.Load() - before; used > int64(len(ids)) {
		t.Fatalf("%d MAC computations for 5 unchanged polls of %d waiting sends", used, len(ids))
	}
}

// A slot freed makes the next ask pump at once (coalescing never delays real progress).
func TestACoalescedPumpStillServesAFreedSlotAtOnce(t *testing.T) {
	r := newGovRig(t, "")
	fillTwoThenWaitC := func() {
		r.start(t)
		fillTwo(t, r)
		if _, err := r.startTurn("c"); !errors.Is(err, coderhost.ErrDeferred) {
			t.Fatal(err)
		}
		if _, err := r.startTurn("c"); !errors.Is(err, coderhost.ErrDeferred) {
			t.Fatal(err)
		}
	}
	fillTwoThenWaitC()
	r.l.turn(0).end()
	waitUntil(t, "a freed", func() bool { return r.liveSlots(t) == 1 })
	if _, err := r.startTurn("c"); err != nil {
		t.Fatalf("the freed slot was not served at once: %v", err)
	}
}

type blockKeys struct {
	inner   governor.Keyer
	on      atomic.Bool
	release chan struct{}
	once    sync.Once
}

func (k *blockKeys) MAC(d string, p ...[]byte) ([]byte, error) {
	if k.on.Load() {
		<-k.release
	}
	return k.inner.MAC(d, p...)
}
func (k *blockKeys) free() { k.once.Do(func() { close(k.release) }) }

// A wedged governor (a key that never answers) does not delay Stop's own steps: the cancel runs on its own goroutine.
func TestAWedgedGovernorDoesNotDelayStopsSteps(t *testing.T) {
	r := newGovRig(t, "")
	r.start(t)
	fillTwo(t, r)
	if _, err := r.startTurn("c"); !errors.Is(err, coderhost.ErrDeferred) {
		t.Fatal(err)
	}
	bk := &blockKeys{inner: r.gw.Keys, release: make(chan struct{})}
	defer bk.free()
	bk.on.Store(true)
	g := GovernedTokens{Inner: &countingTokens{}, DB: r.db.DB, Profile: "p1", Keys: bk}
	start := time.Now()
	if err := g.CancelQueue(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("CancelQueue held Stop's step for %v behind a wedged governor", d)
	}
}

// The unclamped, unauthenticated report row cannot reach the terminal or the operator as anything but plain, bounded text.
func TestAForgedReportRowIsShownAsPlainBoundedText(t *testing.T) {
	r := newGovRig(t, "")
	r.start(t)
	_, _ = r.startTurn("a")
	r.gw.Tick(context.Background())
	forged := `{"at":` + itoa(time.Now().Unix()) + `,"cap":99,"effective":1,"admitting":true,"reasons":["\u001b[31mevil","load"],` +
		`"providers":[{"provider":"\u001b]0;pwn\u0007name","value":9999,"left_secs":99999999999}]}`
	if _, err := r.db.DB.Exec(`UPDATE doomsday_governor_state SET value = ? WHERE key = 'snapshot'`, forged); err != nil {
		t.Fatal(err)
	}
	v, err := GovernorReader{DB: r.db.DB, Profile: "p1", Runs: r.w.Chained()}.Read(context.Background(), 0)
	if err != nil || !v.HostReporting {
		t.Fatalf("%v %+v", err, v)
	}
	var out strings.Builder
	if err := renderGovernor(&out, v); err != nil {
		t.Fatal(err)
	}
	for _, c := range out.String() {
		if c < ' ' && c != '\n' {
			t.Fatalf("a control character reached the output: %q", out.String())
		}
	}
	for _, rs := range v.Reasons {
		if rs != "load" {
			t.Fatalf("an unknown reason was shown: %q", rs)
		}
	}
	for _, p := range v.Providers {
		if p.Value > v.Cap || p.LeftSecs > 3600 || len(p.Provider) > 64 {
			t.Fatalf("a provider line is not bounded: %+v", p)
		}
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// The stop marker is read inside the run view the pump uses: a marker with the run row still running admits nothing.
func TestTheRunViewReadsTheStopMarker(t *testing.T) {
	r := newGovRig(t, "")
	r.start(t)
	_, _ = r.startTurn("a")
	rg := r.gw.cur
	if ok, err := rg.Running(context.Background(), "p1", 1); err != nil || !ok {
		t.Fatalf("running before the marker: %v %v", ok, err)
	}
	if _, err := r.w.RunDir.WriteMarker(1, "", "test"); err != nil {
		t.Fatal(err)
	}
	if ok, err := rg.Running(context.Background(), "p1", 1); err != nil || ok {
		t.Fatalf("the run view says running with the stop marker set: %v %v", ok, err)
	}
}
