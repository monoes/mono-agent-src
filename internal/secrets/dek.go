package secrets

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"github.com/monoes/mono-agent/internal/profiledir"
	"sync"
	"time"
)

// dekEntry holds the memoized result of one (db, profile) pair's
// fetchOrCreateDEK call, guarded by its own sync.Once so that call runs at
// most once no matter how many goroutines race on it. Same pattern
// keyring.go's getOrCreateKEK uses, scoped per-(db, profile) since each
// profile now has its own DEK.
type dekEntry struct {
	once sync.Once
	dek  []byte
	err  error
}

// dekKey identifies one memoized DEK attempt: a given db may hold several
// profiles' secrets, each under its own DEK.
type dekKey struct {
	db        *sql.DB
	profileID string
}

// dekEntries memoizes getOrCreateDEK per (db, profileID). Only the map
// lookup/insert itself is guarded by dekEntriesMu — a short critical
// section; the expensive fetchOrCreateDEK call runs inside that entry's own
// once.Do, outside the mutex. Without this, two goroutines racing on the
// very first use of a given (db, profile) pair (e.g. two workflow
// executions resolving @secret: refs concurrently) could both see
// sql.ErrNoRows on the SELECT and both attempt to INSERT that profile's
// vault_keys row; the loser's INSERT would fail and that call would return a
// spurious error instead of the real DEK.
var (
	dekEntriesMu sync.Mutex
	dekEntries   = map[dekKey]*dekEntry{}
)

// fetchDEK is the function getOrCreateDEK invokes to actually resolve the
// DEK on first use. It is a package-level variable (rather than a direct
// call to fetchOrCreateDEK) purely so tests can substitute a stub that
// fails on demand to exercise the retry-after-failure path below.
var fetchDEK = fetchOrCreateDEK

// getOrCreateDEK returns profileID's unwrapped 32-byte Data Encryption Key,
// reading and unwrapping its vault_keys row if present, or generating a new
// DEK (wrapped under that profile's own KEK from the OS keychain) and
// persisting it if this is the first use. A successful result is cached per
// (db, profile) so repeated calls within a process skip the keychain/table
// round trip. A failed attempt is NOT cached: all callers racing on that one
// attempt observe the same error (via the shared sync.Once), but the next
// call after it completes gets a fresh attempt instead of being stuck
// forever with a stale transient error (e.g. a momentarily locked keychain
// or a SQLITE_BUSY on vault_keys).
func getOrCreateDEK(ctx context.Context, db *sql.DB, profileID string) ([]byte, error) {
	// An empty profile ID is refused: "" would create its own "kek-" keychain
	// entry and an empty-profile vault_keys row that nothing else finds.
	if profileID == "" {
		return nil, profiledir.ErrNoProfile
	}
	key := dekKey{db: db, profileID: profileID}
	dekEntriesMu.Lock()
	entry, ok := dekEntries[key]
	if !ok {
		entry = &dekEntry{}
		dekEntries[key] = entry
	}
	dekEntriesMu.Unlock()

	entry.once.Do(func() {
		entry.dek, entry.err = fetchDEK(ctx, db, profileID)
	})

	if entry.err != nil {
		dekEntriesMu.Lock()
		if dekEntries[key] == entry {
			delete(dekEntries, key)
		}
		dekEntriesMu.Unlock()
	}

	return entry.dek, entry.err
}

// peekDEK returns profileID's unwrapped DEK without creating anything and without the cache: it reads the
// vault_keys row, then the wrapping key, and unwraps (no rewrite). No row is ErrNoProfileKey; a row with no
// usable wrapping key is an error.
func peekDEK(ctx context.Context, db *sql.DB, profileID string) ([]byte, error) {
	if profileID == "" {
		return nil, profiledir.ErrNoProfile
	}
	var wrappedDEK, wrappedNonce []byte
	err := db.QueryRowContext(ctx, `SELECT wrapped_dek, wrapped_nonce FROM vault_keys WHERE profile_id = ?`, profileID).
		Scan(&wrappedDEK, &wrappedNonce)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoProfileKey
	}
	if err != nil {
		return nil, fmt.Errorf("secrets: reading vault_keys: %w", err)
	}
	kek, found, err := peekKEK(profileID)
	if err != nil {
		return nil, fmt.Errorf("secrets: reading the wrapping key: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("secrets: the data key of the profile cannot be unwrapped: its wrapping key is missing")
	}
	dek, err := unwrapDEK(ctx, db, kek, wrappedDEK, wrappedNonce, profileID, false)
	if err != nil {
		return nil, fmt.Errorf("secrets: unwrapping DEK: %w", err)
	}
	return dek, nil
}

