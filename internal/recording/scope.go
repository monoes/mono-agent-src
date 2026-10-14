package recording

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/monoes/mono-agent/internal/capture"
	"github.com/monoes/mono-agent/internal/profiledir"
)

// Where recordings live (contracts §6, revised).
//
// Recordings are NOT kept in the capture inbox (~/.monomind/inbox): that
// directory is ingested by monomind into the knowledge brain, and a
// recording is typed values and the DOM around them. They are capture
// envelopes all the same, written by the capture Writer, but into a store
// of their own:
//
//	~/.monoagent/recordings/                    no profile named
//	~/.monoagent/profiles/<id>/recordings/      profile <id>
//
// with 0700 directories and 0600 files. By default List and Find read every
// store (no database needed); `--profile` narrows that to one profile
// (SetProfile); tests point it anywhere (SetInboxes).

// StoreDirName is a recording store's directory name.
const StoreDirName = "recordings"

var scope struct {
	mu      sync.Mutex
	profile string
	inboxes []string
}

// SetProfile narrows List/Find to one profile's store. "" restores the
// default (every store).
func SetProfile(id string) error {
	id = strings.TrimSpace(id)
	if id != "" && !profiledir.ValidProfileID(id) {
		return fmt.Errorf("recording: unusable profile id %q", id)
	}
	scope.mu.Lock()
	defer scope.mu.Unlock()
	scope.profile = id
	return nil
}

// SetInboxes overrides the store directories read, outright. No arguments
// restores the default.
func SetInboxes(dirs ...string) {
	scope.mu.Lock()
	defer scope.mu.Unlock()
	scope.inboxes = append([]string(nil), dirs...)
}

// StoreDirs returns the store directories List and Find read, in order.
func StoreDirs() []string {
	scope.mu.Lock()
	override, profile := scope.inboxes, scope.profile
	scope.mu.Unlock()
	if len(override) > 0 {
		return append([]string(nil), override...)
	}
	if profile != "" {
		dir, err := StoreDir(profile)
		if err != nil {
			return nil
		}
		return []string{dir}
	}
	return allStores()
}

// StoreDir is where one profile's recordings live ("" = no profile).
func StoreDir(profile string) (string, error) {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".monoagent", StoreDirName), nil
	}
	if !profiledir.ValidProfileID(profile) {
		return "", fmt.Errorf("recording: unusable profile id %q", clamp(profile))
	}
	root := profiledir.Root(nil, profile)
	if root == "" {
		return "", fmt.Errorf("recording: no profile root for %q", clamp(profile))
	}
	return filepath.Join(root, StoreDirName), nil
}

// allStores is the unprofiled store plus every profile store on disk.
func allStores() []string {
	var out []string
	if dir, err := StoreDir(""); err == nil {
		out = append(out, dir)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return out
	}
	matches, _ := filepath.Glob(filepath.Join(home, ".monoagent", "profiles", "*", StoreDirName))
	sort.Strings(matches)
	for _, m := range matches {
		if profiledir.ValidProfileID(filepath.Base(filepath.Dir(m))) {
			out = append(out, m)
		}
	}
	return out
}

// profileOfStore returns the profile id whose store dir is, or "".
func profileOfStore(dir string) string {
	if filepath.Base(dir) != StoreDirName || filepath.Base(filepath.Dir(filepath.Dir(dir))) != "profiles" {
		return ""
	}
	return filepath.Base(filepath.Dir(dir))
}

// legacyInboxes are the capture inboxes recordings used to be written to:
// the default inbox and every profile's monomind inbox.
func legacyInboxes() []string {
	out := []string{capture.DefaultInbox()}
	home, err := os.UserHomeDir()
	if err != nil {
		return out
	}
	matches, _ := filepath.Glob(filepath.Join(home, ".monoagent", "profiles", "*", ".monomind", capture.ProfileInboxDir))
	sort.Strings(matches)
	return append(out, matches...)
}

// profileOfLegacyInbox returns the profile id whose monomind inbox dir is.
func profileOfLegacyInbox(dir string) string {
	if filepath.Base(dir) != capture.ProfileInboxDir || filepath.Base(filepath.Dir(dir)) != ".monomind" ||
		filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(dir)))) != "profiles" {
		return ""
	}
	return filepath.Base(filepath.Dir(filepath.Dir(dir)))
}

// safeID matches identifiers that come from the extension or a command line
// and end up in a path or an argv: recording ids, event ids, envelope
// directory names.
var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// ValidID reports whether id is safe as a path component and argv word.
func ValidID(id string) bool {
	return safeID.MatchString(id) && !strings.Contains(id, "..")
}

// DraftsDir is where `record analyze` writes drafts:
// ~/.monoagent/recording-drafts (contracts §5).
func DraftsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".monoagent", "recording-drafts"), nil
}

// ErrDraftNotFound is ResolveDraftDir's one answer for every reference it
// refuses. The reference comes from a browser, so the refusal must not say
// which way it failed: "no such file" for one path and "outside" for
// another would let a page probe which paths exist on this machine.
var ErrDraftNotFound = errors.New("draft not found or outside the drafts folder")

// ResolveDraftDir turns a caller-supplied draft reference — a draft name or
// a path — into a directory that is guaranteed to sit inside DraftsDir,
// symlinks resolved. Anything else is refused with ErrDraftNotFound, never
// with an OS error.
func ResolveDraftDir(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	root, err := DraftsDir()
	if err != nil {
		return "", ErrDraftNotFound
	}
	if ref == "" || strings.ContainsRune(ref, 0) || strings.HasPrefix(ref, "-") {
		return "", ErrDraftNotFound
	}
	dir := ref
	if !filepath.IsAbs(ref) {
		if !ValidID(ref) {
			return "", ErrDraftNotFound
		}
		dir = filepath.Join(root, ref)
	}
	// Containment is checked on the lexical path first, so a reference
	// outside the drafts folder is refused before anything is looked up.
	if !within(filepath.Clean(root), filepath.Clean(dir)) {
		return "", ErrDraftNotFound
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", ErrDraftNotFound
	}
	realDir, err := filepath.EvalSymlinks(filepath.Clean(dir))
	if err != nil || !within(realRoot, realDir) {
		return "", ErrDraftNotFound
	}
	if fi, err := os.Stat(realDir); err != nil || !fi.IsDir() {
		return "", ErrDraftNotFound
	}
	return realDir, nil
}

// within reports whether path is strictly inside root.
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != ".." &&
		!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
