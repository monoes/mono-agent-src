// internal/vault/documents.go
package vault

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
)

// DocumentEntry is one row from vault_documents.
type DocumentEntry struct {
	ID            string
	Path          string
	Filename      string
	SizeBytes     int64
	Source        string
	ApplicationID string
	CreatedAt     string
	Indexed       bool
	IndexError    string
	// URL is the page a browser capture was taken from; empty for every
	// other row. CaptureDir is the capture's envelope directory, non-empty
	// exactly for capture-backed rows (see ReconcileCaptureDocuments).
	URL        string
	CaptureDir string
	Stale      bool // computed at read time, not a DB column -- see computeStale
}

// documentColumns is the column list ListDocuments and GetDocument select,
// in the order scanDocument reads them.
const documentColumns = `id, path, filename, size_bytes, source, COALESCE(application_id, ''), created_at, indexed, COALESCE(index_error, ''), indexed_mtime, indexed_size_bytes, COALESCE(url, ''), COALESCE(capture_dir, '')`

// scanDocument reads one documentColumns row and computes its Stale flag.
func scanDocument(scan func(dest ...any) error) (DocumentEntry, error) {
	var d DocumentEntry
	var indexedMTime, indexedSizeBytes sql.NullInt64
	if err := scan(&d.ID, &d.Path, &d.Filename, &d.SizeBytes, &d.Source, &d.ApplicationID, &d.CreatedAt, &d.Indexed, &d.IndexError, &indexedMTime, &indexedSizeBytes, &d.URL, &d.CaptureDir); err != nil {
		return d, err
	}
	d.Stale = computeStale(d.Indexed, indexedMTime, indexedSizeBytes, d.Path)
	return d, nil
}

// computeStale reports whether an indexed document's content has changed
// on disk since its last successful index. A NULL baseline (either
// indexedMTime or indexedSizeBytes not .Valid -- a row that predates the
// indexed_mtime/indexed_size_bytes columns, or one that has never been
// successfully indexed) always reads as "not stale": there is no baseline
// to compare against, and treating a NULL as zero would mark every
// pre-existing indexed document Stale the moment this column is added.
// Likewise, a path that can no longer be stat'd (e.g. a discovered
// document whose source file has since vanished) reads as not stale --
// there's nothing to confirm a mismatch against.
func computeStale(indexed bool, indexedMTime, indexedSizeBytes sql.NullInt64, path string) bool {
	if !indexed || !indexedMTime.Valid || !indexedSizeBytes.Valid {
		return false
	}
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	return fi.ModTime().UnixNano() != indexedMTime.Int64 || fi.Size() != indexedSizeBytes.Int64
}

