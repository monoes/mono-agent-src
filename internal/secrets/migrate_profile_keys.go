package secrets

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/monoes/mono-agent/internal/dbconn"
)

// MigrateProfileVaultKeys moves one profile's vault_secrets rows off the
// single pre-per-profile DEK (preserved read-only in vault_keys_legacy by
// migration 027's reconcile) onto a fresh DEK/KEK generated exclusively for
// this profile.
//
// Marker-before-data ordering: the profile's own vault_keys row is created
// (via getOrCreateDEK, under its own cross-process lock) BEFORE any row is
// re-encrypted, and correctness does not depend on that ordering being
// atomic with the data pass — each row is individually idempotent:
//
//   - decrypt under the legacy key succeeds → re-encrypt under the new key
//     (with an immediate round-trip decrypt of the freshly-written
//     ciphertext before it's considered successful);
//   - legacy decrypt fails but the NEW key decrypts → already migrated
//     (e.g. a previous pass wrote the marker, migrated some rows, then
//     crashed before finishing) — skip;
//   - neither key decrypts → per-row error; the row is left untouched
//     rather than lost, and the caller can retry the whole profile safely.
//
// A profile that already has a vault_keys row and no secret rows returns
// early (cheap no-op), and a database with no vault_keys_legacy table never
// knew the legacy scheme at all (fresh install) — also a no-op. A profile
// with no secrets still gets its own key created so it's fully independent
// going forward instead of implicitly depending on the legacy key existing.
//
// The re-encryption pass runs inside one BEGIN IMMEDIATE transaction per
// profile — the same cross-process write-lock pattern bootstrapDEKLocked and
// secrets.addEntry use — so two processes migrating the same profile
// concurrently serialize instead of racing row UPDATEs (the loser observes
// the winner's committed rows and skips them via the already-migrated path
// above).
func MigrateProfileVaultKeys(ctx context.Context, db *sql.DB, profileID string) (migrated int, errs []error) {
	// Fast path: no vault_keys_legacy table → this database never knew the
	// pre-per-profile scheme (fresh install, or pre-017). Nothing to migrate;
	// the profile's own key is created lazily on first real use, as normal.
	var legacyTable int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'vault_keys_legacy'`).Scan(&legacyTable); err != nil {
		return 0, []error{fmt.Errorf("secrets.MigrateProfileVaultKeys: checking legacy table: %w", err)}
	}
	if legacyTable == 0 {
		return 0, nil
	}

	// Cheap guard (NOT the correctness check — that's per-row above): a
	// profile that already has its own key and no secret rows can never have
	// anything to re-encrypt, so skip the keychain round trips entirely.
	var hasKey, rowCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM vault_keys WHERE profile_id = ?`, profileID).Scan(&hasKey); err != nil {
		return 0, []error{fmt.Errorf("secrets.MigrateProfileVaultKeys: checking existing key: %w", err)}
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM vault_secrets WHERE profile_id = ?`, profileID).Scan(&rowCount); err != nil {
		return 0, []error{fmt.Errorf("secrets.MigrateProfileVaultKeys: counting rows: %w", err)}
	}
	if hasKey > 0 && rowCount == 0 {
		return 0, nil
	}

	legacyDEK, found, err := fetchLegacyDEK(ctx, db)
	if err != nil {
		return 0, []error{fmt.Errorf("secrets.MigrateProfileVaultKeys: %w", err)}
	}
	if !found {
		// No legacy key ever existed for this database — nothing to migrate.
		return 0, nil
	}

	// Create this profile's own key (the marker) before touching any data.
	newDEK, err := getOrCreateDEK(ctx, db, profileID)
	if err != nil {
		return 0, []error{fmt.Errorf("secrets.MigrateProfileVaultKeys: creating new key: %w", err)}
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return 0, []error{fmt.Errorf("secrets.MigrateProfileVaultKeys: get conn: %w", err)}
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		dbconn.AbandonBegin(conn)
		return 0, []error{fmt.Errorf("secrets.MigrateProfileVaultKeys: begin tx: %w", err)}
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	rows, err := conn.QueryContext(ctx,
		`SELECT id, ciphertext, nonce, notes_ciphertext, notes_nonce FROM vault_secrets WHERE profile_id = ?`, profileID)
	if err != nil {
		return 0, []error{fmt.Errorf("secrets.MigrateProfileVaultKeys: listing rows: %w", err)}
	}
	type row struct {
		id                          string
		ciphertext, nonce           []byte
		notesCiphertext, notesNonce []byte
	}
	var toMigrate []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.ciphertext, &r.nonce, &r.notesCiphertext, &r.notesNonce); err == nil {
			toMigrate = append(toMigrate, r)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		errs = append(errs, fmt.Errorf("secrets.MigrateProfileVaultKeys: iterating rows: %w", err))
	}

	for _, r := range toMigrate {
		plainFields, decErr := Decrypt(legacyDEK, r.ciphertext, r.nonce)
		if decErr != nil {
			// Not decryptable under the legacy key. If the profile's NEW
			// key decrypts it, the row was already re-encrypted by an
			// earlier (possibly crashed mid-pass) run — skip it.
			if _, vErr := Decrypt(newDEK, r.ciphertext, r.nonce); vErr == nil {
				continue
			}
			errs = append(errs, fmt.Errorf("secrets.MigrateProfileVaultKeys: decrypting %s under legacy key: %w", r.id, decErr))
			continue
		}
		newCiphertext, newNonce, encErr := Encrypt(newDEK, plainFields)
		if encErr != nil {
			errs = append(errs, fmt.Errorf("secrets.MigrateProfileVaultKeys: re-encrypting %s: %w", r.id, encErr))
			continue
		}
		// Round-trip verify before this row's re-encryption is trusted.
		if roundTrip, rtErr := Decrypt(newDEK, newCiphertext, newNonce); rtErr != nil || string(roundTrip) != string(plainFields) {
			errs = append(errs, fmt.Errorf("secrets.MigrateProfileVaultKeys: round-trip verification failed for %s", r.id))
			continue
		}

		var newNotesCiphertext, newNotesNonce []byte
		if len(r.notesCiphertext) > 0 {
			plainNotes, decErr := Decrypt(legacyDEK, r.notesCiphertext, r.notesNonce)
			if decErr != nil {
				errs = append(errs, fmt.Errorf("secrets.MigrateProfileVaultKeys: decrypting notes for %s under legacy key: %w", r.id, decErr))
				continue
			}
			newNotesCiphertext, newNotesNonce, err = Encrypt(newDEK, plainNotes)
			if err != nil {
				errs = append(errs, fmt.Errorf("secrets.MigrateProfileVaultKeys: re-encrypting notes for %s: %w", r.id, err))
				continue
			}
			if roundTrip, rtErr := Decrypt(newDEK, newNotesCiphertext, newNotesNonce); rtErr != nil || string(roundTrip) != string(plainNotes) {
				errs = append(errs, fmt.Errorf("secrets.MigrateProfileVaultKeys: notes round-trip verification failed for %s", r.id))
				continue
			}
		}

		if _, err := conn.ExecContext(ctx,
			`UPDATE vault_secrets SET ciphertext = ?, nonce = ?, notes_ciphertext = ?, notes_nonce = ? WHERE id = ?`,
			newCiphertext, newNonce, newNotesCiphertext, newNotesNonce, r.id,
		); err != nil {
			errs = append(errs, fmt.Errorf("secrets.MigrateProfileVaultKeys: updating %s: %w", r.id, err))
			continue
		}
		migrated++
	}

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return 0, append(errs, fmt.Errorf("secrets.MigrateProfileVaultKeys: commit: %w", err))
	}
	committed = true
	return migrated, errs
}
