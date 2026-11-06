package secrets

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/profiledir"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"

	"github.com/zalando/go-keyring"
)

func newDEKTestDB(t *testing.T) *storage.Database {
	t.Helper()
	db := testdb.Open(t)
	return db
}

func TestGetOrCreateDEK_PersistsAcrossCalls(t *testing.T) {
	keyring.MockInit()
	db := newDEKTestDB(t)
	ctx := context.Background()

	dek1, err := getOrCreateDEK(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("getOrCreateDEK (first call): %v", err)
	}
	if len(dek1) != 32 {
		t.Fatalf("expected 32-byte DEK, got %d bytes", len(dek1))
	}

	dek2, err := getOrCreateDEK(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("getOrCreateDEK (second call): %v", err)
	}
	if string(dek1) != string(dek2) {
		t.Fatal("second call must return the same DEK, not regenerate")
	}

	var count int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM vault_keys`).Scan(&count); err != nil {
		t.Fatalf("counting vault_keys rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 vault_keys row, got %d", count)
	}
}

// TestGetOrCreateDEK_DifferentDBsGetIndependentDEKs verifies the per-*sql.DB
// cache doesn't leak a DEK across distinct databases: two separate temp
// SQLite files, both sharing the same (mocked, process-wide) KEK, must each
// get their own independently-generated DEK.
func TestGetOrCreateDEK_DifferentDBsGetIndependentDEKs(t *testing.T) {
	keyring.MockInit()
	ctx := context.Background()

	dbA := newDEKTestDB(t)
	dbB := newDEKTestDB(t)

	dekA, err := getOrCreateDEK(ctx, dbA.DB, "default")
	if err != nil {
		t.Fatalf("getOrCreateDEK (db A): %v", err)
	}
	dekB, err := getOrCreateDEK(ctx, dbB.DB, "default")
	if err != nil {
		t.Fatalf("getOrCreateDEK (db B): %v", err)
	}
	if string(dekA) == string(dekB) {
		t.Fatal("expected distinct DEKs for distinct databases, got the same key")
	}

	// Confirm the cache is stable per-db across repeated calls too.
	dekA2, err := getOrCreateDEK(ctx, dbA.DB, "default")
	if err != nil {
		t.Fatalf("getOrCreateDEK (db A, second call): %v", err)
	}
	if string(dekA) != string(dekA2) {
		t.Fatal("db A's second call must return the same cached DEK")
	}
}

// TestGetOrCreateDEK_ConcurrentFirstUseIsRaceFree exercises the TOCTOU race
// this function used to have: without a per-db sync.Once serializing the
// expensive fetchOrCreateDEK path (SELECT vault_keys, then INSERT if
// missing), many goroutines racing on the very first use of a fresh db —
// e.g. concurrent workflow executions all resolving @secret: refs against
// the same db for the first time — could all see sql.ErrNoRows, all
// generate a different random DEK, and all attempt to INSERT the id=1
// singleton row; every loser's INSERT would fail, turning a routine
// concurrent first-use path into spurious secret-resolution errors. Run with
// -race to also confirm there's no data race on the shared entry.
func TestGetOrCreateDEK_ConcurrentFirstUseIsRaceFree(t *testing.T) {
	keyring.MockInit()
	db := newDEKTestDB(t)
	ctx := context.Background()

	const numGoroutines = 20
	deks := make([][]byte, numGoroutines)
	errs := make([]error, numGoroutines)

	var wg sync.WaitGroup
	var start sync.WaitGroup
	start.Add(1)
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			start.Wait() // line every goroutine up to maximize the race window
			deks[i], errs[i] = getOrCreateDEK(ctx, db.DB, "default")
		}(i)
	}
	start.Done()
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: getOrCreateDEK: %v", i, err)
		}
	}
	for i := 1; i < numGoroutines; i++ {
		if string(deks[i]) != string(deks[0]) {
			t.Fatalf("goroutine %d got a different DEK than goroutine 0 — first-use race produced divergent keys", i)
		}
	}

	var count int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM vault_keys`).Scan(&count); err != nil {
		t.Fatalf("counting vault_keys rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 vault_keys row after concurrent first use, got %d", count)
	}
}

