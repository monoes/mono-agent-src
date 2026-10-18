package vault

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/profiledir"
)

type ctxKey struct{}

// ContextWithDB returns a new context carrying db for vault operations.
func ContextWithDB(ctx context.Context, db *sql.DB) context.Context {
	return context.WithValue(ctx, ctxKey{}, db)
}

// DBFromContext retrieves the *sql.DB stored by ContextWithDB. Returns nil if absent.
func DBFromContext(ctx context.Context) *sql.DB {
	db, _ := ctx.Value(ctxKey{}).(*sql.DB)
	return db
}

type execCtxKey struct{}

type execIDs struct {
	WorkflowID  string
	ExecutionID string
}

// ContextWithExecIDs stores workflow and execution IDs in context for vault registration.
func ContextWithExecIDs(ctx context.Context, workflowID, executionID string) context.Context {
	return context.WithValue(ctx, execCtxKey{}, execIDs{workflowID, executionID})
}

// ExecIDsFromContext retrieves the workflow and execution IDs stored by ContextWithExecIDs.
func ExecIDsFromContext(ctx context.Context) (workflowID, executionID string) {
	if v, ok := ctx.Value(execCtxKey{}).(execIDs); ok {
		return v.WorkflowID, v.ExecutionID
	}
	return "", ""
}

type profileCtxKey struct{}

// ContextWithProfileID stores a profile ID in context for vault registration.
func ContextWithProfileID(ctx context.Context, profileID string) context.Context {
	return context.WithValue(ctx, profileCtxKey{}, profileID)
}

// ErrNoProfile is returned when an operation needs a profile and none was
// given. There is no implicit profile: callers name one or fail.
var ErrNoProfile = profiledir.ErrNoProfile

// ProfileIDFromContext retrieves the profile ID stored by ContextWithProfileID.
// Returns ErrNoProfile when the context carries none.
func ProfileIDFromContext(ctx context.Context) (string, error) {
	if v, ok := ctx.Value(profileCtxKey{}).(string); ok && v != "" {
		return v, nil
	}
	return "", ErrNoProfile
}

// VaultDir returns the absolute path of a profile's vault directory —
// either its chosen folder or the default
// ~/.monoagent/profiles/<profileID>/vault/.
func VaultDir(db *sql.DB, profileID string) string {
	return profiledir.VaultDir(db, profileID)
}

// EnsureVaultDir creates a profile's vault directory if it does not exist.
func EnsureVaultDir(db *sql.DB, profileID string) error {
	return os.MkdirAll(VaultDir(db, profileID), 0700)
}