// RegisterDocument copies src into the profile's vault (under a
// documents/ subdirectory of the same VaultDir used for images) and
// inserts a vault_documents row. Returns the new vault ID (e.g. "doc-001").
// applicationID is optional (variadic, matching internal/connections.Store
// .Get's ...string convention) — pass one string to link the document to
// a job/tender application, or omit it for a general profile document.
// Mirrors Register's structure exactly (same BEGIN IMMEDIATE seq-allocation
// pattern — see Register's doc comment in vault.go for why a deferred
// transaction would race two concurrent Registers onto the same seq).
func RegisterDocument(ctx context.Context, db *sql.DB, src, source string, applicationID ...string) (string, error) {
	if db == nil {
		return "", fmt.Errorf("vault.RegisterDocument: db is nil")
	}
	if src == "" {
		return "", fmt.Errorf("vault.RegisterDocument: src path is empty")
	}
	var appID string
	if len(applicationID) > 0 {
		appID = applicationID[0]
	}
	absSrc, err := filepath.Abs(src)
	if err != nil {
		return "", fmt.Errorf("vault.RegisterDocument: invalid src path: %w", err)
	}

	profileID, err := ProfileIDFromContext(ctx)
	if err != nil {
		return "", fmt.Errorf("vault.RegisterDocument: %w", err)
	}
	docsDir := filepath.Join(VaultDir(db, profileID), "documents")
	if err := os.MkdirAll(docsDir, 0700); err != nil {
		return "", fmt.Errorf("vault.RegisterDocument: ensure documents dir: %w", err)
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return "", fmt.Errorf("vault.RegisterDocument: get conn: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return "", fmt.Errorf("vault.RegisterDocument: begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	var seq int
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM vault_documents`).Scan(&seq); err != nil {
		return "", fmt.Errorf("vault.RegisterDocument: get next seq: %w", err)
	}

	id := fmt.Sprintf("doc-%03d", seq)
	filename := filepath.Base(absSrc)
	destPath := filepath.Join(docsDir, fmt.Sprintf("%s%s", id, filepath.Ext(filename)))

	if err := copyFileOpt(absSrc, destPath, ctx.Value(noFollowKey{}) != nil); err != nil {
		_ = os.Remove(destPath)
		return "", fmt.Errorf("vault.RegisterDocument: copy file: %w", err)
	}
	fi, err := os.Stat(destPath)
	if err != nil {
		_ = os.Remove(destPath)
		return "", fmt.Errorf("vault.RegisterDocument: stat dest: %w", err)
	}

	nullStr := func(s string) interface{} {
		if s == "" {
			return nil
		}
		return s
	}

	_, err = conn.ExecContext(ctx, `
		INSERT INTO vault_documents (id, seq, path, filename, size_bytes, source, application_id, profile_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))`,
		id, seq, destPath, filename, fi.Size(), source, nullStr(appID), profileID,
	)
	if err != nil {
		os.Remove(destPath)
		return "", fmt.Errorf("vault.RegisterDocument: insert: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		os.Remove(destPath)
		return "", fmt.Errorf("vault.RegisterDocument: commit: %w", err)
	}
	committed = true
	return id, nil
}

// ListDocuments returns profileID's uploaded documents, newest first.
func ListDocuments(ctx context.Context, db *sql.DB, profileID string) ([]DocumentEntry, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT `+documentColumns+` FROM vault_documents WHERE profile_id = ? ORDER BY seq DESC`, profileID)
	if err != nil {
		return nil, fmt.Errorf("vault.ListDocuments: %w", err)
	}
	defer rows.Close()
	docs := []DocumentEntry{}
	for rows.Next() {
		d, err := scanDocument(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("vault.ListDocuments: scan: %w", err)
		}
		docs = append(docs, d)
	}
	return docs, rows.Err()
}

// ListDocumentsPage is ListDocuments for at most limit documents after the first
// offset, newest first: the rows read and the files stat'ed (for Stale) are bounded
// by limit, not by how many documents the profile has. A limit below 1 lists none.
func ListDocumentsPage(ctx context.Context, db *sql.DB, profileID string, limit, offset int) ([]DocumentEntry, error) {
	docs := []DocumentEntry{}
	if limit < 1 {
		return docs, nil
	}
	rows, err := db.QueryContext(ctx,
		`SELECT `+documentColumns+` FROM vault_documents WHERE profile_id = ? ORDER BY seq DESC LIMIT ? OFFSET ?`,
		profileID, limit, max(offset, 0))
	if err != nil {
		return nil, fmt.Errorf("vault.ListDocumentsPage: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		d, err := scanDocument(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("vault.ListDocumentsPage: scan: %w", err)
		}
		docs = append(docs, d)
	}
	return docs, rows.Err()
}

// GetDocument returns id's document entry, scoped to profileID. Returns a
// nil entry (not an error) when no such row exists for this profile --
// deleted, never existed, and belonging to a different profile all look
// identical to a caller, and "not found" is an expected outcome here (a
// stale reference resolving to nothing), not a failure worth an error
// value. Mirrors ListDocuments' column set and stale computation, scoped
// by (id, profile_id) the same way DeleteDocument/SetDocumentIndexed are.
func GetDocument(ctx context.Context, db *sql.DB, profileID, id string) (*DocumentEntry, error) {
	d, err := scanDocument(db.QueryRowContext(ctx,
		`SELECT `+documentColumns+` FROM vault_documents WHERE id = ? AND profile_id = ?`, id, profileID,
	).Scan)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("vault.GetDocument: %w", err)
	}
	return &d, nil
}