// TestGetOrCreateDEK_FailedAttemptIsNotCached exercises the regression this
// change fixes: a transient failure on the very first getOrCreateDEK call
// for a db (e.g. a locked keychain or a busy vault_keys table) must not be
// memoized forever. The next call after the failure should retry from
// scratch and succeed, rather than replaying the stale cached error.
func TestGetOrCreateDEK_FailedAttemptIsNotCached(t *testing.T) {
	keyring.MockInit()
	db := newDEKTestDB(t)
	ctx := context.Background()

	injectedErr := errors.New("injected transient failure")

	orig := fetchDEK
	t.Cleanup(func() { fetchDEK = orig })

	fetchDEK = func(ctx context.Context, db *sql.DB, profileID string) ([]byte, error) {
		return nil, injectedErr
	}

	_, err := getOrCreateDEK(ctx, db.DB, "default")
	if !errors.Is(err, injectedErr) {
		t.Fatalf("first call: expected injected error, got %v", err)
	}

	// Restore the real fetch behavior; the NEXT call must retry from
	// scratch instead of replaying the cached failure.
	fetchDEK = orig

	dek, err := getOrCreateDEK(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("second call after failure: expected success, got error: %v", err)
	}
	if len(dek) != 32 {
		t.Fatalf("expected 32-byte DEK, got %d bytes", len(dek))
	}
}

