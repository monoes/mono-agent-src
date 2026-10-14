package connections

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestMigrateConnectionsToVault_NoOpWhenAlreadyMigrated verifies the cheap
// COUNT check short-circuits the whole migration when every row already
// carries the vaultenc:v1: prefix and has vault_ref set (e.g. because it
// was written through Store.Save, which always encrypts and sets vault_ref).
func TestMigrateConnectionsToVault_NoOpWhenAlreadyMigrated(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	store := NewStore(db)

	if err := store.Save(ctx, &Connection{ID: "conn-1", Platform: "x", Method: "oauth", Label: "Test",
		Data: map[string]interface{}{"access_token": "already-encrypted"}}); err != nil {
		t.Fatalf("seeding encrypted connection: %v", err)
	}

	// Verify that the saved connection has vault_ref set.
	conn, err := store.Get(ctx, "conn-1")
	if err != nil {
		t.Fatalf("Get conn-1: %v", err)
	}
	if conn.VaultRef == "" {
		t.Fatal("expected vault_ref to be set after Save")
	}

	migrated, total, err := MigrateConnectionsToVault(ctx, db)
	if err != nil {
		t.Fatalf("MigrateConnectionsToVault: %v", err)
	}
	if migrated != 0 || total != 0 {
		t.Fatalf("expected a no-op (0, 0), got migrated=%d total=%d", migrated, total)
	}
}

// TestMigrateConnectionsToVault_BrowserConnectionReachesSteadyState verifies
// that a MethodBrowser connection — which structurally never gets a
// vault_ref (see TestStoreSave_BrowserPlatformNeverGetsAVaultRef) — doesn't
// perpetually match needsMigrationQuery's vault_ref-emptiness check. Before
// the fix, every run re-processed the row and returned migrated=1 forever
// instead of settling to a true no-op.
func TestMigrateConnectionsToVault_BrowserConnectionReachesSteadyState(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	store := NewStore(db)

	conn := &Connection{Platform: "instagram", Method: MethodBrowser, Label: "me", Data: map[string]interface{}{}}
	if err := store.Save(ctx, conn); err != nil {
		t.Fatalf("seeding browser connection: %v", err)
	}
	if conn.VaultRef != "" {
		t.Fatalf("expected no vault entry for a browser-session platform, got %q", conn.VaultRef)
	}

	if _, _, err := MigrateConnectionsToVault(ctx, db); err != nil {
		t.Fatalf("first MigrateConnectionsToVault: %v", err)
	}

	migrated, total, err := MigrateConnectionsToVault(ctx, db)
	if err != nil {
		t.Fatalf("second MigrateConnectionsToVault: %v", err)
	}
	if migrated != 0 || total != 0 {
		t.Fatalf("expected the second run to reach a no-op steady state, got migrated=%d total=%d", migrated, total)
	}
}

