package procbind

import (
	"sync"
	"time"
)

// readyTTL is how long Refusal reuses the machine-capability answer: the probe behind Ready can create a temp directory, listen,
// dial and accept (Windows), and Refusal is asked up to three times per turn and by status, doctor and probe. Only the machine's
// capability is cached (it does not depend on the setting or on the service); a cause is cached no longer than a success.
const readyTTL = 5 * time.Second

var (
	readyNow   = time.Now
	readyMu    sync.Mutex
	readyAt    time.Time
	readyErr   error
	readyValid bool
)

func cachedReady() error {
	readyMu.Lock()
	defer readyMu.Unlock()
	if now := readyNow(); readyValid && now.Sub(readyAt) >= 0 && now.Sub(readyAt) < readyTTL {
		return readyErr
	}
	readyErr = ready()
	readyAt, readyValid = readyNow(), true
	return readyErr
}

func resetReadyCache() {
	readyMu.Lock()
	readyValid = false
	readyMu.Unlock()
}

// The seams Refusal reads; tests replace them.
var (
	issuanceReady = func() bool { return IssuanceReady }
	ready         = Ready
)

// Refusal is why peer-credential binding may not run, as a short cause, or "" when it may. It is asked when the
// operator switches the setting on and again at every turn start (a hand-edited setting must still refuse), with
// whether the binding service is running. A cause is a fixed text for the operator: it names no path, key or
// token. The setting never degrades to a weaker mode: if it is on and this is not "", the turn is refused.
func Refusal(serviceRunning bool) string {
	err := cachedReady()
	switch {
	case !issuanceReady():
		return "this build does not issue bound tokens yet"
	case err != nil:
		return "this machine cannot run it: " + err.Error()
	case !serviceRunning:
		return "its service is not running"
	}
	return ""
}

// IssuanceOn says whether bound tokens may be issued and accepted. It is the constant IssuanceReady; the stores install
// the real verifier by it and the gate reads it, so one value decides everything.
func IssuanceOn() bool { return issuanceReady() }

// ForTestSetReady overrides what Ready says about the machine for the life of a test (nil: it can run the service). Like
// ForTestSetIssuance it takes the test, and a test of procbind fails if any non-test file calls it.
func ForTestSetReady(t interface {
	Helper()
	Cleanup(func())
}, err error) {
	t.Helper()
	old := ready
	ready = func() error { return err }
	resetReadyCache()
	t.Cleanup(func() { ready = old; resetReadyCache() })
}

// ForTestSetIssuance overrides IssuanceOn for the life of a test. It takes the test (anything with Cleanup), so it
// cannot be used without one; a test (procbind's own) fails if any non-test file calls it.
func ForTestSetIssuance(t interface {
	Helper()
	Cleanup(func())
}, on bool) {
	t.Helper()
	old := issuanceReady
	issuanceReady = func() bool { return on }
	t.Cleanup(func() { issuanceReady = old })
}

// ForTestSetReadyClock replaces the clock the capability cache reads, for the life of a test.
func ForTestSetReadyClock(t interface {
	Helper()
	Cleanup(func())
}, now func() time.Time) {
	t.Helper()
	old := readyNow
	readyNow = now
	resetReadyCache()
	t.Cleanup(func() { readyNow = old; resetReadyCache() })
}

// ForTestSetReadyFunc replaces the uncached probe Refusal's cache calls (a counter in a test); it also clears the cache.
func ForTestSetReadyFunc(t interface {
	Helper()
	Cleanup(func())
}, f func() error) {
	t.Helper()
	old := ready
	ready = f
	resetReadyCache()
	t.Cleanup(func() { ready = old; resetReadyCache() })
}
