package secrets

import (
	"context"
	"database/sql"
	"fmt"
)

// systemKinds are the vault_secrets kinds created exclusively by other
// subsystems (connections, crawler sessions) via PutSystemEntry, never via
// the public Add/CLI `secret add --kind`. The retired "ai_provider" kind is
// handled by RetireAIProviderEntries.
var systemKinds = map[string]bool{
	"connection": true,
	"session":    true,
}

// systemTableForKind maps a system kind to the table holding its owning
// row, for DeleteCascade and the raw-SQL Meta lookups Export uses. Referenced
// by literal table name rather than by importing internal/connections,
// which would cycle back to this package.
var systemTableForKind = map[string]string{
	"connection": "connections",
	"session":    "crawler_sessions",
}

// PutSystemEntry upserts a system-managed vault entry (kind must be
// "connection" or "session") on behalf of another
// subsystem. If existingID is non-empty, the entry's fields (and
// username/url) are updated in place and its name is left untouched — a
// rename the user made in the Vault UI is never silently reverted by the
// next token refresh. If existingID is empty, a fresh entry is created,
// disambiguating a name collision by appending " (2)", " (3)", etc. Returns
// the vault_secrets id to persist back into the caller's vault_ref column.
func PutSystemEntry(ctx context.Context, db *sql.DB, profileID, kind, existingID, name string, fields map[string]string, username, url string) (string, error) {
	if !systemKinds[kind] {
		return "", fmt.Errorf("secrets.PutSystemEntry: invalid system kind %q", kind)
	}
	if existingID != "" {
		usernamePtr, urlPtr := &username, &url
		if err := Update(ctx, db, profileID, existingID, nil, usernamePtr, urlPtr, nil, fields); err != nil {
			return "", fmt.Errorf("secrets.PutSystemEntry: updating %s: %w", existingID, err)
		}
		return existingID, nil
	}

	uniqueName, err := disambiguateName(ctx, db, profileID, name)
	if err != nil {
		return "", fmt.Errorf("secrets.PutSystemEntry: %w", err)
	}
	id, err := addEntry(ctx, db, profileID, kind, uniqueName, fields, username, url, "")
	if err != nil {
		return "", fmt.Errorf("secrets.PutSystemEntry: %w", err)
	}
	return id, nil
}

// disambiguateName returns name unchanged if no entry under profileID
// already has it, otherwise "name (2)", "name (3)", etc. — the first
// variant not already in use.
func disambiguateName(ctx context.Context, db *sql.DB, profileID, name string) (string, error) {
	entries, err := List(ctx, db, profileID)
	if err != nil {
		return "", fmt.Errorf("checking existing names: %w", err)
	}
	taken := make(map[string]bool, len(entries))
	for _, e := range entries {
		taken[e.Name] = true
	}
	if !taken[name] {
		return name, nil
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s (%d)", name, i)
		if !taken[candidate] {
			return candidate, nil
		}
	}
}

// DeleteCascade deletes vault_secrets entry id and, if its kind is
// system-managed, the linked row in connections/crawler_sessions
// (matched by vault_ref = id) too — the vault entry and the linked row are
// the same credential, so deleting one and not the other would leave the
// app pointing at a token that no longer exists anywhere. For kind
// "secret"/"login" this is equivalent to plain Delete.
func DeleteCascade(ctx context.Context, db *sql.DB, profileID, id string) error {
	if err := checkReservedWrite(ctx, db, profileID, id); err != nil {
		return err
	}
	var kind string
	err := db.QueryRowContext(ctx, `SELECT kind FROM vault_secrets WHERE id = ? AND profile_id = ?`, id, profileID).Scan(&kind)
	if err == sql.ErrNoRows {
		return fmt.Errorf("secrets.DeleteCascade: entry %q not found", id)
	}
	if err != nil {
		return fmt.Errorf("secrets.DeleteCascade: %w", err)
	}

	if table, ok := systemTableForKind[kind]; ok {
		q := fmt.Sprintf(`DELETE FROM %s WHERE vault_ref = ? AND profile_id = ?`, table)
		if _, err := db.ExecContext(ctx, q, id, profileID); err != nil {
			return fmt.Errorf("secrets.DeleteCascade: deleting linked %s row: %w", table, err)
		}
	}
	return Delete(ctx, db, profileID, id)
}
