package vault

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/monoes/mono-agent/internal/dbconn"
	"github.com/monoes/mono-agent/internal/imagescan"
	"github.com/monoes/mono-agent/internal/profiledir"
)

// Images that live in the profile's own folder.
//
// `image sync` tracks every image file in the profile folder in place. An
// image the chat, a workflow or `image add` puts in that folder is the same
// file, so it is recorded in place too (keeping its own source) rather than
// copied into .monoagent/vault — otherwise the next sync would list it a
// second time. Deleting such an image keeps the file, like a discovered
// one, and remembers the path in vault_image_ignored so sync leaves it out
// until it is registered again.

// profileFolderPath reports whether abs is a file `image sync` would find in
// the profile's folder, and returns it in the form the scan produces
// (joined onto profiledir.Root), so rows written here match scanned paths.
// A folder reached through a symlink is compared by its resolved location.
func profileFolderPath(db *sql.DB, profileID, abs string) (string, bool) {
	root := profiledir.Root(db, profileID)
	if root == "" {
		return "", false
	}
	if imagescan.InScannedTree(root, abs) {
		rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(abs))
		if err != nil {
			return "", false
		}
		return filepath.Join(root, rel), true
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", false
	}
	realAbs, err := filepath.EvalSymlinks(abs)
	if err != nil || !imagescan.InScannedTree(realRoot, realAbs) {
		return "", false
	}
	rel, err := filepath.Rel(realRoot, realAbs)
	if err != nil {
		return "", false
	}
	return filepath.Join(root, rel), true
}

// registerInPlace records the profile-folder file at path as an image with
// the given source, without copying it, and clears any ignore entry for it
// (registering a path again is how a deleted image comes back). A path that
// already has a row, of any source, keeps that row: its size is refreshed
// and its id returned.
func registerInPlace(ctx context.Context, db *sql.DB, profileID, path, source, workflowID, executionID string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("vault.Register: stat src: %w", err)
	}
	if fi.IsDir() {
		return "", fmt.Errorf("vault.Register: %s is a directory", path)
	}

	// BEGIN IMMEDIATE for the same reason as Register: seq allocation must
	// be serialized across connections and processes.
	conn, err := db.Conn(ctx)
	if err != nil {
		return "", fmt.Errorf("vault.Register: get conn: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		dbconn.AbandonBegin(conn)
		return "", fmt.Errorf("vault.Register: begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	if _, err := conn.ExecContext(ctx, `DELETE FROM vault_image_ignored WHERE profile_id = ? AND path = ?`, profileID, path); err != nil {
		return "", fmt.Errorf("vault.Register: clear ignore: %w", err)
	}

	var id string
	err = conn.QueryRowContext(ctx, `SELECT id FROM vault_images WHERE profile_id = ? AND path = ?`, profileID, path).Scan(&id)
	switch {
	case err == nil:
		if _, err := conn.ExecContext(ctx, `UPDATE vault_images SET size_bytes = ? WHERE id = ? AND profile_id = ?`, fi.Size(), id, profileID); err != nil {
			return "", fmt.Errorf("vault.Register: refresh size: %w", err)
		}
		if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
			return "", fmt.Errorf("vault.Register: commit: %w", err)
		}
		committed = true
		return id, nil
	case !errors.Is(err, sql.ErrNoRows):
		return "", fmt.Errorf("vault.Register: checking existing: %w", err)
	}

	var seq int
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM vault_images`).Scan(&seq); err != nil {
		return "", fmt.Errorf("vault.Register: get next seq: %w", err)
	}
	id = fmt.Sprintf("img-%03d", seq)
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO vault_images (id, seq, path, filename, size_bytes, source, workflow_id, execution_id, profile_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, seq, path, filepath.Base(path), fi.Size(), source,
		nullIfEmpty(workflowID), nullIfEmpty(executionID), profileID,
	); err != nil {
		return "", fmt.Errorf("vault.Register: insert: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return "", fmt.Errorf("vault.Register: commit: %w", err)
	}
	committed = true

	syncImageToKG(db, profileID, id, source, workflowID, executionID)
	return id, nil
}

// UnignoreImagePath forgets that the user deleted the image at path, so the
// next `image sync` registers it again if it is still in the profile
// folder. It reports whether there was an entry to remove.
func UnignoreImagePath(ctx context.Context, db *sql.DB, profileID, path string) (bool, error) {
	res, err := db.ExecContext(ctx, `DELETE FROM vault_image_ignored WHERE profile_id = ? AND path = ?`, profileID, path)
	if err != nil {
		return false, fmt.Errorf("vault.UnignoreImagePath: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ignoredImagePaths returns the profile's ignored image paths.
func ignoredImagePaths(ctx context.Context, db *sql.DB, profileID string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT path FROM vault_image_ignored WHERE profile_id = ?`, profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out[p] = true
	}
	return out, rows.Err()
}