// Register copies the file at src into the vault, inserts a DB row, and
// returns the new vault ID (e.g. "img-001").
// source should be "gemini", "upload", "huggingface", etc.
// workflowID and executionID may be empty strings.
//
// A file that already sits in the profile's folder, where `image sync`
// would find it, is recorded in place instead (see registerInPlace): a
// copy would show up twice once the folder is scanned.
func Register(ctx context.Context, db *sql.DB, src, source, workflowID, executionID string) (string, error) {
	if db == nil {
		return "", fmt.Errorf("vault.Register: db is nil")
	}
	if src == "" {
		return "", fmt.Errorf("vault.Register: src path is empty")
	}
	absSrc, err := filepath.Abs(src)
	if err != nil {
		return "", fmt.Errorf("vault.Register: invalid src path: %w", err)
	}
	src = absSrc

	profileID, err := ProfileIDFromContext(ctx)
	if err != nil {
		return "", fmt.Errorf("vault.Register: %w", err)
	}
	vaultDir := VaultDir(db, profileID)
	if strings.HasPrefix(absSrc, vaultDir+string(os.PathSeparator)) || absSrc == vaultDir {
		return "", fmt.Errorf("vault.Register: src must not be inside the vault directory")
	}

	if inPlace, ok := profileFolderPath(db, profileID, absSrc); ok {
		return registerInPlace(ctx, db, profileID, inPlace, source, workflowID, executionID)
	}

	if err := EnsureVaultDir(db, profileID); err != nil {
		return "", fmt.Errorf("vault.Register: ensure vault dir: %w", err)
	}

	// Take a dedicated connection and open an IMMEDIATE transaction so the
	// write lock is acquired up front. database/sql's default BeginTx starts a
	// DEFERRED (reader) transaction under SQLite, which lets two concurrent
	// Registers both read the same MAX(seq) and race on the same destPath; the
	// loser's cleanup would then delete the winner's committed vault file.
	// BEGIN IMMEDIATE serializes seq allocation across connections/processes
	// (waiting up to busy_timeout for a concurrent writer).
	conn, err := db.Conn(ctx)
	if err != nil {
		return "", fmt.Errorf("vault.Register: get conn: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return "", fmt.Errorf("vault.Register: begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			// Use a fresh context so rollback still runs if ctx was cancelled.
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	var seq int
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM vault_images`).Scan(&seq); err != nil {
		return "", fmt.Errorf("vault.Register: get next seq: %w", err)
	}

	id := fmt.Sprintf("img-%03d", seq)
	ext := filepath.Ext(src)
	if ext == "" {
		ext = ".png"
	}
	destFilename := id + ext
	destPath := filepath.Join(vaultDir, destFilename)

	// Copy source file to vault.
	if err := copyFile(src, destPath); err != nil {
		_ = os.Remove(destPath) // best-effort cleanup of partial file
		return "", fmt.Errorf("vault.Register: copy file: %w", err)
	}

	// Get file size.
	fi, err := os.Stat(destPath)
	if err != nil {
		_ = os.Remove(destPath) // best-effort cleanup
		return "", fmt.Errorf("vault.Register: stat dest: %w", err)
	}

	// Nullable string helpers.
	nullStr := func(s string) interface{} {
		if s == "" {
			return nil
		}
		return s
	}

	_, err = conn.ExecContext(ctx, `
		INSERT INTO vault_images (id, seq, path, filename, size_bytes, source, workflow_id, execution_id, profile_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, seq, destPath, destFilename, fi.Size(), source,
		nullStr(workflowID), nullStr(executionID), profileID,
	)
	if err != nil {
		os.Remove(destPath) // best-effort cleanup
		return "", fmt.Errorf("vault.Register: insert: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		os.Remove(destPath)
		return "", fmt.Errorf("vault.Register: commit: %w", err)
	}
	committed = true

	syncImageToKG(db, profileID, id, source, workflowID, executionID)
	return id, nil
}

// syncImageToKG indexes a newly registered image in the knowledge graph in
// the background (see Wait).
func syncImageToKG(db *sql.DB, profileID, id, source, workflowID, executionID string) {
	background.Add(1)
	go func() {
		defer background.Done()
		desc := "source: " + source
		if workflowID != "" {
			desc += ", workflow: " + workflowID
		}
		if executionID != "" {
			desc += ", execution: " + executionID
		}
		node := monomind.KGNode{Name: id, Type: "file", Description: desc}
		_ = monomind.SyncToKnowledgeGraph(context.Background(), db, profileID, []monomind.KGNode{node}, nil, "vault:"+id)
	}()
}

// legacyVaultDir is the single flat directory every profile's files used to
// share before per-profile folders existed. Kept only for MigrateVaultFiles.
func legacyVaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, ".monoagent", "vault")
}

// MigrateVaultFiles moves one profile's files out of the old shared flat
// vault directory into its own vault/ folder, updating each vault_images
// row's stored path to match. Idempotent: rows already pointing outside the
// legacy directory (i.e. already migrated, or never lived there) are left
// untouched. Thin wrapper over MoveFiles — kept as its own name since this
// specific "off the old shared dir" case runs unconditionally on every
// startup, same as this file's sibling migrations.
func MigrateVaultFiles(ctx context.Context, db *sql.DB, profileID string) (moved int, errs []error) {
	return MoveFiles(ctx, db, profileID, legacyVaultDir(), VaultDir(db, profileID))
}

// MigrateVaultDirIntoMonoagent moves one profile's files out of the old
// per-profile <root>/vault/ folder (visible, unnamespaced, and mixed in
// with the user's own files whenever root_dir points at a real coding
// project) into <root>/.monoagent/vault/ — VaultDir's current shape —
// updating each vault_images/vault_documents row's stored path to match.
// Both tables are migrated: a profile with uploaded documents has files
// under the old vault/documents/ too, and leaving those behind would both
// keep the old vault/ directory around forever (never empty enough to
// remove) and permanently split documents across the old and new
// locations. Idempotent, via the same MoveFiles/MoveDocumentFiles used
// elsewhere: rows no longer under the old dir (already migrated, or a
// brand new profile that never had one) are left untouched — this
// includes a "discovered" document, whose path was never inside the vault
// to begin with (see MoveDocumentFiles). Once the old directory (and its
// documents/ subdirectory) is empty, both are removed; os.Remove only
// succeeds on an empty directory, so any file neither move knew about
// (never registered in either table) is left in place rather than
// silently discarded.
func MigrateVaultDirIntoMonoagent(ctx context.Context, db *sql.DB, profileID string) (moved int, errs []error) {
	root := profiledir.Root(db, profileID)
	if root == "" {
		return 0, nil
	}
	oldDir := filepath.Join(root, "vault")
	newVaultDir := VaultDir(db, profileID)

	imgMoved, imgErrs := MoveFiles(ctx, db, profileID, oldDir, newVaultDir)
	docMoved, docErrs := MoveDocumentFiles(ctx, db, profileID, oldDir, filepath.Join(newVaultDir, "documents"))
	moved = imgMoved + docMoved
	errs = append(imgErrs, docErrs...)

	_ = os.Remove(filepath.Join(oldDir, "documents"))
	_ = os.Remove(oldDir)
	return moved, errs
}

// moveTrackedFiles is the shared engine behind MoveFiles and
// MoveDocumentFiles: given a table with (id, path, profile_id) columns, it
// moves every profileID-scoped row whose path still lives under fromDir
// into toDir, renaming the file on disk and updating its stored path to
// match. Rows whose path lies elsewhere (already moved, or never lived in
// fromDir) are left completely untouched — the fromDir prefix check is the
// actual safety boundary, not any assumption about what a row's source is.
// table is always a caller-supplied literal ("vault_images" or
// "vault_documents"), never external input, so building the query around
// it carries no injection risk.
//
// The destination filename is always derived from the CURRENT path's own
// basename, never from a separate `filename` column: vault_images keeps
// those in sync (Register stores the same id-based name in both), but
// vault_documents does not — RegisterDocument stores the user's ORIGINAL
// uploaded name in `filename` while `path` uses a unique id-based name.
// Two uploads named identically by the user then share a `filename` but
// have distinct paths; building destPath from `filename` would collide
// them onto the same destination and os.Rename would silently clobber one
// with the other. Basenames of the current path are always already unique
// on disk, so this can't happen.
func moveTrackedFiles(ctx context.Context, db *sql.DB, table, profileID, fromDir, toDir string) (moved int, errs []error) {
	if err := os.MkdirAll(toDir, 0700); err != nil {
		return 0, []error{fmt.Errorf("vault.moveTrackedFiles(%s): ensure dest dir: %w", table, err)}
	}

	rows, err := db.QueryContext(ctx,
		fmt.Sprintf(`SELECT id, path FROM %s WHERE profile_id = ?`, table), profileID)
	if err != nil {
		return 0, []error{fmt.Errorf("vault.moveTrackedFiles(%s): query rows: %w", table, err)}
	}
	type row struct{ id, path string }
	var toMove []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.path); err == nil {
			toMove = append(toMove, r)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		errs = append(errs, fmt.Errorf("vault.moveTrackedFiles(%s): iterate rows: %w", table, err))
	}

	updateSQL := fmt.Sprintf(`UPDATE %s SET path = ? WHERE id = ?`, table)
	for _, r := range toMove {
		if !strings.HasPrefix(r.path, fromDir+string(os.PathSeparator)) {
			continue // already moved, or never lived in fromDir
		}
		destPath := filepath.Join(toDir, filepath.Base(r.path))
		if err := os.Rename(r.path, destPath); err != nil {
			errs = append(errs, fmt.Errorf("vault.moveTrackedFiles(%s): move %s: %w", table, r.id, err))
			continue
		}
		if _, err := db.ExecContext(ctx, updateSQL, destPath, r.id); err != nil {
			errs = append(errs, fmt.Errorf("vault.moveTrackedFiles(%s): update path for %s: %w", table, r.id, err))
			continue
		}
		moved++
	}
	return moved, errs
}

