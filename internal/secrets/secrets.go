package secrets

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Entry is the credential-free projection of a vault_secrets row — safe to
// list, log, or serialize as --json output. It never carries field names or
// values; only DecryptFields does, and only when explicitly called.
type Entry struct {
	ID         string `json:"id"`
	ProfileID  string `json:"profile_id"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Username   string `json:"username,omitempty"`
	URL        string `json:"url,omitempty"`
	FieldCount int    `json:"field_count"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

// Add creates a new vault_secrets entry, encrypting fields (as one
// JSON-encoded blob) and notes, if given, under the vault's DEK before
// storage. fields must contain at least one non-empty key. kind must be
// "secret" or "login" — this is the public entrypoint (CLI `secret add`,
// GUI "Add New Item"), which must never let a human create a system-managed
// entry by hand. System-managed kinds ("connection", "session",
// "ai_provider") are created exclusively via PutSystemEntry, in system.go.
func Add(ctx context.Context, db *sql.DB, profileID, kind, name string, fields map[string]string, username, url, notes string) (string, error) {
	if kind != "secret" && kind != "login" {
		return "", fmt.Errorf("secrets.Add: invalid kind %q, must be \"secret\" or \"login\"", kind)
	}
	return addEntry(ctx, db, profileID, kind, name, fields, username, url, notes)
}

// addEntry is Add's implementation without the public kind restriction —
// shared by Add itself and by PutSystemEntry (system.go), which creates
// entries of the three system-managed kinds.
func addEntry(ctx context.Context, db *sql.DB, profileID, kind, name string, fields map[string]string, username, url, notes string) (string, error) {
	if err := checkReservedWrite(ctx, db, profileID, "", name); err != nil {
		return "", err
	}
	if len(fields) == 0 {
		return "", fmt.Errorf("secrets.addEntry: at least one field is required")
	}
	for k := range fields {
		if k == "" {
			return "", fmt.Errorf("secrets.addEntry: field keys must not be empty")
		}
	}

	dek, err := getOrCreateDEK(ctx, db, profileID)
	if err != nil {
		return "", fmt.Errorf("secrets.addEntry: %w", err)
	}
	fieldsJSON, err := json.Marshal(fields)
	if err != nil {
		return "", fmt.Errorf("secrets.addEntry: marshaling fields: %w", err)
	}
	ciphertext, nonce, err := Encrypt(dek, fieldsJSON)
	if err != nil {
		return "", fmt.Errorf("secrets.addEntry: encrypting fields: %w", err)
	}

	var notesCiphertext, notesNonce []byte
	if notes != "" {
		notesCiphertext, notesNonce, err = Encrypt(dek, []byte(notes))
		if err != nil {
			return "", fmt.Errorf("secrets.addEntry: encrypting notes: %w", err)
		}
	}

	// Take a dedicated connection and open an IMMEDIATE transaction so the
	// write lock is acquired up front — see vault.Register
	// (internal/vault/vault.go) for why BEGIN IMMEDIATE is required here.
	conn, err := db.Conn(ctx)
	if err != nil {
		return "", fmt.Errorf("secrets.addEntry: get conn: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return "", fmt.Errorf("secrets.addEntry: begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	var seq int
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM vault_secrets`).Scan(&seq); err != nil {
		return "", fmt.Errorf("secrets.addEntry: next seq: %w", err)
	}
	id := fmt.Sprintf("sec-%03d", seq)
	now := time.Now().UTC().Format(time.RFC3339)

	_, err = conn.ExecContext(ctx, `
		INSERT INTO vault_secrets (id, seq, profile_id, kind, name, username, url, ciphertext, nonce, notes_ciphertext, notes_nonce, created_at, updated_at, kv, field_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?)`,
		id, seq, profileID, kind, name, nullStr(username), nullStr(url), ciphertext, nonce, notesCiphertext, notesNonce, now, now, len(fields),
	)
	if err != nil {
		return "", fmt.Errorf("secrets.addEntry: insert: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return "", fmt.Errorf("secrets.addEntry: commit: %w", err)
	}
	committed = true
	return id, nil
}

// DecryptFields returns the decrypted field map and notes text for id, in
// one DEK fetch. This and Update are the only functions in this package
// that ever return or accept plaintext field values.
func DecryptFields(ctx context.Context, db *sql.DB, profileID, id string) (map[string]string, string, error) {
	dek, err := getOrCreateDEK(ctx, db, profileID)
	if err != nil {
		return nil, "", fmt.Errorf("secrets.DecryptFields: %w", err)
	}
	var ciphertext, nonce, notesCiphertext, notesNonce []byte
	err = db.QueryRowContext(ctx,
		`SELECT ciphertext, nonce, notes_ciphertext, notes_nonce FROM vault_secrets WHERE id = ? AND profile_id = ?`, id, profileID,
	).Scan(&ciphertext, &nonce, &notesCiphertext, &notesNonce)
	if err == sql.ErrNoRows {
		return nil, "", fmt.Errorf("secrets.DecryptFields: entry %q not found", id)
	}
	if err != nil {
		return nil, "", fmt.Errorf("secrets.DecryptFields: %w", err)
	}

	fieldsJSON, err := Decrypt(dek, ciphertext, nonce)
	if err != nil {
		return nil, "", fmt.Errorf("secrets.DecryptFields: %w", err)
	}
	var fields map[string]string
	if err := json.Unmarshal(fieldsJSON, &fields); err != nil {
		return nil, "", fmt.Errorf("secrets.DecryptFields: decoding fields: %w", err)
	}

	var notes string
	if len(notesCiphertext) > 0 {
		notesPlain, err := Decrypt(dek, notesCiphertext, notesNonce)
		if err != nil {
			return nil, "", fmt.Errorf("secrets.DecryptFields: decrypting notes: %w", err)
		}
		notes = string(notesPlain)
	}
	return fields, notes, nil
}

// Update applies a partial update to entry id: a nil pointer leaves that
// column untouched, a non-nil pointer sets it (including to ""). fields, if
// non-nil, replaces the entire field map; nil leaves the fields blob
// untouched. Re-encrypts whichever of fields/notes changed under the
// vault's DEK, same as Add.
func Update(ctx context.Context, db *sql.DB, profileID, id string, name, username, url, notes *string, fields map[string]string) error {
	if name != nil && *name == "" {
		return fmt.Errorf("secrets.Update: name must not be empty")
	}
	newName := ""
	if name != nil {
		newName = *name
	}
	if err := checkReservedWrite(ctx, db, profileID, id, newName); err != nil {
		return err
	}
	if fields != nil {
		if len(fields) == 0 {
			return fmt.Errorf("secrets.Update: at least one field is required")
		}
		for k := range fields {
			if k == "" {
				return fmt.Errorf("secrets.Update: field keys must not be empty")
			}
		}
	}

	var dek []byte
	if notes != nil || fields != nil {
		var err error
		dek, err = getOrCreateDEK(ctx, db, profileID)
		if err != nil {
			return fmt.Errorf("secrets.Update: %w", err)
		}
	}

	sets := []string{"updated_at = ?"}
	args := []interface{}{time.Now().UTC().Format(time.RFC3339)}

	if name != nil {
		sets = append(sets, "name = ?")
		args = append(args, *name)
	}
	if username != nil {
		sets = append(sets, "username = ?")
		args = append(args, nullStr(*username))
	}
	if url != nil {
		sets = append(sets, "url = ?")
		args = append(args, nullStr(*url))
	}
	if notes != nil {
		var notesCiphertext, notesNonce []byte
		if *notes != "" {
			var err error
			notesCiphertext, notesNonce, err = Encrypt(dek, []byte(*notes))
			if err != nil {
				return fmt.Errorf("secrets.Update: encrypting notes: %w", err)
			}
		}
		sets = append(sets, "notes_ciphertext = ?", "notes_nonce = ?")
		args = append(args, notesCiphertext, notesNonce)
	}
	if fields != nil {
		fieldsJSON, err := json.Marshal(fields)
		if err != nil {
			return fmt.Errorf("secrets.Update: marshaling fields: %w", err)
		}
		ciphertext, nonce, err := Encrypt(dek, fieldsJSON)
		if err != nil {
			return fmt.Errorf("secrets.Update: encrypting fields: %w", err)
		}
		sets = append(sets, "ciphertext = ?", "nonce = ?", "field_count = ?")
		args = append(args, ciphertext, nonce, len(fields))
	}

	args = append(args, id, profileID)
	query := fmt.Sprintf(`UPDATE vault_secrets SET %s WHERE id = ? AND profile_id = ?`, strings.Join(sets, ", "))
	res, err := db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("secrets.Update: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("secrets.Update: entry %q not found", id)
	}
	return nil
}

// List returns metadata for every entry under profileID — never decrypts.
func List(ctx context.Context, db *sql.DB, profileID string) ([]Entry, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, profile_id, kind, name, COALESCE(username,''), COALESCE(url,''), field_count, created_at, updated_at
		FROM vault_secrets WHERE profile_id = ? ORDER BY seq DESC`, profileID)
	if err != nil {
		return nil, fmt.Errorf("secrets.List: %w", err)
	}
	defer rows.Close()

	var out []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.ProfileID, &e.Kind, &e.Name, &e.Username, &e.URL, &e.FieldCount, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, fmt.Errorf("secrets.List: scan: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Delete removes an entry.
func Delete(ctx context.Context, db *sql.DB, profileID, id string) error {
	if err := checkReservedWrite(ctx, db, profileID, id); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, `DELETE FROM vault_secrets WHERE id = ? AND profile_id = ?`, id, profileID)
	if err != nil {
		return fmt.Errorf("secrets.Delete: %w", err)
	}
	return nil
}

func nullStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