// ForgetProfile drops every cached DEK of the profile (any database handle), so the next getOrCreateDEK reads the
// vault_keys row again instead of returning a key the profile no longer has. A profile delete calls it.
func ForgetProfile(profileID string) {
	dekEntriesMu.Lock()
	for k := range dekEntries {
		if k.profileID == profileID {
			delete(dekEntries, k)
		}
	}
	dekEntriesMu.Unlock()
}

// fetchOrCreateDEK's fast path (KEK and wrapped DEK both already exist) is
// pure reads and needs no lock — the common case after first bootstrap. The
// slow path (either is still missing) hands off to bootstrapDEKLocked,
// which serializes the *entire* bootstrap — keychain and vault_keys
// together — across every process sharing db, for this profile.
//
// Before this, getOrCreateKEK and the SELECT-then-INSERT on vault_keys each
// raced independently with no cross-process lock at all: two processes
// bootstrapping the same profile's vault for the first time concurrently
// could each generate a different KEK, each call keyring.Set with no
// compare-and-swap (last write silently wins), and each attempt to INSERT
// that profile's vault_keys row wrapped under whichever KEK *they*
// generated (only one INSERT actually wins). Nothing errors at the moment
// of that race — the process whose own INSERT won stays internally
// self-consistent (via its own in-process memoization) for the rest of its
// lifetime — but the keychain may now hold the *other* process's KEK,
// wrapping nothing. The mismatch only surfaces later, on a different
// process (or that same process's own next launch), as an unrecoverable
// "cipher: message authentication failed" — silently destroying every
// stored secret except from an Export backup.
func fetchOrCreateDEK(ctx context.Context, db *sql.DB, profileID string) ([]byte, error) {
	kek, kekFound, err := peekKEK(profileID)
	if err != nil {
		return nil, fmt.Errorf("secrets: getOrCreateDEK: %w", err)
	}
	if kekFound {
		var wrappedDEK, wrappedNonce []byte
		err := db.QueryRowContext(ctx, `SELECT wrapped_dek, wrapped_nonce FROM vault_keys WHERE profile_id = ?`, profileID).
			Scan(&wrappedDEK, &wrappedNonce)
		switch {
		case err == nil:
			dek, err := unwrapDEK(ctx, db, kek, wrappedDEK, wrappedNonce, profileID, true)
			if err != nil {
				return nil, fmt.Errorf("secrets: unwrapping DEK: %w", err)
			}
			return dek, nil
		case err != sql.ErrNoRows:
			return nil, fmt.Errorf("secrets: reading vault_keys: %w", err)
		}
	}

	return bootstrapDEKLocked(ctx, db, profileID)
}

// bootstrapDEKLocked serializes first-time KEK/DEK creation across every
// process sharing db, for one profile, reusing the same BEGIN IMMEDIATE
// pattern vault.Register (internal/vault/vault.go) and secrets.addEntry use
// for cross-process singleton allocation — see either for why BEGIN
// IMMEDIATE specifically is required (it acquires SQLite's write lock up
// front, unlike the default DEFERRED transaction). Non-DB work (the
// keychain round trip) running inside the transaction has the same
// precedent: vault.Register does its file copy inside its own BEGIN
// IMMEDIATE for the identical reason.
//
// Re-checks both the keychain and vault_keys after acquiring the lock,
// since another process may have completed this profile's bootstrap in the
// window between fetchOrCreateDEK's fast-path peek and this function
// acquiring the lock.
func bootstrapDEKLocked(ctx context.Context, db *sql.DB, profileID string) ([]byte, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("secrets: bootstrapDEKLocked: get conn: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return nil, fmt.Errorf("secrets: bootstrapDEKLocked: begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	// fetchOrCreateKEK (not the memoized getOrCreateKEK): we're already
	// holding a cross-process lock stronger than its in-process sync.Once.
	kek, err := fetchOrCreateKEK(profileID)
	if err != nil {
		return nil, fmt.Errorf("secrets: bootstrapDEKLocked: %w", err)
	}

	var wrappedDEK, wrappedNonce []byte
	err = conn.QueryRowContext(ctx, `SELECT wrapped_dek, wrapped_nonce FROM vault_keys WHERE profile_id = ?`, profileID).
		Scan(&wrappedDEK, &wrappedNonce)

	var dek []byte
	switch {
	case err == sql.ErrNoRows:
		dek, err = createDEK(ctx, db, conn, kek, profileID)
		if err != nil {
			return nil, err
		}
	case err != nil:
		return nil, fmt.Errorf("secrets: reading vault_keys: %w", err)
	default:
		// Another process already finished bootstrapping between our
		// fast-path peek and acquiring this lock.
		dek, err = unwrapDEK(ctx, db, kek, wrappedDEK, wrappedNonce, profileID, false)
		if err != nil {
			return nil, fmt.Errorf("secrets: unwrapping DEK: %w", err)
		}
	}

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, fmt.Errorf("secrets: bootstrapDEKLocked: commit: %w", err)
	}
	committed = true
	return dek, nil
}

