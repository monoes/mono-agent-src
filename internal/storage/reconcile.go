package storage

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	"github.com/monoes/mono-agent/internal/dbconn"
)

// vaultKeysPerProfileDDL is the per-profile vault_keys shape introduced by
// migration 027. It is duplicated here (SQLite can't parameterize DDL) and
// must stay byte-compatible with 027_vault_keys_per_profile.sql.
const vaultKeysPerProfileDDL = `CREATE TABLE IF NOT EXISTS vault_keys (
    profile_id    TEXT PRIMARY KEY,
    wrapped_dek   BLOB NOT NULL,
    wrapped_nonce BLOB NOT NULL,
    created_at    TEXT NOT NULL
)`

// ReconcileSchema repairs known schema-shape drift by inspecting the actual
// tables (PRAGMA table_info / sqlite_master) instead of trusting the
// recorded schema_migrations versions. It exists because schema_migrations
// keys on the version INT alone: two branches shipped different files at
// versions 023/024, so databases from the branch whose 023/024 were index
// migrations had the vault migrations (per-profile vault_keys,
// profiles.root_dir) permanently skipped by the merged binary — every vault
// operation then failed with "no such column: profile_id". Reconcile is a
// no-op on databases whose shape is already correct (fresh installs, and
// databases that recorded the vault pair as their 023/024), heals drifted
// ones, and is idempotent, so it runs at the end of every ApplyMigrations —
// which every DB open path (CLI initDB, MCP bootstrap, wails startup)
// already funnels through.
func (d *Database) ReconcileSchema(ctx context.Context) error {
	if err := d.reconcileVaultKeys(ctx); err != nil {
		return err
	}
	if err := d.reconcileProfilesRootDir(ctx); err != nil {
		return err
	}
	if err := d.reconcileProfilesIcon(ctx); err != nil {
		return err
	}
	if err := d.reconcileAddedColumns(ctx); err != nil {
		return err
	}
	if err := d.reconcileExecutionProfiles(ctx); err != nil {
		return err
	}
	return d.reconcileLegacyOwners(ctx)
}

