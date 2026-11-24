package orgdesign

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// orgDir is the directory name org configs live under, relative to a
// project root — matches monomind's own ORG_DIR constant
// (packages/@monomind/cli/src/orgrt/types.ts).
const orgDir = ".monomind/orgs"

// orgNameRe mirrors monomind's own ORG_NAME_RE
// (packages/@monomind/cli/src/commands/org.ts) — anything else is rejected
// to prevent path traversal via a crafted org name.
var orgNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// orgArtifactSuffixes mirrors monomind's ORG_ARTIFACT_SUFFIXES
// (commands/org.ts) verbatim — org-internal state files
// (<name>-state.json, <name>-goals.json, ...) share the <name>.json naming
// pattern with a real org config and must be excluded from discovery, or
// the watcher fires on every write a running daemon performs and role
// listings show phantom "orgs". Matched with strings.HasSuffix, never
// strings.Contains — see the comment on IsOrgConfigFile.
var orgArtifactSuffixes = []string{
	"-state", "-goals", "-threads", "-activity", "-approvals", "-members",
	"-secrets", "-budgets", "-routines", "-issues", "-projects",
	"-workspaces", "-worktrees", "-environments", "-plugins", "-adapters",
	"-join-requests", "-bootstrap", "-project-workspaces",
	"-approval-comments", "-skills", "-runstate",
}

// ValidOrgName reports whether name is safe to use in a path.
func ValidOrgName(name string) bool {
	return name != "" && orgNameRe.MatchString(name)
}

// OrgsDir returns <profileRoot>/.monomind/orgs.
func OrgsDir(profileRoot string) string {
	return filepath.Join(profileRoot, orgDir)
}

// ConfigPath returns the config file path for name under profileRoot,
// validating name first (path-traversal defense — mirrors monomind's own
// validateOrgName).
func ConfigPath(profileRoot, name string) (string, error) {
	if !ValidOrgName(name) {
		return "", fmt.Errorf("invalid org name: %q", name)
	}
	return filepath.Join(OrgsDir(profileRoot), name+".json"), nil
}

// IsOrgConfigFile reports whether filename (a base name, no directory
// component) is a real org config file — a .json file that is not an
// AppleDouble sidecar (._*), not a legacy v1 backup (*.v1.json), and does
// not end with any of monomind's own artifact suffixes. Mirrors
// listOrgConfigFiles (commands/org.ts) exactly, including its own note that
// this must be endsWith/HasSuffix, not a substring check — an org
// legitimately named "state-machine" would otherwise be hidden.
func IsOrgConfigFile(filename string) bool {
	if !strings.HasSuffix(filename, ".json") {
		return false
	}
	if strings.HasPrefix(filename, "._") {
		return false
	}
	if strings.HasSuffix(filename, ".v1.json") {
		return false
	}
	for _, suf := range orgArtifactSuffixes {
		if strings.HasSuffix(filename, suf+".json") {
			return false
		}
	}
	// The stem has to be a name an org could actually have. Any other .json
	// that happens to sit in the orgs folder — .mcp.json, a tool's config,
	// something a user dropped there — is not an org, and listing it produced
	// a phantom that monomind then refused every operation on ("invalid org
	// name") while the designer reported it as an org with no root role.
	return ValidOrgName(strings.TrimSuffix(filename, ".json"))
}

// IsOrgArtifactFile reports whether filename is org's own artifact file —
// exactly org's name followed by one of monomind's artifact suffixes. Org
// names nest ("app" is a prefix of "app-eu"), so a caller moving an org's
// files must ask this rather than test the "<org>-" prefix: "app-eu-state.json"
// is org "app-eu"'s state, never org "app"'s.
func IsOrgArtifactFile(org, filename string) bool {
	rest, ok := strings.CutPrefix(filename, org)
	if !ok || !strings.HasSuffix(rest, ".json") {
		return false
	}
	return containsSuffix(orgArtifactSuffixes, strings.TrimSuffix(rest, ".json"))
}

func containsSuffix(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// ListOrgNames returns every real org config's name (file stem) under
// profileRoot's orgs directory, sorted. Returns an empty slice (not an
// error) if the directory doesn't exist yet.
func ListOrgNames(profileRoot string) ([]string, error) {
	dir := OrgsDir(profileRoot)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !IsOrgConfigFile(e.Name()) {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".json"))
	}
	return names, nil
}