// TestGetOrCreateDEK_ConcurrentFailureIsSharedThenRetried proves the
// race-fix property from the previous commit survives this change: every
// goroutine racing on the single in-flight (failing) first attempt must
// observe the identical error together — the fix only changes what happens
// on the NEXT call, after that attempt has completed.
func TestGetOrCreateDEK_ConcurrentFailureIsSharedThenRetried(t *testing.T) {
	keyring.MockInit()
	db := newDEKTestDB(t)
	ctx := context.Background()

	injectedErr := errors.New("injected transient failure")

	orig := fetchDEK
	t.Cleanup(func() { fetchDEK = orig })

	const numGoroutines = 20

	// arrived is used to hold the winning goroutine's fetchDEK call open
	// until every other goroutine has entered getOrCreateDEK and picked up
	// the same (not-yet-deleted) entry. Without this, the fast injected
	// failure could resolve — and its entry get evicted from the map —
	// before slower goroutines even do their initial map lookup, causing
	// them to create fresh entries and refetch instead of sharing the one
	// in-flight attempt, which would defeat the point of this test.
	var arrived sync.WaitGroup
	arrived.Add(numGoroutines)

	var calls int32
	fetchDEK = func(ctx context.Context, db *sql.DB, profileID string) ([]byte, error) {
		atomic.AddInt32(&calls, 1)
		arrived.Wait()
		// Give the other goroutines, which have all signaled arrived but
		// may not yet have completed their own (uncontended, near-instant)
		// map lookup, a moment to actually pick up this entry before it
		// can be evicted below on error.
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
			_, errs[i] = getOrCreateDEK(ctx, db.DB, "default")
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

	// Now that the failing attempt is done, the next call must retry
	// from scratch and succeed once the fetch behavior is restored.
	fetchDEK = orig

	dek, err := getOrCreateDEK(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("call after concurrent failure: expected success, got error: %v", err)
	}
	if len(dek) != 32 {
		t.Fatalf("expected 32-byte DEK, got %d bytes", len(dek))
	}
}

// TestFetchOrCreateDEK_ConcurrentColdBootstrapIsRaceFree exercises the
// cross-process race fetchOrCreateDEK/bootstrapDEKLocked fixes. Unlike
// TestGetOrCreateDEK_ConcurrentFirstUseIsRaceFree above (which calls the
// memoized getOrCreateDEK and so only ever proves in-process safety via its
// own per-db sync.Once), this calls fetchOrCreateDEK directly — bypassing
// all in-process memoization — so each goroutine behaves like an
// independent process doing a genuine cold-start bootstrap, all sharing one
// on-disk SQLite file the way separate OS processes sharing
// ~/.monoagent/monoagent.db would. Before the fix, this interleaving could
// leave the keychain holding a different KEK than the one the persisted
// vault_keys row was wrapped under, with no error at the moment of the
// race — corruption that only surfaced later as an unrecoverable decrypt
// failure. Run with -race to also confirm the shared conn pool isn't
// misused.
func TestFetchOrCreateDEK_ConcurrentColdBootstrapIsRaceFree(t *testing.T) {
	resetKEKState(t)
	keyring.MockInit()
	db := newDEKTestDB(t)
	ctx := context.Background()

	const numGoroutines = 20
	deks := make([][]byte, numGoroutines)
	errs := make([]error, numGoroutines)

	var wg sync.WaitGroup
	var start sync.WaitGroup
	start.Add(1)
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			start.Wait()
			deks[i], errs[i] = fetchOrCreateDEK(ctx, db.DB, "default")
		}(i)
	}
	start.Done()
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: fetchOrCreateDEK: %v", i, err)
		}
	}
	for i := 1; i < numGoroutines; i++ {
		if string(deks[i]) != string(deks[0]) {
			t.Fatalf("goroutine %d got a different DEK than goroutine 0 — cross-process bootstrap race produced divergent keys", i)
		}
	}

	var count int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM vault_keys`).Scan(&count); err != nil {
		t.Fatalf("counting vault_keys rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 vault_keys row after concurrent cold bootstrap, got %d", count)
	}

	// The KEK now in the keychain must actually be able to decrypt the
	// persisted row — the exact property the old code couldn't guarantee.
	kek, found, err := peekKEK("default")
	if err != nil || !found {
		t.Fatalf("peekKEK after bootstrap: found=%v err=%v", found, err)
	}
	var wrappedDEK, wrappedNonce []byte
	if err := db.DB.QueryRow(`SELECT wrapped_dek, wrapped_nonce FROM vault_keys WHERE profile_id = 'default'`).Scan(&wrappedDEK, &wrappedNonce); err != nil {
		t.Fatalf("reading persisted vault_keys row: %v", err)
	}
	dek, err := unwrapDEK(context.Background(), db.DB, kek, wrappedDEK, wrappedNonce, "default", false)
	if err != nil {
		t.Fatalf("keychain KEK cannot decrypt the persisted DEK row: %v", err)
	}
	if string(dek) != string(deks[0]) {
		t.Fatal("DEK decrypted via the keychain's current KEK doesn't match what fetchOrCreateDEK returned")
	}
}

// TestGetOrCreateDEK_EmptyProfileIDIsRefused pins the DEK boundary: an empty
// profile ID is an error, never a silent "default" -- no vault_keys row and
// no bare "kek-" keychain entry may appear.
func TestGetOrCreateDEK_EmptyProfileIDIsRefused(t *testing.T) {
	keyring.MockInit()
	db := newDEKTestDB(t)

	if _, err := getOrCreateDEK(context.Background(), db.DB, ""); !errors.Is(err, profiledir.ErrNoProfile) {
		t.Fatalf("getOrCreateDEK with empty profile err = %v, want profiledir.ErrNoProfile", err)
	}

	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM vault_keys`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("vault_keys rows after a refused call: n=%d err=%v", n, err)
	}
	if _, err := keyring.Get(keyringService, "kek-"); err != keyring.ErrNotFound {
		t.Fatalf("expected no bare kek- keychain entry, got err=%v", err)
	}
	if _, err := CheckVault(context.Background(), db.DB, ""); !errors.Is(err, profiledir.ErrNoProfile) {
		t.Fatalf("CheckVault with empty profile err = %v, want profiledir.ErrNoProfile", err)
	}
}
