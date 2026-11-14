package account

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The store puts keyStoreTimeout on every call into the key store, except the
// calls of the interactive keyring sealer, the one the explicit sign-in uses: it
// may ask for the file keyring's passphrase and its confirmation on the terminal,
// or wait for an unlock dialog, and a person takes as long as they take. These
// tests are in package account because the marker the store looks for and the
// keyring sealer's key function are not exported.

// testKEK stands in for the key store that the keyring sealer asks for its key:
// it counts the requests, waits for the gate when there is one, and answers with
// a fixed key. It never holds a real key.
type testKEK struct {
	key   []byte
	gate  chan struct{} // nil: answers at once
	delay time.Duration // how long a request takes before it answers, a loaded machine in small
	asked atomic.Int32
	once  sync.Once
}

func newTestKEK(gate chan struct{}) *testKEK {
	return &testKEK{key: bytes.Repeat([]byte{7}, keySize), gate: gate}
}

func (k *testKEK) get(bool) ([]byte, bool, error) {
	k.asked.Add(1)
	time.Sleep(k.delay)
	if k.gate != nil {
		<-k.gate
	}
	return k.key, true, nil
}

// slowKEK is a key store that answers, but later than the short timeout the tests
// below put on the store: what a hosted runner under load does to a call that is
// meant to be bounded.
func slowKEK() *testKEK {
	k := newTestKEK(nil)
	k.delay = 150 * time.Millisecond
	return k
}

// release lets the waiting request go, once; it is safe to call twice.
func (k *testKEK) release() {
	k.once.Do(func() {
		if k.gate != nil {
			close(k.gate)
		}
	})
}

// A person answers the interactive sealer, so its call is waited for however long
// the timeout is, and its result is returned.
//
// The short timeout is on only while the interactive call runs. The seeding before
// it and the check after it use the quiet sealer, which the timeout does bound, so
// on a loaded runner they would fail with "did not answer" for a reason that has
// nothing to do with what is pinned (issue 1650). slowKEK makes that so here.
func TestTheInteractiveSealersCallsAreWaitedForBeyondTheTimeout(t *testing.T) {
	cases := []struct {
		name string
		call func(Store) (string, error)
		want string
	}{
		{"LoadRefresh", func(st Store) (string, error) { return st.LoadRefresh() }, "rt-1"},
		{"SaveRefresh", func(st Store) (string, error) { return "", st.SaveRefresh("rt-2") }, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "account")
			if err := OpenStore(dir, keyringSealer{kek: slowKEK().get}).SaveRefresh("rt-1"); err != nil {
				t.Fatal(err)
			}
			kek := newTestKEK(make(chan struct{}))
			t.Cleanup(kek.release)
			st := OpenStore(dir, keyringSealer{kek: kek.get, mayPrompt: true})
			normal := keyStoreTimeout
			keyStoreTimeout = 50 * time.Millisecond
			restore := func() { keyStoreTimeout = normal }
			t.Cleanup(restore)
			type result struct {
				token string
				err   error
			}
			done := make(chan result, 1)
			go func() {
				token, err := c.call(st)
				done <- result{token, err}
			}()
			if !waitUntil(func() bool { return kek.asked.Load() == 1 }) {
				t.Fatal("the call never reached the key store")
			}
			select {
			case r := <-done:
				t.Fatalf("%s returned (%q, %v) while the key store was still waiting for a person: the interactive sealer was bounded by the %v timeout", c.name, r.token, r.err, keyStoreTimeout)
			case <-time.After(300 * time.Millisecond): // six times the timeout
			}
			kek.release() // the person answers
			select {
			case r := <-done:
				restore()
				if r.err != nil || r.token != c.want {
					t.Fatalf("%s = %q, %v after the answer, want %q and no error", c.name, r.token, r.err, c.want)
				}
			case <-time.After(30 * time.Second):
				t.Fatalf("%s did not return once the key store had answered", c.name)
			}
			if got, err := OpenStore(dir, keyringSealer{kek: slowKEK().get}).LoadRefresh(); err != nil || (c.name == "SaveRefresh" && got != "rt-2") {
				t.Fatalf("refresh.enc holds %q (%v) afterwards", got, err)
			}
		})
	}
}

