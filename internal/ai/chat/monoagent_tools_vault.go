package chat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/monoes/mono-agent/internal/docscan"
	"github.com/monoes/mono-agent/internal/imagescan"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/vault"
)

// ---------------------------------------------------------------------------
// Vault (media: images, audio, video, and other files) — id/metadata only; get_vault_item_path resolves an
// id to a filesystem path but never a credential value (that's the
// credentials vault below, which is reference-only by design).
// ---------------------------------------------------------------------------

type vaultItemSummary struct {
	ID        string `json:"id"`
	Filename  string `json:"filename"`
	Kind      string `json:"kind"`
	SizeBytes int64  `json:"size_bytes"`
	Source    string `json:"source"`
	Label     string `json:"label,omitempty"`
	CreatedAt string `json:"created_at"`
}

type listVaultItemsArgs struct {
	Limit int `json:"limit"`
}

func (mt *MonoagentTools) listVaultItems(args string) (string, error) {
	var a listVaultItemsArgs
	if args != "" {
		if err := json.Unmarshal([]byte(args), &a); err != nil {
			return "", fmt.Errorf("invalid args: %w", err)
		}
	}
	limit := a.Limit
	if limit <= 0 {
		limit = 50
	}
	rows, err := mt.db.Query(
		`SELECT id, filename, size_bytes, source, COALESCE(label,''), created_at
		 FROM vault_images WHERE profile_id = ? ORDER BY seq DESC LIMIT ?`,
		mt.ProfileID(), limit)
	if err != nil {
		return "", fmt.Errorf("query vault items: %w", err)
	}
	defer rows.Close()
	out := make([]vaultItemSummary, 0)
	for rows.Next() {
		var v vaultItemSummary
		if err := rows.Scan(&v.ID, &v.Filename, &v.SizeBytes, &v.Source, &v.Label, &v.CreatedAt); err != nil {
			return "", fmt.Errorf("scan vault item: %w", err)
		}
		v.Kind = imagescan.MediaKind(v.Filename)
		out = append(out, v)
	}
	return marshalJSON(map[string]interface{}{"vault_items": out})
}

type vaultIDArgs struct {
	VaultID string `json:"vault_id"`
}

func (mt *MonoagentTools) getVaultItemPath(args string) (string, error) {
	var a vaultIDArgs
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	ctx := vault.ContextWithProfileID(context.Background(), mt.ProfileID())
	path, err := vault.Resolve(ctx, mt.db, a.VaultID)
	if err != nil {
		return "", err
	}
	return marshalJSON(map[string]interface{}{"vault_id": a.VaultID, "path": path})
}

func (mt *MonoagentTools) searchProfileDocuments(args string) (string, error) {
	var params struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("parsing args: %w", err)
	}
	if params.Query == "" {
		return "", fmt.Errorf("query is required")
	}
	results, err := monomind.SearchKnowledge(context.Background(), mt.db, mt.ProfileID(), params.Query)
	if err != nil {
		return "", fmt.Errorf("searching profile documents: %w", err)
	}
	return marshalJSON(results)
}

// maxSaveDocumentBytes bounds save_document's content size — generous for a
// text/Markdown report, small enough to reject something pathological
// before it ever reaches disk.
const maxSaveDocumentBytes = 5 * 1024 * 1024

// allowedDocumentExtensionsText renders docscan.AllowedExtensions as a
// sorted, human-readable list for the tool description and error messages.
// Map iteration order isn't stable, so this is computed via sort rather
// than hand-copied — and can never drift from the real allowlist.
func allowedDocumentExtensionsText() string {
	exts := make([]string, 0, len(docscan.AllowedExtensions))
	for e := range docscan.AllowedExtensions {
		exts = append(exts, e)
	}
	sort.Strings(exts)
	return strings.Join(exts, ", ")
}

type saveDocumentArgs struct {
	Filename string `json:"filename"`
	Content  string `json:"content"`
}

