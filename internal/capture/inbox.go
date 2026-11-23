package capture

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// InboxEnv overrides the inbox location outright; HomeEnv overrides only
// the monomind home it sits under. Both exist because the inbox is a
// contract shared with the monomind side of the browser track — the tests
// on either side need to point it somewhere disposable, and a user who
// moved ~/.monomind should not have captures land somewhere else.
const (
	InboxEnv = "MONOMIND_INBOX"
	HomeEnv  = "MONOMIND_HOME"
)

// DefaultInbox is where captures land: ~/.monomind/inbox, unless
// overridden. It never fails — a home directory that cannot be resolved
// falls back to a relative path rather than stopping a capture.
func DefaultInbox() string {
	if dir := strings.TrimSpace(os.Getenv(InboxEnv)); dir != "" {
		return dir
	}
	if home := strings.TrimSpace(os.Getenv(HomeEnv)); home != "" {
		return filepath.Join(home, "inbox")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".monomind", "inbox")
	}
	return filepath.Join(home, ".monomind", "inbox")
}

// Entry is one envelope directory as `capture list` reports it.
//
// The scalar fields are the listing's own columns. Meta is the whole
// provenance record the directory holds, carried along because reading it
// is what produced those columns in the first place — a caller that needs
// the collection, the tags or the content hash must not have to open and
// parse meta.json a second time.
type Entry struct {
	Path       string   `json:"path"`
	URL        string   `json:"url"`
	Title      string   `json:"title"`
	CapturedAt string   `json:"capturedAt"`
	Bytes      int64    `json:"bytes"`
	Artifacts  []string `json:"artifacts"`
	// Profile is the capture's profile, promoted out of Meta because it
	// decides which inbox this entry was even found in — a listing that
	// spans profiles is unreadable without it. Empty for a capture that
	// named no profile.
	Profile string `json:"profile,omitempty"`
	Meta    Meta   `json:"meta"`
}

// MaxMetaFileBytes bounds the small JSON files of an envelope (meta.json,
// classification.json, summary status, the task board). One planted huge
// file must not stall every list, sync or index pass.
const MaxMetaFileBytes = 4 << 20

// ErrTooLarge is wrapped by ReadCapped when a file is over its cap.
var ErrTooLarge = errors.New("file is too large")

// ReadCapped reads path, refusing (ErrTooLarge) one longer than max bytes
// without loading more than max+1 of it.
func ReadCapped(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	blob, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(blob)) > max {
		return nil, fmt.Errorf("%s: %w (over %d bytes)", path, ErrTooLarge, max)
	}
	return blob, nil
}

// ReadMeta reads one capture directory's provenance record.
//
// This is the single-capture counterpart to List: a caller that was handed
// a path (a task being filed against a capture, a staged directory during
// an import) wants exactly this and nothing else. The error wraps
// os.ErrNotExist when the directory holds no meta.json, which is how a
// caller distinguishes "not a capture" from "unreadable capture".
func ReadMeta(dir string) (*Meta, error) {
	path := filepath.Join(dir, MetaFile)
	blob, err := ReadCapped(path, MaxMetaFileBytes)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var meta Meta
	if err := json.Unmarshal(blob, &meta); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return &meta, nil
}

// List returns the envelopes in inbox, newest first. Directories still
// being staged (dot-prefixed) and directories without a meta.json are
// skipped: both are captures that have not landed, and neither should be
// ingested. A missing inbox is not an error — it just has nothing in it.
func List(inbox string) ([]Entry, error) {
	if strings.TrimSpace(inbox) == "" {
		inbox = DefaultInbox()
	}
	dirents, err := os.ReadDir(inbox)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read inbox %s: %w", inbox, err)
	}

	entries := make([]Entry, 0, len(dirents))
	for _, d := range dirents {
		if !d.IsDir() || strings.HasPrefix(d.Name(), ".") {
			continue
		}
		entry, ok := readEntry(filepath.Join(inbox, d.Name()))
		if !ok {
			continue
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].CapturedAt != entries[j].CapturedAt {
			return entries[i].CapturedAt > entries[j].CapturedAt
		}
		return entries[i].Path > entries[j].Path
	})
	return entries, nil
}

// readEntry reads one envelope directory, reporting false for anything
// that is not a landed capture.
func readEntry(dir string) (Entry, bool) {
	meta, err := ReadMeta(dir)
	if err != nil {
		return Entry{}, false
	}
	entry := Entry{
		Path:       dir,
		URL:        meta.DedupeURL(),
		Title:      meta.Title,
		CapturedAt: meta.CapturedAt,
		Profile:    meta.Profile,
		Meta:       *meta,
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		return Entry{}, false
	}
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		// meta.json counts towards the size but is not an artifact.
		if f.Name() != MetaFile {
			entry.Artifacts = append(entry.Artifacts, f.Name())
		}
		if info, err := f.Info(); err == nil {
			entry.Bytes += info.Size()
		}
	}
	sort.Strings(entry.Artifacts)
	return entry, true
}
