// internal/vault/capture_documents.go
package vault

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/monoes/mono-agent/internal/dbconn"
)

// CaptureDocument is one browser capture (an inbox envelope directory) as
// handed to ReconcileCaptureDocuments. Like DiscoveredFile it is a plain
// struct rather than internal/capture's Entry, so this DB-owning package
// never has to import the capture package; internal/capturedocs does the
// mapping.
type CaptureDocument struct {
	// Dir is the envelope directory; it is the capture's identity.
	Dir string
	// Path is the artifact the row opens and indexes (see
	// capturedocs.PrimaryArtifact).
	Path string
	// Title is shown as the document's name.
	Title string
	// URL is the page the capture was taken from.
	URL string
	// Source is meta.json's source ("extension", "crawl", "monobrowse").
	Source string
	// CapturedAt is the capture time as "2006-01-02 15:04:05" UTC, the
	// same shape datetime('now') gives every other row's created_at, so
	// the two sort together.
	CapturedAt string
	SizeBytes  int64
}

// ReconcileCaptureDocuments makes profileID's capture-backed rows match
// found: a row is added for every capture not yet tracked (matched by
// envelope directory), an existing row's path, name, url and size are
// refreshed when they drift, and a capture-backed row whose directory is
// no longer in found is removed. found must be the profile inbox's full
// current listing, never a delta.
//
// Only rows with a capture_dir are ever touched, so an uploaded or
// discovered document can never be removed here. Removing a row never
// touches the disk: the capture is already gone.
//
// Continues past per-capture errors, collecting them.
func ReconcileCaptureDocuments(ctx context.Context, db *sql.DB, profileID string, found []CaptureDocument) (added, removed int, errs []error) {
	type row struct {
		id, path, filename, url string
		size                    int64
	}
	existing := map[string]row{}
	rows, err := db.QueryContext(ctx,
		`SELECT id, capture_dir, path, filename, COALESCE(url, ''), size_bytes FROM vault_documents
		 WHERE profile_id = ? AND capture_dir IS NOT NULL AND capture_dir != ''`, profileID)
	if err != nil {
		return 0, 0, []error{fmt.Errorf("vault.ReconcileCaptureDocuments: loading existing: %w", err)}
	}
	for rows.Next() {
		var dir string
		var r row
		if scanErr := rows.Scan(&r.id, &dir, &r.path, &r.filename, &r.url, &r.size); scanErr != nil {
			rows.Close()
			return 0, 0, []error{fmt.Errorf("vault.ReconcileCaptureDocuments: scanning existing: %w", scanErr)}
		}
		existing[dir] = r
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, []error{fmt.Errorf("vault.ReconcileCaptureDocuments: %w", err)}
	}

	seen := make(map[string]bool, len(found))
	for _, c := range found {
		seen[c.Dir] = true
		name := captureDisplayName(c)
		r, tracked := existing[c.Dir]
		if !tracked {
			_, created, regErr := registerCaptureDocument(ctx, db, profileID, c, name)
			if regErr != nil {
				errs = append(errs, fmt.Errorf("registering capture %s: %w", c.Dir, regErr))
				continue
			}
			if created {
				added++
			}
			continue
		}
		if r.path == c.Path && r.filename == name && r.url == c.URL && r.size == c.SizeBytes {
			continue
		}
		if _, updErr := db.ExecContext(ctx,
			`UPDATE vault_documents SET path = ?, filename = ?, url = ?, size_bytes = ? WHERE id = ? AND profile_id = ?`,
			c.Path, name, nullIfEmpty(c.URL), c.SizeBytes, r.id, profileID); updErr != nil {
			errs = append(errs, fmt.Errorf("refreshing capture %s: %w", c.Dir, updErr))
		}
	}

	for dir, r := range existing {
		if seen[dir] {
			continue
		}
		if _, delErr := db.ExecContext(ctx, `DELETE FROM vault_documents WHERE id = ? AND profile_id = ?`, r.id, profileID); delErr != nil {
			errs = append(errs, fmt.Errorf("removing vanished capture %s: %w", dir, delErr))
			continue
		}
		removed++
	}
	return added, removed, errs
}