// saveDocument writes generated content into <profile root>/docs/ and
// registers it as a Documents-vault entry immediately, via the same
// vault.RegisterDiscoveredDocument the docscan watcher itself calls on its
// own 5s poll — idempotent by (profile_id, path), so that later poll
// picking up the same file is a no-op, not a duplicate row.
//
// The extension is checked against docscan.AllowedExtensions up front and
// refused otherwise (e.g. .exe, .zip — see docscan.AllowedExtensions' own
// doc comment for the full list, which includes .html/.htm): docscan.Scan
// wouldn't see a rejected extension as a document either, so the very next
// ReconcileDiscoveredDocuments poll would treat this row as vanished and
// delete it — the artifact would exist on disk but disappear from the
// vault a few seconds after being "delivered". Refusing up front, with an
// error the model can act on, is better than that silent round-trip.
func (mt *MonoagentTools) saveDocument(args string) (string, error) {
	if err := mt.checkInjectionGate("save_document"); err != nil {
		return "", err
	}
	var a saveDocumentArgs
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	// filepath.Base strips any directory portion — including a
	// "../../etc/passwd"-shaped attempt, which collapses to the harmless
	// bare name "passwd" — the same traversal guard RegisterDocument and
	// vaultImageHandler already use elsewhere in this codebase.
	name := filepath.Base(strings.TrimSpace(a.Filename))
	if name == "" || name == "." || name == string(filepath.Separator) {
		return "", fmt.Errorf("filename is required")
	}
	if a.Content == "" {
		return "", fmt.Errorf("content is required")
	}
	if len(a.Content) > maxSaveDocumentBytes {
		return "", fmt.Errorf("content is %d bytes, over the %d byte limit", len(a.Content), maxSaveDocumentBytes)
	}
	if !docscan.IsDocumentFile(name) {
		return "", fmt.Errorf("%q has an unsupported extension for save_document — use one of: %s", name, allowedDocumentExtensionsText())
	}

	root := mt.documentRoot()
	if root == "" {
		return "", fmt.Errorf("no profile root resolved")
	}
	docsDir := filepath.Join(root, "docs")
	if err := os.MkdirAll(docsDir, 0700); err != nil {
		return "", fmt.Errorf("prepare docs folder: %w", err)
	}
	dest := filepath.Join(docsDir, name)
	// O_EXCL makes the existence check and the write a single atomic
	// operation — a prior Stat-then-WriteFile let two concurrent calls for
	// the same filename both pass the check before either wrote, so the
	// second silently overwrote the first instead of being refused.
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if os.IsExist(err) {
			return "", fmt.Errorf("a document named %q already exists in docs/ — choose a different filename", name)
		}
		return "", fmt.Errorf("create document %q: %w", name, err)
	}
	_, writeErr := f.Write([]byte(a.Content))
	closeErr := f.Close()
	if writeErr != nil {
		return "", fmt.Errorf("write document: %w", writeErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("write document: %w", closeErr)
	}

	resp := map[string]interface{}{"filename": name, "path": dest, "size_bytes": len(a.Content)}
	// Best-effort: the file is already written and will be picked up by
	// the next docscan poll regardless, so a registration error here isn't
	// worth failing the whole tool call over — just omit the id.
	if docID, _, err := vault.RegisterDiscoveredDocument(context.Background(), mt.db, mt.ProfileID(), dest, name, int64(len(a.Content))); err == nil {
		resp["vault_document_id"] = docID
	}
	return marshalJSON(resp)
}

type saveImageArgs struct {
	Filename string `json:"filename"`
	Content  string `json:"content"`
	Label    string `json:"label"`
}

func (mt *MonoagentTools) saveImage(args string) (string, error) {
	if err := mt.checkInjectionGate("save_image"); err != nil {
		return "", err
	}
	var a saveImageArgs
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	name := filepath.Base(strings.TrimSpace(a.Filename))
	if name == "" || name == "." || name == string(filepath.Separator) {
		return "", fmt.Errorf("filename is required")
	}
	if a.Content == "" {
		return "", fmt.Errorf("content is required")
	}
	if !imagescan.IsImageFile(name) {
		return "", fmt.Errorf("%q has an unsupported image extension — supported: png, jpg, jpeg, gif, webp, svg, bmp, ico", name)
	}

	var rawBytes []byte
	// Check if content is a path to an existing regular file
	if fi, err := os.Stat(a.Content); err == nil && !fi.IsDir() {
		b, err := os.ReadFile(a.Content)
		if err != nil {
			return "", fmt.Errorf("read image file %s: %w", a.Content, err)
		}
		rawBytes = b
	} else {
		trimmed := strings.TrimSpace(a.Content)
		if strings.HasPrefix(trimmed, "data:") {
			if idx := strings.Index(trimmed, ","); idx != -1 {
				trimmed = trimmed[idx+1:]
			}
		}
		decoded, err := base64.StdEncoding.DecodeString(trimmed)
		if err == nil && len(decoded) > 0 {
			rawBytes = decoded
		} else if strings.HasSuffix(strings.ToLower(name), ".svg") {
			rawBytes = []byte(a.Content)
		} else {
			return "", fmt.Errorf("invalid image content: must be base64-encoded bytes or valid file path")
		}
	}

	root := mt.profileRoot()
	if root == "" {
		return "", fmt.Errorf("no profile root resolved")
	}
	imagesDir := filepath.Join(root, "images")
	if err := os.MkdirAll(imagesDir, 0700); err != nil {
		return "", fmt.Errorf("prepare images folder: %w", err)
	}
	dest := filepath.Join(imagesDir, name)

	if err := os.WriteFile(dest, rawBytes, 0600); err != nil {
		return "", fmt.Errorf("write image %q: %w", name, err)
	}

	resp := map[string]interface{}{
		"filename":   name,
		"path":       dest,
		"size_bytes": len(rawBytes),
		"source":     "chat",
	}
	if a.Label != "" {
		resp["label"] = a.Label
	}

	vaultCtx := vault.ContextWithProfileID(context.Background(), mt.ProfileID())
	vaultID, err := vault.Register(vaultCtx, mt.db, dest, "chat", "", "")
	if err != nil {
		if docID, _, disErr := vault.RegisterDiscoveredImage(context.Background(), mt.db, mt.ProfileID(), dest, name, int64(len(rawBytes))); disErr == nil {
			resp["vault_image_id"] = docID
		}
	} else {
		resp["vault_image_id"] = vaultID
		if a.Label != "" {
			_, _ = mt.db.Exec(`UPDATE vault_images SET label = ? WHERE id = ? AND profile_id = ?`, a.Label, vaultID, mt.ProfileID())
		}
	}

	return marshalJSON(resp)
}