// The quiet keyring sealer, and the same key function with it, stays bounded: a
// gate that runs before a command has no person to wait for.
func TestTheQuietKeyringSealersCallsStillTimeOut(t *testing.T) {
	setKeyStoreTimeout(t, 100*time.Millisecond)
	dir := filepath.Join(t.TempDir(), "account")
	if err := OpenStore(dir, keyringSealer{kek: newTestKEK(nil).get}).SaveRefresh("rt-1"); err != nil {
		t.Fatal(err)
	}
	kek := newTestKEK(make(chan struct{}))
	t.Cleanup(kek.release)
	loads, saves := ownLimit(OpenStore(dir, keyringSealer{kek: kek.get})), ownLimit(OpenStore(dir, keyringSealer{kek: kek.get}))
	var loadErr, saveErr error
	timed(t, "LoadRefresh", func() { _, loadErr = loads.LoadRefresh() })
	timed(t, "SaveRefresh", func() { saveErr = saves.SaveRefresh("rt-2") })
	for name, err := range map[string]error{"LoadRefresh": loadErr, "SaveRefresh": saveErr} {
		if !errors.Is(err, ErrKeyringUnavailable) || !strings.Contains(err.Error(), "did not answer within") {
			t.Errorf("%s = %v, want the key store timeout", name, err)
		}
	}
	kek.release()
	if !waitUntil(func() bool { return goroutinesIn("callKeyStore") == 0 }) {
		t.Fatal("the abandoned calls did not end after the key store answered")
	}
}

// Only the interactive keyring sealer is marked. Every other sealer is bounded:
// the quiet keyring sealer, the memory sealer, and a double that does not say.
func TestOnlyTheInteractiveKeyringSealerMayWaitForAPerson(t *testing.T) {
	cases := []struct {
		name string
		s    Sealer
		want bool
	}{
		{"the interactive keyring sealer", NewInteractiveKeyringSealer(), true},
		{"the quiet keyring sealer", NewKeyringSealer(), false},
		{"the memory sealer", NewMemorySealer(), false},
		{"a sealer of no known kind", &gatedSealer{inner: NewMemorySealer()}, false},
		{"a keyring sealer that is not marked", keyringSealer{kek: newTestKEK(nil).get}, false},
		{"a keyring sealer that is marked", keyringSealer{kek: newTestKEK(nil).get, mayPrompt: true}, true},
	}
	for _, c := range cases {
		if got := isPrompting(c.s); got != c.want {
			t.Errorf("isPrompting(%s) = %t, want %t", c.name, got, c.want)
		}
	}
}

// Through the interactive sealer the store otherwise behaves as it did: a missing
// refresh.enc never reaches the key store, a token goes in and comes out, and the
// sealer's own failure is reported as the key store being unavailable, with its
// cause.
func TestTheInteractiveSealerOtherwiseBehavesAsBefore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "account")
	kek := newTestKEK(nil)
	st := OpenStore(dir, keyringSealer{kek: kek.get, mayPrompt: true})
	if rt, err := st.LoadRefresh(); rt != "" || err != nil || kek.asked.Load() != 0 {
		t.Fatalf("LoadRefresh with no refresh.enc = %q, %v after %d key store requests, want none at all", rt, err, kek.asked.Load())
	}
	if err := st.SaveRefresh("rt-1"); err != nil {
		t.Fatal(err)
	}
	if rt, err := st.LoadRefresh(); rt != "rt-1" || err != nil {
		t.Fatalf("LoadRefresh = %q, %v, want rt-1", rt, err)
	}
	if err := st.SaveRefresh(""); err == nil {
		t.Fatal("an empty refresh token was sealed")
	}
	cause := errors.New("the keychain is locked")
	failing := keyringSealer{kek: func(bool) ([]byte, bool, error) { return nil, false, cause }, mayPrompt: true}
	broken := OpenStore(dir, failing)
	if _, err := broken.LoadRefresh(); !errors.Is(err, ErrKeyringUnavailable) || !errors.Is(err, cause) {
		t.Fatalf("LoadRefresh with a key store that fails = %v, want ErrKeyringUnavailable with its cause", err)
	}
	if err := broken.SaveRefresh("rt-2"); !errors.Is(err, ErrKeyringUnavailable) || !errors.Is(err, cause) {
		t.Fatalf("SaveRefresh with a key store that fails = %v, want ErrKeyringUnavailable with its cause", err)
	}
	if rt, err := OpenStore(dir, keyringSealer{kek: kek.get}).LoadRefresh(); rt != "rt-1" || err != nil {
		t.Fatalf("a failed SaveRefresh changed refresh.enc: %q, %v", rt, err)
	}
}