// registerCaptureDocument inserts one capture-backed row, idempotent by
// (profile_id, capture_dir). Two processes may sync the same inbox at once
// (the GUI reloads the list on every change event, and each reload is its
// own CLI process), so the existence check is repeated inside the write
// lock, the same way RegisterDiscoveredDocument does it.
func registerCaptureDocument(ctx context.Context, db *sql.DB, profileID string, c CaptureDocument, name string) (id string, created bool, err error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return "", false, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		dbconn.AbandonBegin(conn)
		return "", false, fmt.Errorf("begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	err = conn.QueryRowContext(ctx, `SELECT id FROM vault_documents WHERE profile_id = ? AND capture_dir = ?`, profileID, c.Dir).Scan(&id)
	if err == nil {
		return id, false, nil
	}
	if err != sql.ErrNoRows {
		return "", false, fmt.Errorf("checking existing: %w", err)
	}

	var seq int
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM vault_documents`).Scan(&seq); err != nil {
		return "", false, fmt.Errorf("get next seq: %w", err)
	}
	id = fmt.Sprintf("doc-%03d", seq)
	source := strings.TrimSpace(c.Source)
	if source == "" {
		source = "extension"
	}
	createdAt := c.CapturedAt
	if createdAt == "" {
		if err := conn.QueryRowContext(ctx, `SELECT datetime('now')`).Scan(&createdAt); err != nil {
			return "", false, fmt.Errorf("get timestamp: %w", err)
		}
	}
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO vault_documents (id, seq, path, filename, size_bytes, source, profile_id, created_at, url, capture_dir)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, seq, c.Path, name, c.SizeBytes, source, profileID, createdAt, nullIfEmpty(c.URL), c.Dir,
	); err != nil {
		return "", false, fmt.Errorf("insert: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return "", false, fmt.Errorf("commit: %w", err)
	}
	committed = true
	return id, true, nil
}

// captureDisplayName is the page title, falling back to the URL and then
// the envelope directory's own name for a page that had no title.
func captureDisplayName(c CaptureDocument) string {
	for _, s := range []string{c.Title, c.URL, filepath.Base(c.Dir)} {
		if s = strings.TrimSpace(s); s != "" && s != "." && s != string(filepath.Separator) {
			return s
		}
	}
	return "Untitled capture"
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// removeCaptureDir deletes a capture's envelope directory. The directory
// came from this app's own inbox listing, but it is still a recursive
// delete driven by a DB value, so it refuses anything that does not look
// like an envelope: a relative or root path, a directory that is not
// directly inside one named "inbox", one without a meta.json, or one that
// does not contain the row's own primary artifact.
func removeCaptureDir(dir, primary string) error {
	clean := filepath.Clean(dir)
	if !filepath.IsAbs(clean) || filepath.Dir(clean) == clean {
		return fmt.Errorf("refusing to remove %q: not an absolute capture directory", dir)
	}
	if filepath.Base(filepath.Dir(clean)) != "inbox" {
		return fmt.Errorf("refusing to remove %q: not inside an inbox", dir)
	}
	if filepath.Dir(filepath.Clean(primary)) != clean {
		return fmt.Errorf("refusing to remove %q: the document's file is not inside it", dir)
	}
	if _, err := os.Stat(filepath.Join(clean, "meta.json")); err != nil {
		if os.IsNotExist(err) {
			if _, dirErr := os.Stat(clean); os.IsNotExist(dirErr) {
				return nil // already gone
			}
		}
		return fmt.Errorf("refusing to remove %q: no meta.json: %w", dir, err)
	}
	return os.RemoveAll(clean)
}