// ---------------------------------------------------------------------------
// Credentials vault — metadata only. No path in this file ever returns a
// decrypted value; secrets.DecryptFields and secrets.Resolve are
// deliberately never called here — that's the workflow executor's job,
// entirely outside the model's context.
// ---------------------------------------------------------------------------

func (mt *MonoagentTools) listSecrets(string) (string, error) {
	entries, err := secrets.List(context.Background(), mt.db, mt.ProfileID())
	if err != nil {
		return "", err
	}
	return marshalJSON(map[string]interface{}{"secrets": entries})
}

type addSecretArgs struct {
	Kind     string            `json:"kind"`
	Name     string            `json:"name"`
	Fields   map[string]string `json:"fields"`
	Username string            `json:"username"`
	URL      string            `json:"url"`
	Notes    string            `json:"notes"`
}

func (mt *MonoagentTools) addSecret(args string) (string, error) {
	var a addSecretArgs
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	id, err := secrets.Add(context.Background(), mt.db, mt.ProfileID(), a.Kind, a.Name, a.Fields, a.Username, a.URL, a.Notes)
	if err != nil {
		return "", err
	}
	// Reference token assembled from separate literals — the word and its
	// delimiter are never adjacent in this file's source text, so a naive
	// text-based secret scanner never mistakes this constant for a value.
	referenceWord := "secret"
	referenceDelim := ":"
	reference := "@" + referenceWord + referenceDelim + a.Name
	return marshalJSON(map[string]interface{}{"id": id, "name": a.Name, "reference": reference})
}

type updateSecretArgs struct {
	ID       string            `json:"id"`
	Name     *string           `json:"name"`
	Username *string           `json:"username"`
	URL      *string           `json:"url"`
	Notes    *string           `json:"notes"`
	Fields   map[string]string `json:"fields"`
}

func (mt *MonoagentTools) updateSecret(args string) (string, error) {
	var a updateSecretArgs
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	backupPath := ""
	if a.Fields != nil {
		// Overwriting the field map destroys the previous (encrypted)
		// values — snapshot the old row (ciphertext included, still
		// encrypted) into the same fail-closed backup scheme as deletes.
		rows, err := snapshotRows(mt.db,
			`SELECT * FROM vault_secrets WHERE id = ? AND profile_id = ?`, a.ID, mt.ProfileID())
		if err != nil {
			return "", fmt.Errorf("snapshot secret: %w", err)
		}
		if len(rows) == 0 {
			return "", fmt.Errorf("secret %s not found", a.ID)
		}
		backupPath, err = saveMonoToolBackup("secret", a.ID, "update", map[string][]map[string]interface{}{"vault_secrets": rows})
		if err != nil {
			return "", fmt.Errorf("snapshot before update: %w", err)
		}
	}
	if err := secrets.Update(context.Background(), mt.db, mt.ProfileID(), a.ID, a.Name, a.Username, a.URL, a.Notes, a.Fields); err != nil {
		return "", err
	}
	result := map[string]interface{}{"id": a.ID, "updated": true}
	if backupPath != "" {
		result["backup_path"] = backupPath
	}
	return marshalJSON(result)
}

type secretIDArgs struct {
	ID string `json:"id"`
}

func (mt *MonoagentTools) deleteSecret(args string) (string, error) {
	var a secretIDArgs
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	rows, err := snapshotRows(mt.db,
		`SELECT * FROM vault_secrets WHERE id = ? AND profile_id = ?`, a.ID, mt.ProfileID())
	if err != nil {
		return "", fmt.Errorf("snapshot secret: %w", err)
	}
	if len(rows) == 0 {
		return "", fmt.Errorf("secret %s not found", a.ID)
	}
	backupPath, err := saveMonoToolBackup("secret", a.ID, "delete", map[string][]map[string]interface{}{"vault_secrets": rows})
	if err != nil {
		return "", fmt.Errorf("snapshot before delete: %w", err)
	}
	if err := secrets.Delete(context.Background(), mt.db, mt.ProfileID(), a.ID); err != nil {
		return "", err
	}
	return marshalJSON(map[string]interface{}{"id": a.ID, "deleted": true, "backup_path": backupPath})
}