// MoveFiles moves a profile's vault_images files from fromDir to toDir,
// updating each row's stored path to match. Idempotent: rows whose path
// isn't inside fromDir (already moved, or never lived there) are left
// untouched — so re-running after a partial failure only retries what
// didn't move yet. A failure on one row is reported via the returned
// per-row error list rather than aborting the rest.
func MoveFiles(ctx context.Context, db *sql.DB, profileID, fromDir, toDir string) (moved int, errs []error) {
	return moveTrackedFiles(ctx, db, "vault_images", profileID, fromDir, toDir)
}

// MoveDocumentFiles is MoveFiles' counterpart for vault_documents. It has
// to exist separately (rather than everyone just calling MoveFiles twice)
// because vault_documents mixes two very differently-owned kinds of rows:
// files RegisterDocument copied into the vault (source "upload" etc.),
// which belong under fromDir/toDir exactly like images, and files
// RegisterDiscoveredDocument recorded in place (source "discovered")
// that live wherever the user's own project put them and were never
// copied anywhere. moveTrackedFiles's fromDir prefix check is what keeps
// this safe regardless: a discovered document's path is essentially never
// under the vault directory, so it is simply skipped — never moved,
// never rewritten.
func MoveDocumentFiles(ctx context.Context, db *sql.DB, profileID, fromDir, toDir string) (moved int, errs []error) {
	return moveTrackedFiles(ctx, db, "vault_documents", profileID, fromDir, toDir)
}