// LoadPath reads and parses an org config file at an exact path.
func LoadPath(path string) (*Doc, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var d Doc
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	sum := sha256.Sum256(b)
	d.loadedSHA = hex.EncodeToString(sum[:])
	d.loadedRaw = b
	return &d, nil
}

// Load reads and parses the named org's config file under profileRoot.
func Load(profileRoot, name string) (*Doc, error) {
	path, err := ConfigPath(profileRoot, name)
	if err != nil {
		return nil, err
	}
	return LoadPath(path)
}

// ErrConcurrentEdit is Save refusing to overwrite an org file that changed since d was loaded.
var ErrConcurrentEdit = errors.New("the org file changed since it was loaded; reload and apply the edit again")

// Save validates d, then writes it to <profileRoot>/.monomind/orgs/<d.Name>.json
// atomically (write to a .tmp sibling, then rename — the same pattern as
// internal/workflow/file_store.go's SaveWorkflow and monomind's own
// migrateOrgFile). Returns the sha256 of the exact bytes written, which
// callers use to register a watcher self-write suppression before emitting
// their own live-update event.
//
// Save does NOT run monomind's own `org validate` — that is the caller's
// responsibility (see wails-app/app_orgs_design.go), since it requires a
// resolved monomind binary and this package intentionally has no such
// dependency (it must remain usable, and testable, standalone).
func Save(profileRoot string, d *Doc) (sha string, err error) {
	if err := Validate(d); err != nil {
		return "", err
	}
	path, err := ConfigPath(profileRoot, d.Name)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("creating orgs directory: %w", err)
	}
	next, err := encodeNoHTML(d)
	if err != nil {
		return "", fmt.Errorf("encoding org config: %w", err)
	}
	data, err := reflowLike(d.loadedRaw, next)
	if err != nil {
		return "", fmt.Errorf("encoding org config: %w", err)
	}
	data = append(data, '\n')

	release, err := lockOrgFile(path)
	if err != nil {
		return "", err
	}
	defer release()
	// Compare-and-swap: a Doc loaded from the file saves only over the bytes it was loaded from.
	if d.loadedSHA != "" {
		if cur, rerr := os.ReadFile(path); rerr == nil {
			sum := sha256.Sum256(cur)
			if hex.EncodeToString(sum[:]) != d.loadedSHA {
				return "", fmt.Errorf("%w: %s", ErrConcurrentEdit, path)
			}
		}
	}

	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return "", fmt.Errorf("writing %s: %w", path, err)
	}
	tmp := f.Name()
	_, werr := f.Write(data)
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp, 0o644)
	}
	if werr != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("writing %s: %w", tmp, werr)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("renaming %s to %s: %w", tmp, path, err)
	}

	sum := sha256.Sum256(data)
	d.loadedSHA = hex.EncodeToString(sum[:])
	d.loadedRaw = data
	return d.loadedSHA, nil
}

// Delete removes an org's config file only — never its run-data
// subdirectory (<orgsDir>/<name>/, containing bus.jsonl, checkpoints, and
// sentinel files), which callers who want a full teardown should remove via
// `monomind org delete`, not this package.
func Delete(profileRoot, name string) error {
	path, err := ConfigPath(profileRoot, name)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// InheritLoaded makes d describe the same file state as from: the bytes (and
// their sha) the next Save lays out and the re-sign rule compares against.
// For a caller that replaces a Doc it has just saved with an equivalent one
// built another way (decoded from a CLI's JSON): without it the replacement
// has no file layout to follow, and the next Save would reorder the file.
// Only call it when d is from's content as reconciled, not a different file.
func (d *Doc) InheritLoaded(from *Doc) {
	d.loadedSHA, d.loadedRaw = from.loadedSHA, from.loadedRaw
}

// InheritLayout lets d's next Save lay the file out like from's bytes without
// claiming d was loaded from them: LoadedSHA stays as it is (empty for a Doc
// decoded from JSON), so the re-sign rule, which trusts only a Doc loaded from
// the exact signed bytes, is unchanged.
func (d *Doc) InheritLayout(from *Doc) {
	d.loadedRaw = from.loadedRaw
}