// reconcileVaultKeys moves an old singleton-shape vault_keys (no profile_id
// column — the pre-per-profile layout created by migration 017) out of the
// way, preserving it byte-for-byte as vault_keys_legacy exactly the way the
// original 023 migration's RENAME did, and creates the per-profile
// vault_keys table in its place. Databases that already have the
// per-profile shape are untouched.
func (d *Database) reconcileVaultKeys(ctx context.Context) error {
	exists, err := d.tableExists(ctx, "vault_keys")
	if err != nil {
		return fmt.Errorf("storage: reconcile: checking vault_keys: %w", err)
	}
	if !exists {
		// No vault_keys at all yet (a database that pre-dates migration
		// 017): create the per-profile table directly.
		if _, err := d.DB.ExecContext(ctx, vaultKeysPerProfileDDL); err != nil {
			return fmt.Errorf("storage: reconcile: creating per-profile vault_keys: %w", err)
		}
		log.Printf("storage: reconcile: created per-profile vault_keys table (none existed)")
		return nil
	}

	hasProfileID, err := d.columnExists(ctx, "vault_keys", "profile_id")
	if err != nil {
		return fmt.Errorf("storage: reconcile: inspecting vault_keys: %w", err)
	}
	if hasProfileID {
		return nil // already the per-profile shape
	}

	// Old singleton shape: do the RENAME half of migration 027 under a
	// cross-process write lock, re-checking inside it so a concurrent (or
	// duplicate) reconcile is a no-op rather than a duplicate-table error.
	// BEGIN IMMEDIATE specifically (not DEFERRED) acquires SQLite's write
	// lock up front — the same pattern bootstrapDEKLocked (internal/secrets)
	// and vault.Register use for cross-process singleton work.
	conn, err := d.DB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("storage: reconcile: get conn: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		dbconn.AbandonBegin(conn)
		return fmt.Errorf("storage: reconcile: begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	// Re-check under the lock: another process may have reconciled between
	// the shape peek above and acquiring the write lock.
	hasProfileID, err = columnExistsOn(ctx, conn, "vault_keys", "profile_id")
	if err != nil {
		return fmt.Errorf("storage: reconcile: re-inspecting vault_keys: %w", err)
	}
	if !hasProfileID {
		legacyExists, err := tableExistsOn(ctx, conn, "vault_keys_legacy")
		if err != nil {
			return fmt.Errorf("storage: reconcile: checking vault_keys_legacy: %w", err)
		}
		if legacyExists {
			// Should be unreachable (an old-shape vault_keys alongside an
			// existing vault_keys_legacy matches no migration history this
			// code can produce). Renaming would collide, so leave the data
			// intact and surface the anomaly instead of guessing.
			log.Printf("storage: reconcile: WARN: vault_keys lacks profile_id but vault_keys_legacy already exists — leaving both untouched")
		} else {
			if _, err := conn.ExecContext(ctx, `ALTER TABLE vault_keys RENAME TO vault_keys_legacy`); err != nil {
				return fmt.Errorf("storage: reconcile: renaming old vault_keys to vault_keys_legacy: %w", err)
			}
			log.Printf("storage: reconcile: preserved old singleton vault_keys as vault_keys_legacy")
		}
		if _, err := conn.ExecContext(ctx, vaultKeysPerProfileDDL); err != nil {
			return fmt.Errorf("storage: reconcile: creating per-profile vault_keys: %w", err)
		}
		log.Printf("storage: reconcile: created per-profile vault_keys table")
	}

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("storage: reconcile: commit: %w", err)
	}
	committed = true
	return nil
}

// reconcileProfilesRootDir adds profiles.root_dir (the 028 migration's
// column) when missing — the ALTER-guard counterpart of reconcileVaultKeys:
// SQLite cannot express "ADD COLUMN IF NOT EXISTS", so the shape is checked
// via PRAGMA instead. No-op when the column is already there.
func (d *Database) reconcileProfilesRootDir(ctx context.Context) error {
	exists, err := d.tableExists(ctx, "profiles")
	if err != nil {
		return fmt.Errorf("storage: reconcile: checking profiles: %w", err)
	}
	if !exists {
		return nil
	}
	hasRootDir, err := d.columnExists(ctx, "profiles", "root_dir")
	if err != nil {
		return fmt.Errorf("storage: reconcile: inspecting profiles: %w", err)
	}
	if hasRootDir {
		return nil
	}

	conn, err := d.DB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("storage: reconcile: get conn: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		dbconn.AbandonBegin(conn)
		return fmt.Errorf("storage: reconcile: begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	// Re-check under the lock, mirroring reconcileVaultKeys.
	hasRootDir, err = columnExistsOn(ctx, conn, "profiles", "root_dir")
	if err != nil {
		return fmt.Errorf("storage: reconcile: re-inspecting profiles: %w", err)
	}
	if !hasRootDir {
		if _, err := conn.ExecContext(ctx, `ALTER TABLE profiles ADD COLUMN root_dir TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("storage: reconcile: adding profiles.root_dir: %w", err)
		}
		log.Printf("storage: reconcile: added profiles.root_dir column")
	}

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("storage: reconcile: commit: %w", err)
	}
	committed = true
	return nil
}

// reconcileProfilesIcon adds profiles.icon (the 039 migration's column)
// when missing — same ALTER-guard shape as reconcileProfilesRootDir, since
// SQLite still can't express "ADD COLUMN IF NOT EXISTS". No-op when the
// column is already there. Empty string means "no icon chosen", matching
// how an empty root_dir already means "use the default location".
func (d *Database) reconcileProfilesIcon(ctx context.Context) error {
	exists, err := d.tableExists(ctx, "profiles")
	if err != nil {
		return fmt.Errorf("storage: reconcile: checking profiles: %w", err)
	}
	if !exists {
		return nil
	}
	hasIcon, err := d.columnExists(ctx, "profiles", "icon")
	if err != nil {
		return fmt.Errorf("storage: reconcile: inspecting profiles: %w", err)
	}
	if hasIcon {
		return nil
	}

	conn, err := d.DB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("storage: reconcile: get conn: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		dbconn.AbandonBegin(conn)
		return fmt.Errorf("storage: reconcile: begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	// Re-check under the lock, mirroring reconcileProfilesRootDir.
	hasIcon, err = columnExistsOn(ctx, conn, "profiles", "icon")
	if err != nil {
		return fmt.Errorf("storage: reconcile: re-inspecting profiles: %w", err)
	}
	if !hasIcon {
		if _, err := conn.ExecContext(ctx, `ALTER TABLE profiles ADD COLUMN icon TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("storage: reconcile: adding profiles.icon: %w", err)
		}
		log.Printf("storage: reconcile: added profiles.icon column")
	}

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("storage: reconcile: commit: %w", err)
	}
	committed = true
	return nil
}

// legacyOwnerTables are the tables migration 099 cannot stamp in SQL because
// their profile_id column may not exist yet when it runs: vault_keys (the
// per-profile shape is created by reconcile). ai_chat_messages is deliberately
// absent: its profile_id ” is a sentinel for ambiguous legacy global-assistant
// chats that must never be returned to any profile (see ai.AIStore).
var legacyOwnerTables = []string{"vault_keys"}

// reconcileLegacyOwners gives a legacyOwnerTables row with a NULL or empty
// profile_id the same owner migration 099 gives every other legacy row: the
// profile 'default' if it exists, else the oldest. Idempotent; a no-op
// without profiles. A row that would collide with the stamp profile's own row
// stays as it was.
func (d *Database) reconcileLegacyOwners(ctx context.Context) error {
	profiles, err := d.tableExists(ctx, "profiles")
	if err != nil {
		return fmt.Errorf("storage: reconcile: checking profiles: %w", err)
	}
	if !profiles {
		return nil
	}
	owner, err := d.LegacyOwnerProfile(ctx)
	if err != nil {
		return err
	}
	if owner == "" {
		return nil
	}
	for _, table := range legacyOwnerTables {
		has, err := d.columnExists(ctx, table, "profile_id")
		if err != nil {
			return fmt.Errorf("storage: reconcile: inspecting %s: %w", table, err)
		}
		if !has {
			continue
		}
		if _, err := d.DB.ExecContext(ctx, `UPDATE OR IGNORE `+table+`
			SET profile_id = ?
			WHERE (profile_id IS NULL OR profile_id = '')`, owner); err != nil {
			return fmt.Errorf("storage: reconcile: stamping %s owner: %w", table, err)
		}
	}
	return nil
}

// reconcileExecutionProfiles backfills workflow_executions.profile_id from
// the workflow each execution belongs to. Executions created before per-
// profile execution stamping all recorded 'default' even when their workflow
// belonged to another profile, which made per-profile execution listings
// miss them forever. Idempotent: rows whose profile_id is already non-
// 'default' are excluded, rows whose workflow is also 'default' (or gone —
// orphaned executions) are deliberately left alone.
func (d *Database) reconcileExecutionProfiles(ctx context.Context) error {
	weExists, err := d.tableExists(ctx, "workflow_executions")
	if err != nil {
		return fmt.Errorf("storage: reconcile: checking workflow_executions: %w", err)
	}
	if !weExists {
		return nil
	}
	wfExists, err := d.tableExists(ctx, "workflows")
	if err != nil {
		return fmt.Errorf("storage: reconcile: checking workflows: %w", err)
	}
	if !wfExists {
		return nil
	}
	weHas, err := d.columnExists(ctx, "workflow_executions", "profile_id")
	if err != nil {
		return fmt.Errorf("storage: reconcile: inspecting workflow_executions: %w", err)
	}
	wfHas, err := d.columnExists(ctx, "workflows", "profile_id")
	if err != nil {
		return fmt.Errorf("storage: reconcile: inspecting workflows: %w", err)
	}
	if !weHas || !wfHas {
		return nil
	}

	res, err := d.DB.ExecContext(ctx, `
		UPDATE workflow_executions
		SET profile_id = (SELECT w.profile_id FROM workflows w WHERE w.id = workflow_executions.workflow_id)
		WHERE profile_id = 'default'
		  AND EXISTS (SELECT 1 FROM workflows w
		              WHERE w.id = workflow_executions.workflow_id
		                AND w.profile_id <> 'default' AND w.profile_id <> '')`)
	if err != nil {
		return fmt.Errorf("storage: reconcile: backfilling execution profiles: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("storage: reconcile: backfilled profile_id on %d workflow execution(s)", n)
	}
	return nil
}

// tableExists reports whether the named table is present in sqlite_master.
func (d *Database) tableExists(ctx context.Context, table string) (bool, error) {
	return tableExistsOn(ctx, d.DB, table)
}

func tableExistsOn(ctx context.Context, q interface {
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
}, table string) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// columnExists reports whether the named table has the named column,
// determined via PRAGMA table_info (shape, not recorded versions).
func (d *Database) columnExists(ctx context.Context, table, column string) (bool, error) {
	return columnExistsOn(ctx, d.DB, table, column)
}

func columnExistsOn(ctx context.Context, q interface {
	QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
}, table, column string) (bool, error) {
	rows, err := q.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt interface{}
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}