// TestMigrateConnectionsToVault_MigratesLegacyPlaintextAndBackfillsVaultRef verifies rows
// inserted with raw plaintext JSON (as connections created before the vault
// feature shipped would have) get re-encrypted and have vault_ref backfilled.
func TestMigrateConnectionsToVault_MigratesLegacyPlaintextAndBackfillsVaultRef(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	_, err := db.Exec(`
		INSERT INTO connections (id, platform, method, label, account_id, data, status, last_tested, profile_id, created_at, updated_at)
		VALUES ('conn-1', 'x', 'oauth', 'Test', '', '{"access_token":"plaintext-token"}', 'active', '', 'default', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("seeding plaintext connection: %v", err)
	}

	migrated, total, err := MigrateConnectionsToVault(ctx, db)
	if err != nil {
		t.Fatalf("MigrateConnectionsToVault: %v", err)
	}
	if migrated != 1 || total != 1 {
		t.Fatalf("expected migrated=1 total=1, got migrated=%d total=%d", migrated, total)
	}

	var rawData, vaultRef string
	if err := db.QueryRow(`SELECT data, vault_ref FROM connections WHERE id = 'conn-1'`).Scan(&rawData, &vaultRef); err != nil {
		t.Fatalf("reading migrated row: %v", err)
	}
	if strings.Contains(rawData, "plaintext-token") {
		t.Fatal("connections.data must not contain plaintext after migration")
	}
	if !strings.HasPrefix(rawData, "vaultenc:v1:") {
		t.Fatalf("expected vaultenc-prefixed ciphertext, got: %s", rawData)
	}
	if vaultRef == "" {
		t.Fatal("expected vault_ref to be backfilled after migration")
	}
}

// TestMigrateConnectionsToVault_ContinuesPastPerRowFailure verifies that a
// Save failure on one row doesn't abort the rest of the batch. A trigger
// simulates a row whose UPDATE (the path Save's INSERT...ON CONFLICT takes
// for a pre-existing id) always fails.
func TestMigrateConnectionsToVault_ContinuesPastPerRowFailure(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	_, err := db.Exec(`
		INSERT INTO connections (id, platform, method, label, account_id, data, status, last_tested, profile_id, created_at, updated_at)
		VALUES
		('bad-row', 'x', 'oauth', 'Bad', '', '{"access_token":"bad-token"}', 'active', '', 'default', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
		('good-row', 'x', 'oauth', 'Good', '', '{"access_token":"good-token"}', 'active', '', 'default', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("seeding plaintext connections: %v", err)
	}
	_, err = db.Exec(`
		CREATE TRIGGER fail_bad_row BEFORE UPDATE ON connections
		WHEN NEW.id = 'bad-row'
		BEGIN
			SELECT RAISE(FAIL, 'simulated failure');
		END;`)
	if err != nil {
		t.Fatalf("creating failure trigger: %v", err)
	}

	migrated, total, err := MigrateConnectionsToVault(ctx, db)
	if err != nil {
		t.Fatalf("MigrateConnectionsToVault should not abort on a per-row failure: %v", err)
	}
	if total != 2 {
		t.Fatalf("expected total=2, got %d", total)
	}
	if migrated != 1 {
		t.Fatalf("expected migrated=1 (good-row only), got %d", migrated)
	}

	var badData, goodData string
	if err := db.QueryRow(`SELECT data FROM connections WHERE id = 'bad-row'`).Scan(&badData); err != nil {
		t.Fatalf("reading bad-row: %v", err)
	}
	if !strings.Contains(badData, "bad-token") {
		t.Fatal("bad-row should remain unmigrated (plaintext) since its Save failed")
	}
	if err := db.QueryRow(`SELECT data FROM connections WHERE id = 'good-row'`).Scan(&goodData); err != nil {
		t.Fatalf("reading good-row: %v", err)
	}
	if !strings.HasPrefix(goodData, "vaultenc:v1:") {
		t.Fatalf("expected good-row to be migrated, got: %s", goodData)
	}
}

// TestMigrateConnectionsToVault_SkipsRowLockedByAnotherProcess verifies
// that a connection whose refresh lock is already held (simulating a
// concurrent RefreshToken in another process) is skipped by the migration
// rather than blocked on or clobbered, and correctly reported as
// not-migrated-this-pass so the next startup's pass can retry it.
func TestMigrateConnectionsToVault_SkipsRowLockedByAnotherProcess(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	store := NewStore(db)

	_, err := db.Exec(`
		INSERT INTO connections (id, platform, method, label, account_id, data, status, last_tested, profile_id, created_at, updated_at)
		VALUES ('locked-row', 'x', 'oauth', 'Locked', '', '{"access_token":"plaintext-token"}', 'active', '', 'default', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("seeding plaintext connection: %v", err)
	}

	// Simulate another process (e.g. a concurrent RefreshToken) already
	// holding the refresh lock for this connection.
	acquired, err := store.acquireRefreshLock(ctx, "locked-row")
	if err != nil {
		t.Fatalf("acquireRefreshLock: %v", err)
	}
	if !acquired {
		t.Fatal("expected to acquire the lock as the simulated other process")
	}
	defer store.releaseRefreshLock(ctx, "locked-row")

	// Pin the lock timing: the lock can never go stale under machine load
	// and the migration does not sit out the real wait before giving up.
	oldStale, oldWait := refreshLockStaleAfter, refreshLockWaitTimeout
	refreshLockStaleAfter, refreshLockWaitTimeout = time.Hour, 50*time.Millisecond
	t.Cleanup(func() { refreshLockStaleAfter, refreshLockWaitTimeout = oldStale, oldWait })

	migrated, total, err := MigrateConnectionsToVault(ctx, db)
	if err != nil {
		t.Fatalf("MigrateConnectionsToVault: %v", err)
	}
	if total != 1 {
		t.Fatalf("expected total=1, got %d", total)
	}
	if migrated != 0 {
		t.Fatalf("expected migrated=0 (row is locked by another process), got %d", migrated)
	}

	var rawData string
	if err := db.QueryRow(`SELECT data FROM connections WHERE id = 'locked-row'`).Scan(&rawData); err != nil {
		t.Fatalf("reading locked-row: %v", err)
	}
	if !strings.Contains(rawData, "plaintext-token") {
		t.Fatal("locked-row should remain unmigrated (plaintext) since its lock was held")
	}
}

// TestMigrateConnectionsToVault_DoesNotOverwriteConcurrentRefresh exercises
// the race the round-1 fix missed: the migration's pre-loop ListAll snapshot
// goes stale if a concurrent RefreshToken (in another process, sharing this
// DB) rotates the row's tokens after the snapshot but before the migration
// reaches that row and acquires the (by-then-free) refresh lock. A migration
// that re-saves the stale snapshot instead of re-reading fresh data would
// silently clobber the freshly-rotated tokens with the old ones.
func TestMigrateConnectionsToVault_DoesNotOverwriteConcurrentRefresh(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	store := NewStore(db)

	_, err := db.Exec(`
		INSERT INTO connections (id, platform, method, label, account_id, data, status, last_tested, profile_id, created_at, updated_at)
		VALUES ('conn-1', 'x', 'oauth', 'Test', '', '{"access_token":"stale-token"}', 'active', '', 'default', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("seeding plaintext connection: %v", err)
	}

	// Simulate another process already mid-refresh: it holds the lock before
	// the migration ever gets to this row, so the migration's ListAll
	// snapshot (taken next, once MigrateConnectionsToVault starts) will
	// capture "stale-token" — the pre-refresh data.
	acquired, err := store.acquireRefreshLock(ctx, "conn-1")
	if err != nil {
		t.Fatalf("acquireRefreshLock: %v", err)
	}
	if !acquired {
		t.Fatal("expected to acquire the lock as the simulated other process")
	}

	migDone := make(chan struct{})
	var migrated, total int
	var migErr error
	go func() {
		migrated, total, migErr = MigrateConnectionsToVault(ctx, db)
		close(migDone)
	}()

	// Give the migration time to run its pre-loop ListAll (capturing the
	// stale snapshot) and start blocking on acquireRefreshLock's poll loop
	// for conn-1, since we still hold the lock.
	time.Sleep(50 * time.Millisecond)

	// While still holding the lock (as the simulated other process), rotate
	// the connection's tokens directly — this is what a concurrent
	// RefreshToken's own Save would do. Using store.Save directly (rather
	// than RefreshToken) avoids deadlocking on the lock we're holding.
	fresh, err := store.Get(ctx, "conn-1")
	if err != nil || fresh == nil {
		t.Fatalf("Get conn-1: %v", err)
	}
	fresh.Data["access_token"] = "freshly-rotated-token"
	if err := store.Save(ctx, fresh); err != nil {
		t.Fatalf("simulating concurrent refresh save: %v", err)
	}

	// Release the lock so the blocked migration can proceed.
	store.releaseRefreshLock(ctx, "conn-1")

	<-migDone
	if migErr != nil {
		t.Fatalf("MigrateConnectionsToVault: %v", migErr)
	}
	if total != 1 || migrated != 1 {
		t.Fatalf("expected total=1 migrated=1, got total=%d migrated=%d", total, migrated)
	}

	final, err := store.Get(ctx, "conn-1")
	if err != nil || final == nil {
		t.Fatalf("Get conn-1 after migration: %v", err)
	}
	if got := final.Data["access_token"]; got != "freshly-rotated-token" {
		t.Fatalf("migration overwrote the freshly-rotated token with a stale snapshot: access_token=%v", got)
	}
}