// SetDocumentIndexed records the outcome of a knowledge_ingest attempt for
// id, scoped to profileID. indexErr should be empty on success.
//
// Asymmetric by design: on success (indexed=true), index_error is cleared
// and indexed_mtime/indexed_size_bytes are stamped with the file's (mtime,
// size) at the moment of this successful index -- the new staleness
// baseline. On failure (indexed=false), ONLY index_error is written; the
// existing indexed flag and staleness baseline are left untouched, and
// indexedMTime/indexedSizeBytes are ignored. This matters once a document
// can be re-indexed on demand: a failed re-index of an
// already-successfully-indexed document must not un-index it or erase the
// baseline that makes it show "Stale" rather than "Not indexed" -- the OLD
// content is still live in monomind's knowledge base until a later attempt
// actually succeeds.
func SetDocumentIndexed(ctx context.Context, db *sql.DB, profileID, id string, indexed bool, indexErr string, indexedMTime, indexedSizeBytes int64) error {
	var errVal interface{}
	if indexErr != "" {
		errVal = indexErr
	}

	var res sql.Result
	var err error
	if indexed {
		res, err = db.ExecContext(ctx,
			`UPDATE vault_documents SET indexed = ?, index_error = ?, indexed_mtime = ?, indexed_size_bytes = ? WHERE id = ? AND profile_id = ?`,
			indexed, errVal, indexedMTime, indexedSizeBytes, id, profileID,
		)
	} else {
		res, err = db.ExecContext(ctx,
			`UPDATE vault_documents SET index_error = ? WHERE id = ? AND profile_id = ?`,
			errVal, id, profileID,
		)
	}
	if err != nil {
		return fmt.Errorf("vault.SetDocumentIndexed: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("vault.SetDocumentIndexed: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("vault.SetDocumentIndexed: id %q not found", id)
	}
	return nil
}

// DeleteDocument removes id's vault_documents row, scoped to profileID.
// Returns an error if the row does not exist. A browser capture's row
// (CaptureDir set) takes its whole envelope directory with it. Otherwise the underlying file is only
// removed when the document's source is not "discovered": a discovered
// document's path is the user's ORIGINAL file (never copied into the
// vault — see RegisterDiscoveredDocument), so deleting the row must never
// destroy a file this app never took ownership of. Uploaded/generated
// documents' files, by contrast, are vault copies this app itself created
// and owns, and are removed as before.
func DeleteDocument(ctx context.Context, db *sql.DB, profileID, id string) error {
	var path, source, captureDir string
	err := db.QueryRowContext(ctx,
		`SELECT path, source, COALESCE(capture_dir, '') FROM vault_documents WHERE id = ? AND profile_id = ?`, id, profileID,
	).Scan(&path, &source, &captureDir)
	if err == sql.ErrNoRows {
		return fmt.Errorf("vault.DeleteDocument: id %q not found", id)
	}
	if err != nil {
		return fmt.Errorf("vault.DeleteDocument: %w", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM vault_documents WHERE id = ? AND profile_id = ?`, id, profileID); err != nil {
		return fmt.Errorf("vault.DeleteDocument: %w", err)
	}
	if captureDir != "" {
		// A capture is one envelope, not one file: removing only the
		// primary artifact would let the next inbox sync re-register the
		// capture under its next-best artifact. See removeCaptureDir.
		if err := removeCaptureDir(captureDir, path); err != nil {
			fmt.Fprintf(os.Stderr, "warning: document %s deleted from index but its capture could not be removed: %v\n", id, err)
		}
		return nil
	}
	if source != "discovered" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "warning: document %s deleted from index but its file could not be removed: %v\n", id, err)
		}
	}
	return nil
}

// RegisterDiscoveredDocument inserts a vault_documents row for a
// human-readable document found under the profile's folder that was never
// uploaded or generated through this app. Unlike RegisterDocument, this
// does NOT copy path into the vault -- it is recorded as-is: copying a
// file that may be far from the vault on every scan tick would be
// surprising and wasteful, and the original file is the source of truth
// for a discovered document.
//
// Idempotent by (profile_id, path): if a row already exists for this
// path, its id is returned with created=false instead of inserting a
// duplicate -- e.g. a scan that also walks vault/documents/ must not
// double-register an uploaded file's own vault copy.
func RegisterDiscoveredDocument(ctx context.Context, db *sql.DB, profileID, path, filename string, sizeBytes int64) (id string, created bool, err error) {
	if existingID, ok, lookupErr := documentIDByPath(ctx, db, profileID, path); lookupErr != nil {
		return "", false, fmt.Errorf("vault.RegisterDiscoveredDocument: %w", lookupErr)
	} else if ok {
		return existingID, false, nil
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return "", false, fmt.Errorf("vault.RegisterDiscoveredDocument: get conn: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return "", false, fmt.Errorf("vault.RegisterDiscoveredDocument: begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	// Another goroutine may have registered this exact path while we
	// waited for the write lock -- re-check inside the transaction before
	// inserting, since the pre-check above ran without one.
	var existingID string
	err = conn.QueryRowContext(ctx, `SELECT id FROM vault_documents WHERE profile_id = ? AND path = ?`, profileID, path).Scan(&existingID)
	if err == nil {
		if _, rbErr := conn.ExecContext(ctx, "ROLLBACK"); rbErr == nil {
			committed = true
		}
		return existingID, false, nil
	}
	if err != sql.ErrNoRows {
		return "", false, fmt.Errorf("vault.RegisterDiscoveredDocument: checking existing: %w", err)
	}

	var seq int
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM vault_documents`).Scan(&seq); err != nil {
		return "", false, fmt.Errorf("vault.RegisterDiscoveredDocument: get next seq: %w", err)
	}
	id = fmt.Sprintf("doc-%03d", seq)

	_, err = conn.ExecContext(ctx, `
		INSERT INTO vault_documents (id, seq, path, filename, size_bytes, source, profile_id, created_at)
		VALUES (?, ?, ?, ?, ?, 'discovered', ?, datetime('now'))`,
		id, seq, path, filename, sizeBytes, profileID,
	)
	if err != nil {
		return "", false, fmt.Errorf("vault.RegisterDiscoveredDocument: insert: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return "", false, fmt.Errorf("vault.RegisterDiscoveredDocument: commit: %w", err)
	}
	committed = true
	return id, true, nil
}

func documentIDByPath(ctx context.Context, db *sql.DB, profileID, path string) (id string, ok bool, err error) {
	err = db.QueryRowContext(ctx, `SELECT id FROM vault_documents WHERE profile_id = ? AND path = ?`, profileID, path).Scan(&id)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return id, true, nil
}

// DiscoveredFile is one file a filesystem scan (internal/docscan) found, as
// handed to ReconcileDiscoveredDocuments. Deliberately not
// docscan.FileInfo: vault (DB-owning) must not import docscan (pure
// filesystem), so the caller does a trivial field-for-field map between
// the two, keeping both packages mutually independent.
type DiscoveredFile struct {
	Path      string
	Filename  string
	SizeBytes int64
}

// ReconcileDiscoveredDocuments registers a row for every file in found not
// already tracked under profileID by path, refreshes size_bytes for ones
// that already are, and removes any "discovered"-sourced row whose path no
// longer appears in found -- a file that was deleted, moved, or is now
// excluded by docscan.Scan's own rules (e.g. a dot-folder docscan.isDotDir
// excludes). found must be a full current snapshot (matching
// docscan.Watcher's own contract), never a partial delta -- a delta would
// make every path outside it look "vanished" and wrongly purge it.
//
// Only rows with source="discovered" are ever candidates for removal: a
// manually uploaded/registered document (any other source, e.g. "upload")
// has a different lifecycle -- see DeleteDocument's own source check -- and
// simply not appearing in a docscan snapshot (it may not even live under
// the scanned tree) is never grounds to delete it here.
//
// Continues past per-file errors, collecting them, so one bad file never
// blocks the rest of a scan from being reconciled.
func ReconcileDiscoveredDocuments(ctx context.Context, db *sql.DB, profileID string, found []DiscoveredFile) (added int, removed int, errs []error) {
	r := SyncDiscoveredDocuments(ctx, db, profileID, found)
	return r.Added, r.Removed, r.Errs
}

// ReconcileResult counts what one reconcile pass changed. Updated is a
// tracked file whose size_bytes was refreshed.
type ReconcileResult struct {
	Added, Updated, Removed int
	Errs                    []error
}

// Changed reports whether the pass wrote anything.
func (r ReconcileResult) Changed() bool { return r.Added+r.Updated+r.Removed > 0 }

// SyncDiscoveredDocuments is ReconcileDiscoveredDocuments with the
// size refreshes counted too -- what `profile documents sync` reports.
func SyncDiscoveredDocuments(ctx context.Context, db *sql.DB, profileID string, found []DiscoveredFile) (res ReconcileResult) {
	existing := make(map[string]int64, len(found))
	rows, err := db.QueryContext(ctx, `SELECT path, size_bytes FROM vault_documents WHERE profile_id = ? AND source = 'discovered'`, profileID)
	if err != nil {
		res.Errs = []error{fmt.Errorf("vault.ReconcileDiscoveredDocuments: loading existing: %w", err)}
		return res
	}
	for rows.Next() {
		var path string
		var size int64
		if scanErr := rows.Scan(&path, &size); scanErr != nil {
			rows.Close()
			res.Errs = []error{fmt.Errorf("vault.ReconcileDiscoveredDocuments: scanning existing: %w", scanErr)}
			return res
		}
		existing[path] = size
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		res.Errs = []error{fmt.Errorf("vault.ReconcileDiscoveredDocuments: %w", err)}
		return res
	}

	excluded, err := excludedPaths(ctx, db, profileID)
	if err != nil {
		res.Errs = []error{fmt.Errorf("vault.ReconcileDiscoveredDocuments: %w", err)}
		return res
	}

	foundPaths := make(map[string]bool, len(found))
	for _, f := range found {
		foundPaths[f.Path] = true
		size, tracked := existing[f.Path]
		if !tracked {
			if excluded[f.Path] {
				continue // the user removed it from MonoAgent
			}
			// RegisterDiscoveredDocument's own lookup matches by path
			// regardless of source, so a path already tracked under a
			// different source (e.g. "upload") correctly reports
			// created=false here rather than inserting a duplicate.
			_, created, regErr := RegisterDiscoveredDocument(ctx, db, profileID, f.Path, f.Filename, f.SizeBytes)
			if regErr != nil {
				res.Errs = append(res.Errs, fmt.Errorf("registering %s: %w", f.Path, regErr))
				continue
			}
			if created {
				res.Added++
			}
			continue
		}
		if size != f.SizeBytes {
			if _, updErr := db.ExecContext(ctx, `UPDATE vault_documents SET size_bytes = ? WHERE profile_id = ? AND path = ?`, f.SizeBytes, profileID, f.Path); updErr != nil {
				res.Errs = append(res.Errs, fmt.Errorf("refreshing size for %s: %w", f.Path, updErr))
				continue
			}
			res.Updated++
		}
	}

	for path := range existing {
		if foundPaths[path] {
			continue
		}
		if _, delErr := db.ExecContext(ctx, `DELETE FROM vault_documents WHERE profile_id = ? AND path = ? AND source = 'discovered'`, profileID, path); delErr != nil {
			res.Errs = append(res.Errs, fmt.Errorf("removing vanished %s: %w", path, delErr))
			continue
		}
		res.Removed++
	}
	return res
}
