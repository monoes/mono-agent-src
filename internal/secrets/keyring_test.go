package secrets

import (
	"errors"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

// resetKEKState clears the process-wide kekAttempts memoization so each test
// starts as if in a fresh process, regardless of what earlier tests in this
// package did to the shared KEK state.
func resetKEKState(t *testing.T) {
	t.Helper()
	kekAttemptsMu.Lock()
	kekAttempts = map[string]*kekAttempt{}
	kekAttemptsMu.Unlock()
	forgetFilePassphrases()
	t.Cleanup(forgetFilePassphrases)
}

func TestGetOrCreateKEK_PersistsAcrossCalls(t *testing.T) {
	resetKEKState(t)
	keyring.MockInit() // in-memory mock backend, no real OS keychain touched in tests

	key1, err := getOrCreateKEK("default")
	if err != nil {
		t.Fatalf("getOrCreateKEK (first call): %v", err)
	}
	if len(key1) != 32 {
		t.Fatalf("expected 32-byte key, got %d bytes", len(key1))
	}

	key2, err := getOrCreateKEK("default")
	if err != nil {
		t.Fatalf("getOrCreateKEK (second call): %v", err)
	}
	if string(key1) != string(key2) {
		t.Fatal("second call must return the same KEK, not regenerate")
	}
}

// TestGetOrCreateKEK_DifferentProfilesGetIndependentKEKs verifies each
// profile gets its own keychain-backed KEK — the core property of the
// per-profile redesign.
func TestGetOrCreateKEK_DifferentProfilesGetIndependentKEKs(t *testing.T) {
	resetKEKState(t)
	keyring.MockInit()

	keyA, err := getOrCreateKEK("profile-a")
	if err != nil {
		t.Fatalf("getOrCreateKEK (profile-a): %v", err)
	}
	keyB, err := getOrCreateKEK("profile-b")
	if err != nil {
		t.Fatalf("getOrCreateKEK (profile-b): %v", err)
	}
	if string(keyA) == string(keyB) {
		t.Fatal("expected distinct KEKs for distinct profiles, got the same key")
	}
}

// TestGetOrCreateKEK_FailedAttemptIsNotCached exercises the regression this
// change fixes: a transient failure on the very first getOrCreateKEK call in
// the process (e.g. a momentarily locked OS keychain) must not be memoized
// forever. The next call after the failure should retry from scratch and
// succeed, rather than replaying the stale cached error for the rest of the
// process's lifetime.
func TestGetOrCreateKEK_FailedAttemptIsNotCached(t *testing.T) {
	resetKEKState(t)
	keyring.MockInit()

	injectedErr := errors.New("injected transient failure")

	orig := fetchKEK
	t.Cleanup(func() { fetchKEK = orig })

	fetchKEK = func(profileID string) ([]byte, error) {
		return nil, injectedErr
	}

	_, err := getOrCreateKEK("default")
	if !errors.Is(err, injectedErr) {
		t.Fatalf("first call: expected injected error, got %v", err)
	}

	// Restore the real fetch behavior; the NEXT call must retry from
	// scratch instead of replaying the cached failure.
	fetchKEK = orig

	key, err := getOrCreateKEK("default")
	if err != nil {
		t.Fatalf("second call after failure: expected success, got error: %v", err)
	}
	if len(key) != 32 {
		t.Fatalf("expected 32-byte KEK, got %d bytes", len(key))
	}
}

// TestGetOrCreateKEK_ConcurrentFailureIsSharedThenRetried proves multiple
// goroutines racing on the single in-flight (failing) first attempt observe
// the identical error together, and that only the NEXT call after that
// attempt completes gets a fresh retry.
func TestGetOrCreateKEK_ConcurrentFailureIsSharedThenRetried(t *testing.T) {
	resetKEKState(t)
	keyring.MockInit()

	injectedErr := errors.New("injected transient failure")

	orig := fetchKEK
	t.Cleanup(func() { fetchKEK = orig })

	const numGoroutines = 20

	// arrived holds the winning goroutine's fetchKEK call open until every
	// other goroutine has entered getOrCreateKEK and picked up the same
	// (not-yet-replaced) kekOnce, so they all share the one in-flight
	// attempt instead of racing ahead onto a freshly-swapped Once.
	var arrived sync.WaitGroup
	arrived.Add(numGoroutines)

	var calls int32
	fetchKEK = func(profileID string) ([]byte, error) {
		atomic.AddInt32(&calls, 1)
		arrived.Wait()
		time.Sleep(50 * time.Millisecond)
		return nil, injectedErr
	}

	errs := make([]error, numGoroutines)

	var wg sync.WaitGroup
	var start sync.WaitGroup
	start.Add(1)
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			start.Wait()
			arrived.Done()
			_, errs[i] = getOrCreateKEK("default")
		}(i)
	}
	start.Done()
	wg.Wait()

	for i, err := range errs {
		if !errors.Is(err, injectedErr) {
			t.Fatalf("goroutine %d: expected injected error, got %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected exactly 1 fetch attempt for the concurrent failing first use, got %d", got)
	}

	// Now that the failing attempt is done, the next call must retry from
	// scratch and succeed once the fetch behavior is restored.
	fetchKEK = orig

	key, err := getOrCreateKEK("default")
	if err != nil {
		t.Fatalf("call after concurrent failure: expected success, got error: %v", err)
	}
	if len(key) != 32 {
		t.Fatalf("expected 32-byte KEK, got %d bytes", len(key))
	}
}