// Resolve turns "@img-001" into the absolute file path stored in the DB.
// Returns an error if the image is not found.
func Resolve(ctx context.Context, db *sql.DB, ref string) (string, error) {
	if !strings.HasPrefix(ref, "@") {
		return ref, nil
	}
	id := strings.TrimPrefix(ref, "@")
	profileID, err := ProfileIDFromContext(ctx)
	if err != nil {
		return ref, fmt.Errorf("vault.Resolve: %w", err)
	}
	var path string
	err = db.QueryRowContext(ctx,
		`SELECT path FROM vault_images WHERE id = ? AND profile_id = ?`,
		id, profileID).Scan(&path)
	if err == sql.ErrNoRows {
		return ref, fmt.Errorf("vault.Resolve: image %q not found", id)
	}
	if err != nil {
		return ref, fmt.Errorf("vault.Resolve: %w", err)
	}
	return path, nil
}

// ErrDataReference is wrapped by the refusal of an @img reference that a
// step's config holds only once its expressions were evaluated.
var ErrDataReference = errors.New("an @img reference that came from the run's data or an expression is not read from the vault")

// ResolveConfig walks a config map and replaces any string value that starts
// with "@img-" with its absolute file path from the vault.
// Keys with missing images are left as-is (the ref string) and a warning is logged to stderr.
// stored is the same config as the workflow holds it, before its expressions
// were evaluated into config: a reference resolves only where stored holds it
// as it is, and one an expression produced (the run's data, or text the
// expression built) is refused, so the step doesn't run.
func ResolveConfig(ctx context.Context, db *sql.DB, stored, config map[string]interface{}) error {
	if db == nil {
		return nil
	}
	for k, v := range config {
		s, ok := v.(string)
		if !ok {
			continue
		}
		if !strings.HasPrefix(s, "@img-") {
			continue
		}
		if lit, ok := stored[k].(string); !ok || lit != s || strings.Contains(lit, "{{") {
			return fmt.Errorf("%q: %w: only one written as it is in the workflow resolves", k, ErrDataReference)
		}
		resolved, err := Resolve(ctx, db, s)
		if errors.Is(err, ErrNoProfile) {
			return err
		}
		if err != nil {
			// Non-fatal: leave original ref, emit warning.
			fmt.Fprintf(os.Stderr, "vault: warning: %v\n", err)
			continue
		}
		config[k] = resolved
	}
	return nil
}

func copyFile(src, dst string) (retErr error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() {
		cerr := out.Close()
		if retErr == nil {
			retErr = cerr
		}
	}()
	_, err = io.Copy(out, in)
	return err
}

// background tracks Register's knowledge-graph syncs, which run after it
// returns and write under the profile's folder.
var background sync.WaitGroup

// Wait blocks until every knowledge-graph sync Register started has
// finished. Tests call it before their temporary HOME is removed, since a
// sync still writing there makes the cleanup fail; a process that is about
// to exit can call it to let the syncs land.
func Wait() { background.Wait() }
