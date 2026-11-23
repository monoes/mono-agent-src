// Package profiledir resolves the per-profile filesystem root each profile's
// vault files, encrypted keys, and monomind project (knowledge graph +
// memory) live under. Defaults to ~/.monoagent/profiles/<profileID>/, but a
// profile may override this with any folder the user picks (profiles.root_dir
// in the database) — e.g. an external drive or a synced folder.
package profiledir

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MachineProfileID is the reserved vault scope that holds machine-wide secrets
// (the TypeSafe key). It is never a profile's id.
const MachineProfileID = "_machine"

// ValidProfileID reports whether profileID is safe to embed in a filesystem
// path: non-empty, not the reserved MachineProfileID, and free of path
// separators and parent-directory components, so it can never escape
// ~/.monoagent/profiles/ via a join.
func ValidProfileID(profileID string) bool {
	if profileID == "" || profileID == MachineProfileID || strings.ContainsAny(profileID, `/\`) {
		return false
	}
	if strings.Contains(profileID, "..") || profileID == "." {
		return false
	}
	if strings.HasSuffix(profileID, ".") || strings.HasSuffix(profileID, " ") || strings.ContainsRune(profileID, ':') {
		return false
	}
	for _, r := range profileID {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// defaultRoot is the fallback used when a profile has no root_dir override.
// os.UserHomeDir covers Unix ($HOME) and Windows (%USERPROFILE%); $HOME is
// the last-resort fallback for environments where the lookup fails but the
// variable is still set.
func defaultRoot(profileID string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, ".monoagent", "profiles", profileID)
}

// Root returns the folder that owns everything belonging to one profile —
// either its chosen override (profiles.root_dir, if set), or the default
// ~/.monoagent/profiles/<profileID>/. db may be nil, in which case the
// default is always used (callers that genuinely have no DB handle yet,
// e.g. very early startup) — this never fails, it just can't see an
// override. An invalid profileID (see ValidProfileID) yields "" — a dead
// path — instead of joining untrusted components into a real one;
// EnsureLayout, the only creator of these directories, rejects such IDs
// outright.
func Root(db *sql.DB, profileID string) string {
	if !ValidProfileID(profileID) {
		return ""
	}
	if db != nil {
		var override string
		if err := db.QueryRow(`SELECT root_dir FROM profiles WHERE id = ?`, profileID).Scan(&override); err == nil && override != "" {
			return override
		}
	}
	return defaultRoot(profileID)
}

// IsDefaultManaged reports whether profileID currently resolves to the
// default profile root (no profiles.root_dir override) rather than a
// custom folder such as an existing coding project. Callers use this to
// decide whether the TaxonomyFolders convention and its stricter default
// org fileWrite policy should apply — a custom root_dir is explicitly
// supported for pointing a profile at existing project structure (see
// wails-app/app.go's validateFolderChoice), so that case is deliberately
// left unrestricted.
func IsDefaultManaged(db *sql.DB, profileID string) bool {
	return Root(db, profileID) == defaultRoot(profileID)
}

// VaultDir returns <profile root>/.monoagent/vault/, where this profile's
// registered files/images live. Nested under .monoagent/ (not directly in
// root) so that everything monoagent itself owns is a single, gitignorable
// unit when root_dir points at a real coding project — distinct from
// .monomind/, which belongs to the separate monomind tool's own convention,
// and from the TaxonomyFolders (docs/, workingdocs/, assets/, codes/),
// which are deliberately user/agent-visible content at the project root.
func VaultDir(db *sql.DB, profileID string) string {
	return filepath.Join(Root(db, profileID), ".monoagent", "vault")
}

// MonomindDir returns <profile root>/.monomind/, the project root monomind
// resolves its monograph/knowledge-graph databases against for this profile
// (via the MONOMIND_CWD env override — never the chat subprocess's own cwd).
func MonomindDir(db *sql.DB, profileID string) string {
	return filepath.Join(Root(db, profileID), ".monomind")
}

// TaxonomyFolder names one of a profile's standard content subfolders and
// documents its intended purpose, so callers (chat/org system prompts,
// default fileWrite policies) can generate guidance from one source of
// truth instead of hand-copying folder names.
type TaxonomyFolder struct {
	Name    string // relative to Root(db, profileID), no trailing slash
	Purpose string // one sentence, written for an agent's own guidance text
}

// TaxonomyFolders are the standard subfolders new content -- especially
// content an agent (chat or org) creates -- should default into. Unlike
// VaultDir/MonomindDir, these are not created by EnsureLayout: see its own
// doc comment for why.
var TaxonomyFolders = []TaxonomyFolder{
	{Name: "docs", Purpose: "Finished, stable reference documentation — specs, write-ups, README-style content meant to be read later, not actively being drafted."},
	{Name: "workingdocs", Purpose: "In-progress drafts, notes, and scratch working documents not yet finished — the staging area before something graduates to docs/."},
	{Name: "assets", Purpose: "Ad hoc images, charts, or other binary/media files created or fetched while doing a task, that don't need full vault registration the way vault/ items do."},
	{Name: "codes", Purpose: "Scripts, snippets, or small programs written to accomplish a task — not vault content, not internal monomind state."},
}

// EnsureLayout creates a profile's folder structure (root, .monoagent/vault,
// .monomind) if it doesn't already exist. Safe to call repeatedly. Rejects
// invalid profile IDs before any path is joined or created.
//
// Deliberately does NOT create TaxonomyFolders: EnsureLayout also runs as a
// startup migration pass over every existing profile, and materializing 4
// new folders in every profile -- including ones pointed at a real coding
// project via root_dir -- would violate the "only .monoagent/ and .monomind/"
// promise validateFolderChoice (wails-app/app.go) documents and relies on.
// Taxonomy folders are created lazily, the first time something actually
// writes into them (same as vault/documents/ already is).
func EnsureLayout(db *sql.DB, profileID string) error {
	if !ValidProfileID(profileID) {
		return fmt.Errorf("invalid profile id %q: must be non-empty and contain no '/', '\\', or '..'", profileID)
	}
	for _, dir := range []string{Root(db, profileID), VaultDir(db, profileID), MonomindDir(db, profileID)} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
	}
	ensureMonoagentGitignore(db, profileID)
	return nil
}

// ensureMonoagentGitignore writes a blanket-ignore .gitignore into
// <root>/.monoagent/ the first time it's missing — the actual reason this
// data lives under .monoagent/ at all: so it disappears from `git
// status`/`git add` as a single unit when root_dir points at a real,
// git-tracked coding project. Never overwrites an existing file (a user is
// free to edit or delete it — EnsureLayout runs on every startup for every
// profile), and never fails EnsureLayout: a missing .gitignore leaves the
// folder fully usable, just untidy in `git status`, which isn't worth
// failing profile setup over.
func ensureMonoagentGitignore(db *sql.DB, profileID string) {
	path := filepath.Join(Root(db, profileID), ".monoagent", ".gitignore")
	// O_EXCL fails on any existing entry, a dangling symlink included, so a link planted there
	// cannot make this create its target.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return
	}
	_, _ = f.WriteString("*\n")
	_ = f.Close()
}

// Exists reports whether a profile's folder has already been created —
// used as the "already migrated" marker for existing profiles.
func Exists(db *sql.DB, profileID string) bool {
	info, err := os.Stat(Root(db, profileID))
	return err == nil && info.IsDir()
}
