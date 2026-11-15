package account_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/account/accounttest"
)

// A clock set back while the refresher follows a marker (A24). The refresher's backoff runs on
// the guard's clock: after a step back its next retry comes as much later in real time as the
// step, and until then no pass looks at the marker, so the evidence that the clock went back (a
// clock before the last attempt) is never read while the clock is behind it. The refresher
// therefore compares each reading of the clock with its previous one: a reading more than
// clockBackTolerance (10 s) earlier, with a marker pending, is a clock set back. It raises the
// session's last attempt to the previous reading and passes at once, and that pass drops the
// token. monoes.me keeps its own clock in these tests: only the machine's is set back.

// steppedMachine is an install whose refresher runs on a clock of its own while monoes.me keeps
// its own: a clock set back moves the machine's only.
type steppedMachine struct {
	machine *accounttest.Clock
	m0      time.Time
	dir     string
	seal    account.Sealer
	store   account.Store
	srv     *windowServer
	net     *flakyNet
	g       *account.Guard
	server  *accounttest.Clock
}

// newSteppedMachine signs an install in with a token past its half-life (its refresher is due at
// once) and starts its refresher, whose grants meet fates.
func newSteppedMachine(t *testing.T, fates ...fate) *steppedMachine {
	t.Helper()
	f := accounttest.New(t) // f.Clock is monoes.me's clock
	m := &steppedMachine{dir: filepath.Join(t.TempDir(), "account"), seal: account.NewMemorySealer(), server: f.Clock}
	m.store = account.OpenStore(m.dir, m.seal)
	signInInstall(t, f, m.store, 57*time.Minute, time.Hour, "rt-1")
	m.srv = newWindowServer(f, 0, "rt-1")
	m.net = &flakyNet{srv: m.srv}
	m.net.then(fates...)
	m.machine = accounttest.NewClock(f.Clock.Now())
	m.m0 = m.machine.Now()
	m.g = account.NewGuard(account.GuardOptions{Store: account.OpenStore(m.dir, m.seal), Refresher: m.net, Now: m.machine.Now, Poll: loopPoll})
	t.Cleanup(m.g.Close)
	m.g.StartRefresher(context.Background())
	waitForGrants(t, m.net, 1, "the first attempt")
	return m
}

// both moves both clocks by d.
func (m *steppedMachine) both(d time.Duration) {
	m.server.Advance(d)
	m.machine.Advance(d)
}

// retryAt moves both clocks to +at from the first attempt, where the refresher's next retry is
// due, and waits for its grant.
func (m *steppedMachine) retryAt(t *testing.T, at time.Duration, grants int) {
	t.Helper()
	m.both(m.m0.Add(at).Sub(m.machine.Now()))
	waitForGrants(t, m.net, grants, "the retry at +"+at.String())
}

// markAt waits until the refresher has written the high-water mark at +at: it has read the clock
// there.
func (m *steppedMachine) markAt(t *testing.T, at time.Duration) {
	t.Helper()
	eventually(t, "the high-water write at +"+at.String(), func() bool {
		sess, err := m.store.Load()
		return err == nil && sess != nil && sess.HW.Equal(m.m0.Add(at))
	})
}

// droppedSoon waits until the refresher has dropped the token, as it must at its first pass after
// the clock went back.
func (m *steppedMachine) droppedSoon(t *testing.T, otherwise string) {
	t.Helper()
	eventually(t, "the refresher to drop the token at its first pass after the clock went back ("+otherwise+")", func() bool {
		sess, err := m.store.Load()
		return err == nil && sess != nil && sess.LastResult == "unconfirmed"
	})
}

func (m *steppedMachine) session(t *testing.T) *account.Session {
	t.Helper()
	sess, err := m.store.Load()
	if err != nil || sess == nil {
		t.Fatalf("session: found=%v, err=%v", sess != nil, err)
	}
	return sess
}

// dropped fails unless the token was dropped, never presented more than times times, and the
// account is whole.
func (m *steppedMachine) dropped(t *testing.T, times int) {
	t.Helper()
	if got := count(m.srv.presented(), "rt-1"); got != times || m.srv.isRevoked() {
		t.Fatalf("monoes.me was presented rt-1 %d times (revoked %t), want %d and the account whole: %v", got, m.srv.isRevoked(), times, m.srv.presented())
	}
	if sess := m.session(t); sess.LastResult != "unconfirmed" {
		t.Fatalf("stored session = %s, want the token dropped as unconfirmed", describe(sess))
	}
	if rt, _ := m.store.LoadRefresh(); rt != "" {
		t.Fatalf("refresh.enc holds %q, want the dead token gone", rt)
	}
}

// The finding of the second adversarial round: the refresher alone, no other process. Its answer
// is lost at +0 and again at its retries at +31 and +92 s; at +100 s the clock is set back 120 s.
// Its next retry was due at +212 s by the clock, real +332 s, past monoes.me's window.
func TestTheRefresherAloneDropsTheTokenWhenTheClockGoesBackBetweenItsRetries(t *testing.T) {
	m := newSteppedMachine(t, lost, lost, lost) // a fourth grant would reach monoes.me
	m.retryAt(t, 31*time.Second, 2)
	m.retryAt(t, 92*time.Second, 3) // the next retry is due at +212 s
	m.both(8 * time.Second)         // +100 s
	m.machine.Advance(-120 * time.Second)
	m.droppedSoon(t, "it would present the token at its retry, real +332 s")
	m.both(232 * time.Second) // the machine reads +212 s, the next retry; monoes.me +332 s
	quiet(t, m.g)
	m.dropped(t, 3)
}

