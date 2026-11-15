package account_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/account/accounttest"
)

// receive waits for the next status on ch.
func receive(t *testing.T, ch <-chan account.Status, what string) account.Status {
	t.Helper()
	select {
	case st := <-ch:
		return st
	case <-time.After(testWait):
		t.Fatalf("timed out waiting for %s", what)
		return account.Status{}
	}
}

func TestEveryOnRefusedCallbackFiresOncePerRefusal(t *testing.T) {
	e := newEnv(t)
	e.signIn(10*time.Minute, time.Hour)
	chans := make([]chan account.Status, 3)
	for i := range chans {
		ch := make(chan account.Status, 4)
		chans[i] = ch
		e.g.OnRefused(func(st account.Status) { ch <- st })
	}
	if st := e.g.Status(); st.State != account.StateOK {
		t.Fatalf("Status = %s/%q", st.State, st.Reason)
	}
	for refusal := 1; refusal <= 2; refusal++ {
		e.refuse()
		e.f.Clock.Advance(account.PollInterval)
		e.g.Status()
		e.g.Status()
		for i, ch := range chans {
			st := receive(t, ch, fmt.Sprintf("callback %d in refusal %d", i, refusal))
			if st.State != account.StateLocked || st.Reason != account.ReasonRefused {
				t.Fatalf("callback %d got %s/%q", i, st.State, st.Reason)
			}
		}
		quiet(t, e.loops...)
		for i, ch := range chans {
			if len(ch) != 0 {
				t.Fatalf("callback %d fired again in refusal %d", i, refusal)
			}
		}
		e.signIn(time.Minute, time.Hour) // a new sign-in ends the refusal
		e.f.Clock.Advance(account.PollInterval)
		if st := e.g.Status(); st.State != account.StateOK {
			t.Fatalf("after signing in again: %s/%q", st.State, st.Reason)
		}
	}
}

func TestALateOnRefusedCallbackIsCalledOnceWithTheRefusalAndNoOtherIsCalledAgain(t *testing.T) {
	e := newEnv(t)
	e.signIn(10*time.Minute, time.Hour)
	early := make(chan account.Status, 4)
	e.g.OnRefused(func(st account.Status) { early <- st })
	e.refuse()
	e.f.Clock.Advance(account.PollInterval)
	e.g.Status()
	receive(t, early, "the callback registered before the refusal")

	late := make(chan account.Status, 4)
	e.g.OnRefused(func(st account.Status) { late <- st })
	if st := receive(t, late, "the callback registered during the refusal"); st.State != account.StateLocked || st.Reason != account.ReasonRefused {
		t.Fatalf("the late callback got %s/%q, want locked/refused", st.State, st.Reason)
	}
	e.g.Status()
	quiet(t, e.loops...)
	if len(late) != 0 || len(early) != 0 {
		t.Fatalf("a callback was called again: %d for the late one, %d for the early one", len(late), len(early))
	}
}

// A refusal the guard noted can be over in the file before the guard has looked
// again. A callback registered then is called only if the refusal is still in
// effect once the guard has looked: it is not told of a refusal that has ended.
func TestALateOnRefusedCallbackIsNotCalledOnceTheRefusalHasEnded(t *testing.T) {
	for _, c := range []struct {
		name   string
		end    func(*env)
		state  account.State
		reason account.Reason
	}{
		{"another process signs in", func(e *env) { e.signIn(10*time.Minute, time.Hour) }, account.StateOK, ""},
		{"the login is removed", func(e *env) {
			if err := os.Remove(filepath.Join(e.dir, "session.json")); err != nil {
				e.t.Fatal(err)
			}
		}, account.StateLocked, account.ReasonNotLoggedIn},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.refuse()
			early := make(chan account.Status, 4)
			e.g.OnRefused(func(st account.Status) { early <- st })
			receive(t, early, "the refusal that was already stored")

			// The refusal ends in the file. This guard has not looked: its poll is
			// due, and nothing has called Status since.
			c.end(e)
			e.f.Clock.Advance(account.PollInterval)
			late := make(chan account.Status, 4)
			e.g.OnRefused(func(st account.Status) { late <- st })
			quiet(t, e.loops...)
			if len(late) != 0 {
				t.Fatalf("a callback registered after the refusal ended was called with %s", describeStatus(<-late))
			}
			if st := e.g.Status(); st.State != c.state || st.Reason != c.reason {
				t.Fatalf("Status = %s, want %s/%q", describeStatus(st), c.state, c.reason)
			}

			// The next refusal reaches both callbacks, once each.
			e.refuse()
			e.f.Clock.Advance(account.PollInterval)
			e.g.Status()
			receive(t, early, "the early callback in the next refusal")
			receive(t, late, "the late callback in the next refusal")
			quiet(t, e.loops...)
			if len(early) != 0 || len(late) != 0 {
				t.Fatalf("a callback was called twice for the next refusal (%d, %d extra)", len(early), len(late))
			}
		})
	}
}

// Another process's refusal is in the file before this guard has read anything:
// registering is enough to hear of it, with no Status call in between.
func TestARefusalAlreadyInTheFileFiresWhenACallbackIsRegistered(t *testing.T) {
	e := newEnv(t)
	e.refuse()
	got := make(chan account.Status, 4)
	e.g.OnRefused(func(st account.Status) { got <- st })
	if st := receive(t, got, "the refusal that was already stored"); st.State != account.StateLocked || st.Reason != account.ReasonRefused {
		t.Fatalf("the callback got %s/%q, want locked/refused", st.State, st.Reason)
	}
	quiet(t, e.loops...)
	if len(got) != 0 {
		t.Fatal("the callback was called twice for one refusal")
	}
}