// TestGetOrCreateKEK_StragglerDoesNotRaceWithFreshAttempt exercises the data
// race this change fixes: on the old implementation, a failed attempt swapped
// only the *sync.Once pointer while kekCache/kekErr remained shared,
// unguarded package-level variables. A straggler goroutine from a just-failed
// attempt reading "return kekCache, kekErr" could race with a brand-new
// attempt (using the freshly-swapped Once) concurrently writing
// "kekCache, kekErr = fetchKEK()". By hammering getOrCreateKEK with many
// goroutines through many failure-then-retry cycles, this reliably surfaces
// under `go test -race` on the old shared-variable implementation, and passes
// cleanly now that each attempt owns its own kekAttempt struct.
func TestGetOrCreateKEK_StragglerDoesNotRaceWithFreshAttempt(t *testing.T) {
	resetKEKState(t)
	keyring.MockInit()

	injectedErr := errors.New("injected transient failure")

	orig := fetchKEK
	t.Cleanup(func() { fetchKEK = orig })

	const failuresBeforeSuccess = 200
	var failCount int32
	fetchKEK = func(profileID string) ([]byte, error) {
		if atomic.AddInt32(&failCount, 1) <= failuresBeforeSuccess {
			return nil, injectedErr
		}
		return orig(profileID)
	}

	const goroutines = 50
	const callsPerGoroutine = 20

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < callsPerGoroutine; j++ {
				// Ignore the returned values here: the point of this test is
				// to surface the unguarded concurrent read/write of the
				// memoized result under -race, not to assert on any one
				// call's outcome (which legitimately varies call-to-call as
				// failures give way to the eventual successful retry).
				_, _ = getOrCreateKEK("default")
			}
		}()
	}
	wg.Wait()
}

// The file-based fallback KEK is removed with the profile, and the answer says
// whether anything was there to remove.
func TestDeleteProfileKEKRemovesTheFileFallbackToo(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	if removed, err := DeleteProfileKEK("gone"); err != nil || removed {
		t.Fatalf("no key anywhere: removed %v, err %v, want false and nil", removed, err)
	}
	path := fileKeyringPath("gone")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	other := fileKeyringPath("kept")
	if err := os.WriteFile(other, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	removed, err := DeleteProfileKEK("gone")
	if err != nil || !removed {
		t.Fatalf("file key: removed %v, err %v, want true and nil", removed, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the file key is still there: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("another profile's file key was touched: %v", err)
	}
}