// The same with a command: the refresher retried at +31, +92 and +213 s, the clock is set back
// 80 s at +250 s, and a command runs a minute later, once the clock has come past the last attempt
// it recorded (+213 s): the marker reads 230 s old, and the command would present the token at
// real +310 s. The refresher has dropped it at the step.
func TestARefresherDropsTheTokenAtAClockSetBackBeforeACommandComesAfterIt(t *testing.T) {
	m := newSteppedMachine(t, lost, lost, lost, lost)
	m.retryAt(t, 31*time.Second, 2)
	m.retryAt(t, 92*time.Second, 3)
	m.retryAt(t, 213*time.Second, 4)
	m.both(37 * time.Second)             // +250 s
	m.machine.Advance(-80 * time.Second) // the machine reads +170 s
	m.droppedSoon(t, "a command once the clock has come past +213 s would present it")
	m.both(time.Minute) // the machine reads +230 s, monoes.me +310 s
	cli := account.NewGuard(account.GuardOptions{Store: account.OpenStore(m.dir, m.seal), Refresher: m.srv, Now: m.machine.Now})
	t.Cleanup(cli.Close)
	if _, err := cli.EnsureFresh(context.Background()); err != nil {
		t.Fatalf("the command: %v", err)
	}
	m.dropped(t, 4)
}

// A step back of 30 s, more than the tolerance, is a clock set back too: the refresher drops the
// token at its next pass.
func TestARefresherTakesAStepBackBeyondTheToleranceForAClockSetBack(t *testing.T) {
	m := newSteppedMachine(t, lost, lost)
	m.retryAt(t, 31*time.Second, 2) // the next retry is due at +91 s
	m.both(29 * time.Second)        // +60 s
	m.markAt(t, 60*time.Second)
	m.machine.Advance(-30 * time.Second)
	m.droppedSoon(t, "a step back of 30 s is more than the tolerance")
	quiet(t, m.g)
	m.dropped(t, 2)
}

// A step back within the tolerance (the size of an NTP correction) is ignored: the marker is
// followed as it was, and the retry it was due for recovers the answer.
func TestARefresherIgnoresAStepBackWithinTheTolerance(t *testing.T) {
	m := newSteppedMachine(t, lost, lost)
	m.retryAt(t, 31*time.Second, 2) // the next retry is due at +91 s
	m.both(29 * time.Second)        // +60 s
	m.markAt(t, 60*time.Second)
	before := m.session(t)
	m.machine.Advance(-5 * time.Second) // the machine reads +55 s
	quiet(t, m.g)
	if sess := m.session(t); !sess.LastAttempt.Equal(before.LastAttempt) || !sess.PendingSince.Equal(m.m0) || sess.LastResult != before.LastResult || m.net.grants() != 2 {
		t.Fatalf("after a step back of 5 s: stored session = %s with %d grants, want it as it was, %s, and no new grant", describe(sess), m.net.grants(), describe(before))
	}
	m.both(36 * time.Second) // the machine reads +91 s: the retry
	waitForGrants(t, m.net, 3, "the retry the marker was due for")
	eventually(t, "the answer to be stored", func() bool {
		sess := m.session(t)
		return sess.LastResult == "ok" && sess.PendingSince.IsZero()
	})
	if m.srv.isRevoked() {
		t.Fatal("the account was revoked")
	}
}

// A clock that jumps forward is no clock set back: the marker is followed as it was.
func TestARefresherDoesNothingAtAClockThatJumpsForward(t *testing.T) {
	m := newSteppedMachine(t, lost, lost)
	m.retryAt(t, 31*time.Second, 2) // the next retry is due at +91 s
	waitForAttempt(t, m.store, m.m0.Add(31*time.Second), "the record of the retry")
	before := m.session(t)
	m.machine.Advance(20 * time.Second) // +51 s on the machine: no retry is due, no mark is stale
	quiet(t, m.g)
	if sess := m.session(t); !sess.LastAttempt.Equal(before.LastAttempt) || !sess.PendingSince.Equal(m.m0) || m.net.grants() != 2 {
		t.Fatalf("after a jump forward: stored session = %s with %d grants, want it as it was, %s, and no new grant", describe(sess), m.net.grants(), describe(before))
	}
}

// With no marker a clock set back changes nothing: the refresher waits for its next attempt as it
// did, and writes nothing.
func TestARefresherWithNoMarkerWaitsOutItsBackoffAfterAClockSetBack(t *testing.T) {
	m := newSteppedMachine(t, unsent, unsent) // settled failures: no marker
	m.retryAt(t, 31*time.Second, 2)           // the next attempt is due at +91 s
	m.both(29 * time.Second)                  // +60 s
	m.markAt(t, 60*time.Second)
	before := m.session(t)
	m.machine.Advance(-30 * time.Second)
	quiet(t, m.g)
	if sess := m.session(t); describe(sess) != describe(before) || m.net.grants() != 2 {
		t.Fatalf("after a clock set back with no marker: stored session = %s with %d grants, want it as it was, %s, and no new attempt", describe(sess), m.net.grants(), describe(before))
	}
}
