package account_test

import (
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/account"
)

// A nil callback is ignored. Registered, it would be called on a goroutine of its
// own at the first refusal and panic there, and a panic on a goroutine that
// nobody recovers ends the whole process: a daemon would die of a nil argument.
// In each case a callback registered after the nil one is the witness that the
// guard went on working, and the wait that follows gives a nil callback that was
// registered the time to crash.
func TestANilOnRefusedCallbackIsIgnored(t *testing.T) {
	t.Run("registered before the refusal", func(t *testing.T) {
		e := newEnv(t)
		e.signIn(10*time.Minute, time.Hour)
		got := make(chan account.Status, 4)
		e.g.OnRefused(nil)
		e.g.OnRefused(func(st account.Status) { got <- st })
		if st := e.g.Status(); st.State != account.StateOK {
			t.Fatalf("Status = %s/%q, want ok", st.State, st.Reason)
		}
		e.refuse()
		e.f.Clock.Advance(account.PollInterval)
		e.g.Status()
		receive(t, got, "the callback registered after the nil one")
		quiet(t, e.loops...)
		if len(got) != 0 {
			t.Fatal("the callback was called twice for one refusal")
		}
	})

	t.Run("registered while a refusal is in effect", func(t *testing.T) {
		e := newEnv(t)
		e.refuse()
		first := make(chan account.Status, 4)
		e.g.OnRefused(func(st account.Status) { first <- st })
		receive(t, first, "the refusal that was already stored") // the guard has noted it
		e.g.OnRefused(nil)                                       // the guard calls a callback at once when a refusal is in effect
		late := make(chan account.Status, 4)
		e.g.OnRefused(func(st account.Status) { late <- st })
		receive(t, late, "the callback registered after the nil one, during the refusal")
		quiet(t, e.loops...)
	})

	t.Run("registered while a refusal is stored that the guard has not read", func(t *testing.T) {
		e := newEnv(t)
		e.refuse()
		e.g.OnRefused(nil) // registering reads the session, and a refusal fires every callback
		got := make(chan account.Status, 4)
		e.g.OnRefused(func(st account.Status) { got <- st })
		receive(t, got, "the callback registered after the nil one")
		quiet(t, e.loops...)
		if len(got) != 0 {
			t.Fatal("the callback was called twice for one refusal")
		}
	})
}
