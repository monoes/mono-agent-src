package vault

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Audio and video found in a profile's folder are tracked only when the
// profile has switched that on (`media scan on`): a project folder can hold
// many large recordings nobody meant for the vault. The switch is one row of
// the settings table per profile, absent (off) by default. Images in the
// folder are always tracked. The switch also decides whether a workflow
// node's audio or video output is registered on its own (an image always
// is). Audio and video the user adds with `media add` or the extension saves
// are never affected by it.

func scanAVKey(profileID string) string { return "media_scan_av:" + profileID }

// ScanAVEnabled reports whether `media sync` tracks audio and video in the
// profile's folder.
func ScanAVEnabled(ctx context.Context, db *sql.DB, profileID string) (bool, error) {
	var v string
	err := db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, scanAVKey(profileID)).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("vault.ScanAVEnabled: %w", err)
	}
	return v == "on", nil
}

// SetScanAV switches audio and video discovery on or off for the profile.
// The next `media sync` tracks them, or drops the rows it had discovered
// (the files stay where they are).
func SetScanAV(ctx context.Context, db *sql.DB, profileID string, on bool) error {
	if profileID == "" {
		return fmt.Errorf("vault.SetScanAV: %w", ErrNoProfile)
	}
	v := "off"
	if on {
		v = "on"
	}
	_, err := db.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, scanAVKey(profileID), v)
	if err != nil {
		return fmt.Errorf("vault.SetScanAV: %w", err)
	}
	return nil
}
