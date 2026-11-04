package vault

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/monoes/mono-agent/internal/dbconn"
)

// SaveInput is one file SaveBytes stores, already checked by its caller
// (the extension bridge's vault.save, internal/extension/vault_save.go).
type SaveInput struct {
	Data []byte
	// Ext is the file's extension with its dot, chosen by the caller from the
	// checked media type, never from a name the sender chose.
	Ext string
	// Label is the name the item shows; Source says where it came from.
	Label  string
	Source string
	// ClientID makes the save idempotent within the profile; SourceURL is the
	// address it was saved from ("" for none).
	ClientID  string
	SourceURL string
	// MaxItems and MaxBytes cap what the profile's vault holds from saves
	// made this way (rows with a client id), this one included; 0 = no cap.
	MaxItems int
	MaxBytes int64
}

// ErrStorageConflict is a save that found files nobody's row owns where the
// next items would go, maxOrphanSkips times over: the vault folder needs a
// look before saving can go on.
var ErrStorageConflict = errors.New("vault.SaveBytes: files that are no vault item's sit where the next items would go")

// ErrQuotaExceeded is wrapped by QuotaError.
var ErrQuotaExceeded = errors.New("vault.SaveBytes: quota exceeded")

// QuotaError is a save refused for a SaveInput cap: Limit is "items" or
// "bytes", Max the cap.
type QuotaError struct {
	Limit string
	Max   int64
}

func (e *QuotaError) Error() string {
	return fmt.Sprintf("vault.SaveBytes: the profile's saved files would pass the %s cap of %d", e.Limit, e.Max)
}
func (e *QuotaError) Unwrap() error { return ErrQuotaExceeded }

// ErrNoClientID refuses a SaveBytes without a client id: without one a retry
// could store the same file twice.
var ErrNoClientID = errors.New("vault.SaveBytes: client id is required")

// SavedItem is the item a SaveBytes made or found, as the vault holds it.
type SavedItem struct {
	ID      string
	Created bool
	// Filename is the stored file's name (img-NNN<ext>); Label the name it
	// shows; SizeBytes the stored size.
	Filename  string
	Label     string
	SizeBytes int64
}

// maxOrphanSkips bounds how many seq numbers SaveBytes passes over because a
// file (or a symlink) of that name is already in the vault folder.
const maxOrphanSkips = 5