// dbExecer is the subset of *sql.DB / *sql.Conn createDEK needs — it's
// always called with the *sql.Conn already holding bootstrapDEKLocked's
// BEGIN IMMEDIATE transaction.
type dbExecer interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
}

func createDEK(ctx context.Context, db *sql.DB, execer dbExecer, kek []byte, profileID string) ([]byte, error) {
	dek := make([]byte, 32)
	if _, err := rand.Read(dek); err != nil {
		return nil, fmt.Errorf("secrets: generating DEK: %w", err)
	}
	wrappedDEK, wrappedNonce, err := wrapDEK(ctx, db, kek, dek)
	if err != nil {
		return nil, fmt.Errorf("secrets: wrapping DEK: %w", err)
	}
	_, err = execer.ExecContext(ctx,
		`INSERT INTO vault_keys (profile_id, wrapped_dek, wrapped_nonce, created_at) VALUES (?, ?, ?, ?)`,
		profileID, wrappedDEK, wrappedNonce, time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return nil, fmt.Errorf("secrets: storing wrapped DEK: %w", err)
	}
	if path, perr := installPath(ctx, db); perr == nil {
		recordBoundHint(ctx, execer, profileID, path)
	}
	return dek, nil
}

// fetchLegacyDEK returns the single pre-per-profile DEK every profile used
// to share, unwrapped under the legacy KEK and read from vault_keys_legacy
// (id=1, preserved as-is by migration 027's reconcile). Read-only, used
// only by the vault-key migration to decrypt existing secrets before
// re-encrypting them under a fresh per-profile key. Returns found=false if
// this database was never migrated from the pre-per-profile scheme (a fresh
// install, or one that already completed migration and has no legacy row).
//
// A successful result is memoized per database for the process lifetime:
// the legacy DEK is immutable, and the per-profile migration loops (CLI
// initDB, MCP bootstrap, wails startup) plus DecryptBlobLegacy would
// otherwise re-read the OS keychain once per profile/row — on macOS each
// keyring read forks a /usr/bin/security subprocess. Failures are not
// cached, mirroring the retry-after-failure behavior above.
var (
	legacyDEKMu    sync.Mutex
	legacyDEKCache = map[*sql.DB][]byte{}
)

func fetchLegacyDEK(ctx context.Context, db *sql.DB) (dek []byte, found bool, err error) {
	legacyDEKMu.Lock()
	cached, ok := legacyDEKCache[db]
	legacyDEKMu.Unlock()
	if ok {
		return cached, true, nil
	}

	kek, kekFound, err := fetchLegacyKEK()
	if err != nil {
		return nil, false, fmt.Errorf("secrets: fetchLegacyDEK: %w", err)
	}
	if !kekFound {
		return nil, false, nil
	}
	var wrappedDEK, wrappedNonce []byte
	err = db.QueryRowContext(ctx, `SELECT wrapped_dek, wrapped_nonce FROM vault_keys_legacy WHERE id = 1`).
		Scan(&wrappedDEK, &wrappedNonce)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("secrets: fetchLegacyDEK: reading vault_keys_legacy: %w", err)
	}
	dek, err = Decrypt(kek, wrappedDEK, wrappedNonce)
	if err != nil {
		return nil, false, fmt.Errorf("secrets: fetchLegacyDEK: unwrapping: %w", err)
	}

	legacyDEKMu.Lock()
	legacyDEKCache[db] = dek
	legacyDEKMu.Unlock()
	return dek, true, nil
}
