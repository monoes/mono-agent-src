package vault

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"

	"github.com/monoes/mono-agent/internal/dbconn"
)

// RegisterDiscoveredImage records an image file found on disk under the
// user's project directory as a vault_images entry with source="discovered".
// The file is tracked in-place and is NEVER copied into the vault folder.
// Idempotent: returns the existing id and created=false if (profileID, path)
// is already tracked.
func RegisterDiscoveredImage(ctx context.Context, db *sql.DB, profileID, path, filename string, sizeBytes int64) (id string, created bool, err error) {
	if db == nil {
		return "", false, fmt.Errorf("vault.RegisterDiscoveredImage: db is nil")
	}
	if path == "" {
		return "", false, fmt.Errorf("vault.RegisterDiscoveredImage: path is empty")
	}
	if profileID == "" {
		return "", false, fmt.Errorf("vault.RegisterDiscoveredImage: %w", ErrNoProfile)
	}
	if filename == "" {
		filename = filepath.Base(path)
	}

	if existingID, ok, lookupErr := imageIDByPath(ctx, db, profileID, path); lookupErr != nil {
		return "", false, fmt.Errorf("vault.RegisterDiscoveredImage: %w", lookupErr)
	} else if ok {
		return existingID, false, nil
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return "", false, fmt.Errorf("vault.RegisterDiscoveredImage: get conn: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		dbconn.AbandonBegin(conn)
		return "", false, fmt.Errorf("vault.RegisterDiscoveredImage: begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	var existingID string
	err = conn.QueryRowContext(ctx, `SELECT id FROM vault_images WHERE profile_id = ? AND path = ?`, profileID, path).Scan(&existingID)
	if err == nil {
		if _, rbErr := conn.ExecContext(ctx, "ROLLBACK"); rbErr == nil {
			committed = true
		}
		return existingID, false, nil
	}
	if err != sql.ErrNoRows {
		return "", false, fmt.Errorf("vault.RegisterDiscoveredImage: checking existing: %w", err)
	}

	var seq int
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM vault_images`).Scan(&seq); err != nil {
		return "", false, fmt.Errorf("vault.RegisterDiscoveredImage: get next seq: %w", err)
	}
	id = fmt.Sprintf("img-%03d", seq)

	_, err = conn.ExecContext(ctx, `
		INSERT INTO vault_images (id, seq, path, filename, size_bytes, source, profile_id, created_at)
		VALUES (?, ?, ?, ?, ?, 'discovered', ?, datetime('now'))`,
		id, seq, path, filename, sizeBytes, profileID,
	)
	if err != nil {
		return "", false, fmt.Errorf("vault.RegisterDiscoveredImage: insert: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return "", false, fmt.Errorf("vault.RegisterDiscoveredImage: commit: %w", err)
	}
	committed = true
	return id, true, nil
}

func imageIDByPath(ctx context.Context, db *sql.DB, profileID, path string) (id string, ok bool, err error) {
	err = db.QueryRowContext(ctx, `SELECT id FROM vault_images WHERE profile_id = ? AND path = ?`, profileID, path).Scan(&id)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return id, true, nil
}

// ReconcileDiscoveredImages registers a row for every image file in found not
// already tracked under profileID by path, refreshes size_bytes for ones
// that already are, and removes any "discovered"-sourced row whose path no
// longer appears in found (e.g. a deleted or moved project image).
//
// A row of any source whose path is in found counts as tracking that file
// (the chat, workflows and `image add` record profile-folder files in
// place), and paths in vault_image_ignored — images the user deleted but
// kept on disk — are skipped. Only rows with source="discovered" are ever
// candidates for removal: manually uploaded, workflow-generated, or
// chat-generated images are never purged here.
func ReconcileDiscoveredImages(ctx context.Context, db *sql.DB, profileID string, found []DiscoveredFile) (added int, removed int, errs []error) {
	r := SyncDiscoveredImages(ctx, db, profileID, found)
	return r.Added, r.Removed, r.Errs
}

// SyncDiscoveredImages is ReconcileDiscoveredImages with the size
// refreshes counted too -- what `image sync` reports.
func SyncDiscoveredImages(ctx context.Context, db *sql.DB, profileID string, found []DiscoveredFile) (res ReconcileResult) {
	if profileID == "" {
		return ReconcileResult{Errs: []error{fmt.Errorf("vault.SyncDiscoveredImages: %w", ErrNoProfile)}}
	}
	type trackedRow struct {
		size       int64
		discovered bool
	}
	existing := make(map[string]trackedRow, len(found))
	rows, err := db.QueryContext(ctx, `SELECT path, size_bytes, source FROM vault_images WHERE profile_id = ?`, profileID)
	if err != nil {
		res.Errs = []error{fmt.Errorf("vault.ReconcileDiscoveredImages: loading existing: %w", err)}
		return res
	}
	for rows.Next() {
		var path, source string
		var size int64
		if scanErr := rows.Scan(&path, &size, &source); scanErr != nil {
			rows.Close()
			res.Errs = []error{fmt.Errorf("vault.ReconcileDiscoveredImages: scanning existing: %w", scanErr)}
			return res
		}
		// Two rows can share a path (an in-place row and a discovered
		// duplicate left by an older build); either one tracks the file.
		prev := existing[path]
		existing[path] = trackedRow{size: size, discovered: prev.discovered || source == "discovered"}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		res.Errs = []error{fmt.Errorf("vault.ReconcileDiscoveredImages: %w", err)}
		return res
	}
	ignored, err := ignoredImagePaths(ctx, db, profileID)
	if err != nil {
		res.Errs = []error{fmt.Errorf("vault.ReconcileDiscoveredImages: loading ignored: %w", err)}
		return res
	}

	foundPaths := make(map[string]bool, len(found))
	for _, f := range found {
		foundPaths[f.Path] = true
		row, tracked := existing[f.Path]
		if !tracked {
			if ignored[f.Path] {
				continue
			}
			// RegisterDiscoveredImage re-checks by path inside its
			// transaction, so a row another process just added for the
			// file is kept and reports created=false.
			_, created, regErr := RegisterDiscoveredImage(ctx, db, profileID, f.Path, f.Filename, f.SizeBytes)
			if regErr != nil {
				res.Errs = append(res.Errs, fmt.Errorf("registering %s: %w", f.Path, regErr))
				continue
			}
			if created {
				res.Added++
			}
			continue
		}
		if row.size != f.SizeBytes {
			if _, updErr := db.ExecContext(ctx, `UPDATE vault_images SET size_bytes = ? WHERE profile_id = ? AND path = ?`, f.SizeBytes, profileID, f.Path); updErr != nil {
				res.Errs = append(res.Errs, fmt.Errorf("refreshing size for %s: %w", f.Path, updErr))
				continue
			}
			res.Updated++
		}
	}

	for path, row := range existing {
		if foundPaths[path] || !row.discovered {
			continue
		}
		if _, delErr := db.ExecContext(ctx, `DELETE FROM vault_images WHERE profile_id = ? AND path = ? AND source = 'discovered'`, profileID, path); delErr != nil {
			res.Errs = append(res.Errs, fmt.Errorf("removing vanished %s: %w", path, delErr))
			continue
		}
		res.Removed++
	}
	return res
}