// SaveBytes stores in.Data in the profile's vault as a new item (vault_images,
// the vault `image list`, vault_item_list and vault_item_get_path read), at
// most once per client id: a client id this profile's vault already has gets
// that item back, as stored, with Created false, and nothing is written. The
// file is named after the item's id (img-NNN<ext>), so no name the sender
// chose ever reaches the filesystem. The check, the file and the row are one
// IMMEDIATE transaction, as in Register, so two saves of one client id at
// once store one item.
//
// A file already at img-NNN<ext> (one a crash left between its write and the
// commit, a symlink, anything else put there) is never overwritten or
// followed: the save takes the next seq instead, up to maxOrphanSkips times,
// then fails with ErrStorageConflict. The caps (MaxItems, MaxBytes) are
// checked inside the same transaction, so saves at once cannot pass them
// together; a client id already saved is answered before they are. Unlike
// Register it does not index the item in the knowledge graph: that shells out
// to monomind, which a bridge request must not wait on.
func SaveBytes(ctx context.Context, db *sql.DB, profileID string, in SaveInput) (SavedItem, error) {
	if db == nil {
		return SavedItem{}, fmt.Errorf("vault.SaveBytes: db is nil")
	}
	if strings.TrimSpace(profileID) == "" {
		return SavedItem{}, fmt.Errorf("vault.SaveBytes: %w", ErrNoProfile)
	}
	if in.ClientID == "" {
		return SavedItem{}, ErrNoClientID
	}
	if !strings.HasPrefix(in.Ext, ".") || strings.ContainsAny(in.Ext, `/\`) || strings.Contains(in.Ext, "..") {
		return SavedItem{}, fmt.Errorf("vault.SaveBytes: bad extension %q", in.Ext)
	}
	vaultDir := VaultDir(db, profileID)
	if !filepath.IsAbs(vaultDir) {
		return SavedItem{}, fmt.Errorf("vault.SaveBytes: no vault folder for this profile")
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return SavedItem{}, fmt.Errorf("vault.SaveBytes: get conn: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		dbconn.AbandonBegin(conn)
		return SavedItem{}, fmt.Errorf("vault.SaveBytes: begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	var found SavedItem
	err = conn.QueryRowContext(ctx, `SELECT id, filename, COALESCE(label, ''), size_bytes FROM vault_images WHERE profile_id = ? AND client_id = ?`,
		profileID, in.ClientID).Scan(&found.ID, &found.Filename, &found.Label, &found.SizeBytes)
	switch {
	case err == nil:
		return found, nil
	case !errors.Is(err, sql.ErrNoRows):
		return SavedItem{}, fmt.Errorf("vault.SaveBytes: look up client id: %w", err)
	}

	if in.MaxItems > 0 || in.MaxBytes > 0 {
		var items int
		var bytes int64
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(size_bytes), 0) FROM vault_images WHERE profile_id = ? AND client_id IS NOT NULL`,
			profileID).Scan(&items, &bytes); err != nil {
			return SavedItem{}, fmt.Errorf("vault.SaveBytes: read quota: %w", err)
		}
		if in.MaxItems > 0 && items+1 > in.MaxItems {
			return SavedItem{}, &QuotaError{Limit: "items", Max: int64(in.MaxItems)}
		}
		if in.MaxBytes > 0 && bytes+int64(len(in.Data)) > in.MaxBytes {
			return SavedItem{}, &QuotaError{Limit: "bytes", Max: in.MaxBytes}
		}
	}

	var seq int
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM vault_images`).Scan(&seq); err != nil {
		return SavedItem{}, fmt.Errorf("vault.SaveBytes: get next seq: %w", err)
	}
	if err := EnsureVaultDir(db, profileID); err != nil {
		return SavedItem{}, fmt.Errorf("vault.SaveBytes: ensure vault dir: %w", err)
	}
	var id, destFilename, destPath string
	for skips := 0; ; skips++ {
		id = fmt.Sprintf("img-%03d", seq)
		destFilename = id + in.Ext
		destPath = filepath.Join(vaultDir, destFilename)
		err := writeNew(destPath, in.Data)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrExist) {
			return SavedItem{}, fmt.Errorf("vault.SaveBytes: write file: %w", err)
		}
		if skips == maxOrphanSkips {
			return SavedItem{}, fmt.Errorf("%w (%s and the %d before it)", ErrStorageConflict, destFilename, maxOrphanSkips)
		}
		seq++ // a file no row of ours made sits there: leave it, take the next
	}
	nullStr := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	_, err = conn.ExecContext(ctx, `
		INSERT INTO vault_images (id, seq, path, filename, size_bytes, source, label, profile_id, client_id, source_url)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, seq, destPath, destFilename, len(in.Data), in.Source, nullStr(in.Label), profileID, in.ClientID, nullStr(in.SourceURL))
	if err != nil {
		_ = os.Remove(destPath)
		return SavedItem{}, fmt.Errorf("vault.SaveBytes: insert: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		_ = os.Remove(destPath)
		return SavedItem{}, fmt.Errorf("vault.SaveBytes: commit: %w", err)
	}
	committed = true
	return SavedItem{ID: id, Created: true, Filename: destFilename, Label: in.Label, SizeBytes: int64(len(in.Data))}, nil
}

// writeNew writes data to a file that must not exist yet (O_EXCL, which also
// refuses a symlink planted at path) and removes what it wrote on failure.
func writeNew(path string, data []byte) (retErr error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|oNoFollow, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); retErr == nil {
			retErr = cerr
		}
		if retErr != nil {
			_ = os.Remove(path)
		}
	}()
	_, err = f.Write(data)
	return err
}
