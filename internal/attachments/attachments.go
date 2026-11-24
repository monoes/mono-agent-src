// Package attachments stores files that arrive with synced messages (email
// attachments today) on disk, so anything that can read a file — an AI agent,
// a script, the user — can open them by path. Only the path is kept in the
// database; the bytes live here.
package attachments

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Dir returns the root directory holding message attachments.
func Dir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".monoagent", "attachments")
	}
	return filepath.Join(home, ".monoagent", "attachments")
}

// safeSegment turns arbitrary external text (a provider message id, a sender's
// filename) into a single safe path segment: no separators, no traversal, no
// surprises from a hostile attachment name. Uniqueness is preserved by
// appending a hash of the original whenever anything had to be rewritten.
func safeSegment(raw, fallback string) string {
	if raw == "" {
		raw = fallback
	}
	var b strings.Builder
	changed := false
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
			changed = true
		}
	}
	out := strings.Trim(b.String(), ".")
	if out == "" {
		out = fallback
		changed = true
	}
	if len(out) > 80 {
		out = out[:80]
		changed = true
	}
	if !changed {
		return out
	}
	sum := sha256.Sum256([]byte(raw))
	hash := fmt.Sprintf("%x", sum[:4])
	// Insert the disambiguation hash before the extension, not after, so a
	// rewritten "Verto Overview.pdf" lands as "Verto_Overview-a488d521.pdf"
	// rather than "Verto_Overview.pdf-a488d521" — a PDF reader (or an AI
	// agent) that dispatches on file extension must still recognize the file.
	if ext := filepath.Ext(out); ext != "" && ext != out && len(ext) <= 10 {
		return strings.TrimSuffix(out, ext) + "-" + hash + ext
	}
	return out + "-" + hash
}

// Save writes one attachment for a message and returns its absolute path.
// Attachments are grouped in a per-message directory so a message's files stay
// together and re-syncing the same message overwrites rather than duplicates.
func Save(messageID, filename string, data []byte) (string, error) {
	return SaveDistinct(messageID, filename, data, nil)
}

// SaveDistinct is Save for a sync pass that writes every attachment of one
// message: seen records the paths already written in this pass, and a second
// attachment that maps to a seen path (two "image.png") gets a content-hash
// suffix instead of overwriting the first. A nil seen behaves as Save.
func SaveDistinct(messageID, filename string, data []byte, seen map[string]bool) (string, error) {
	return saveIn(Dir(), messageID, filename, data, seen)
}

// saveIn is SaveDistinct under the given root folder.
func saveIn(root, messageID, filename string, data []byte, seen map[string]bool) (string, error) {
	dir := filepath.Join(root, safeSegment(messageID, "message"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("attachments: create dir: %w", err)
	}
	name := safeSegment(filename, "attachment")
	path := filepath.Join(dir, name)
	// A different attachment of this pass already holds this name (two
	// "image.png" in one message): disambiguate by content hash.
	if seen[path] {
		sum := sha256.Sum256(data)
		hash := fmt.Sprintf("%x", sum[:4])
		if ext := filepath.Ext(name); ext != "" && ext != name && len(ext) <= 10 {
			name = strings.TrimSuffix(name, ext) + "-" + hash + ext
		} else {
			name += "-" + hash
		}
		path = filepath.Join(dir, name)
	}
	if seen != nil {
		seen[path] = true
	}
	// Write via a temp file and rename so a reader never sees a partial file.
	tmp, err := os.CreateTemp(dir, ".att-*")
	if err != nil {
		return "", fmt.Errorf("attachments: write %s: %w", path, err)
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(data)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmpName, 0o600)
	}
	if werr == nil {
		werr = os.Rename(tmpName, path)
	}
	if werr != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("attachments: write %s: %w", path, werr)
	}
	return path, nil
}