// A refusal is one thing, every other way to be locked out another: only the
// first is ended by monoes.me, so only it tells OnRefused.
func TestOnRefusedIsToldOfRefusalsAndOfNothingElse(t *testing.T) {
	e := newEnv(t)
	fired := make(chan account.Status, 8)
	e.g.OnRefused(func(st account.Status) { fired <- st })
	step := func(what string, state account.State, reason account.Reason) {
		t.Helper()
		e.f.Clock.Advance(account.PollInterval)
		if st := e.g.Status(); st.State != state || st.Reason != reason {
			t.Fatalf("%s: Status = %s/%q, want %s/%q", what, st.State, st.Reason, state, reason)
		}
	}
	step("nothing stored", account.StateLocked, account.ReasonNotLoggedIn)
	e.corrupt()
	step("a file that is not a session", account.StateLocked, account.ReasonInvalid)
	e.signIn(10*time.Minute, time.Hour)
	step("a login", account.StateOK, "")
	e.f.Clock.Advance(time.Hour)
	step("an expired token inside the grace", account.StateGrace, account.ReasonUnreachable)
	e.f.Clock.Advance(24 * time.Hour)
	step("a login past the grace", account.StateLocked, account.ReasonExpired)
	e.storeToken(e.f.Token(accounttest.TokenOptions{KID: "rotated-key"}), e.f.Clock.Now())
	step("a key this build does not pin", account.StateLocked, account.ReasonKeyUnknown)
	e.storeToken(e.f.Token(accounttest.TokenOptions{IssuedAt: e.f.Clock.Now().Add(10 * time.Minute)}), time.Time{})
	step("a token from the future", account.StateLocked, account.ReasonClockSkew)
	sess := e.signIn(10*time.Minute, time.Hour)
	sess.HW = e.f.Clock.Now().Add(2 * time.Hour)
	e.save(sess)
	step("a clock set back", account.StateLocked, account.ReasonClockRollback)
	quiet(t, e.loops...)
	if len(fired) != 0 {
		t.Fatalf("OnRefused was called for a lockout that is not a refusal (%d times)", len(fired))
	}

	e.refuse()
	step("a refusal", account.StateLocked, account.ReasonRefused)
	receive(t, fired, "the refusal, to prove the callback is live")
}

// A callback is its own goroutine, started after the guard's locks are let go:
// a slow one holds nothing up, not the call that found the refusal, not the next
// callback, and one may take a lock that the caller of Status holds or ask the
// guard about itself.
func TestASlowOnRefusedCallbackBlocksNothing(t *testing.T) {
	e := newEnv(t)
	e.signIn(10*time.Minute, time.Hour)
	release := make(chan struct{})
	var once sync.Once
	releaseAll := func() { once.Do(func() { close(release) }) }
	t.Cleanup(releaseAll)
	started := make(chan struct{}, 4)
	slow := func(account.Status) { started <- struct{}{}; <-release }
	var held sync.Mutex
	asked := make(chan account.Status, 4)
	e.g.OnRefused(slow) // first in line
	e.g.OnRefused(func(account.Status) {
		held.Lock() // the lock the caller of Status holds while it asks
		//lint:ignore SA2001 waiting for the lock is the point: the caller of Status holds it
		held.Unlock()
		asked <- e.g.Status()
	})
	if st := e.g.Status(); st.State != account.StateOK {
		t.Fatalf("Status = %s/%q", st.State, st.Reason)
	}
	e.refuse()
	e.f.Clock.Advance(account.PollInterval)

	returned := make(chan struct{})
	go func() {
		held.Lock()
		defer held.Unlock()
		e.g.Status()
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(testWait):
		t.Fatal("Status did not return while OnRefused callbacks were running")
	}
	if st := receive(t, asked, "the callback behind a slow one"); st.State != account.StateLocked || st.Reason != account.ReasonRefused {
		t.Fatalf("the guard answered %s/%q to a callback, want locked/refused", st.State, st.Reason)
	}
	select {
	case <-started:
	case <-time.After(testWait):
		t.Fatal("the slow callback was never called")
	}

	// A callback registered during the refusal is called at once, and its
	// registration does not wait for it either.
	registered := make(chan struct{})
	go func() {
		e.g.OnRefused(slow)
		close(registered)
	}()
	select {
	case <-registered:
	case <-time.After(testWait):
		t.Fatal("OnRefused waited for the callback it called")
	}
	select {
	case <-started:
	case <-time.After(testWait):
		t.Fatal("a callback registered during a refusal was not called")
	}
	releaseAll()
}

// A daemon calls Status from many goroutines while another process signs in and
// out, and callbacks are registered as it runs. Run with -race: the guard's own
// locks are what is under test. The goroutines register together, before any of
// them touches a lock, so that nothing but the guard's own lock orders the
// registrations for the race detector.
func TestAGuardIsSafeForConcurrentUse(t *testing.T) {
	e := newEnv(t)
	e.signIn(10*time.Minute, time.Hour)
	g := e.newGuard(0)
	start := make(chan struct{})
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			g.OnRefused(func(account.Status) {})
			for {
				select {
				case <-stop:
					return
				default:
				}
				g.Status()
				g.Require(context.Background())
			}
		}()
	}
	close(start)
	for i := range 20 {
		if i%2 == 0 {
			e.refuse()
		} else {
			e.signIn(time.Minute, time.Hour)
		}
		e.f.Clock.Advance(account.PollInterval)
	}
	close(stop)
	wg.Wait()
	e.f.Clock.Advance(account.PollInterval)
	if st := g.Status(); st.State != account.StateOK {
		t.Fatalf("Status = %s/%q after the last sign-in, want ok", st.State, st.Reason)
	}
}
