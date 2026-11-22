// Package docscan finds human-readable, non-code documents under a profile
// folder. Pure filesystem — no DB/vault dependency, mirroring
// internal/orgdesign's independence from App/DB.
package docscan

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// AllowedExtensions are the (lowercase, no dot) extensions treated as "a
// human-readable, non-code document" for discovery and indexing purposes.
// Deliberately separate from FileViewerModal.jsx's own fileViewerKind map:
// that one answers "can this be previewed in-app" (images, HTML, and
// source code all qualify there); this one answers "is this a document
// worth indexing" — still excludes images and raw source code, but html/
// htm are included on both sides now, since a self-contained HTML file
// (a styled report/summary, e.g. from save_document in
// internal/ai/chat) is exactly the kind of deliverable this package
// exists to surface. Safe to preview even for untrusted/agent-authored
// content: FileViewerModal's iframe uses an empty sandbox="" (no
// scripts, no same-origin), so embedded JS never executes. They cannot
// share a Go/JS source of truth anyway, so this list still isn't
// derived from that one.
var AllowedExtensions = map[string]bool{
	"md": true, "markdown": true, "txt": true, "rtf": true,
	"doc": true, "docx": true, "odt": true,
	"pdf": true,
	"xls": true, "xlsx": true, "ods": true, "csv": true,
	"ppt": true, "pptx": true,
	"html": true, "htm": true,
}

// isDotDir reports whether name is a dot-prefixed directory (.monomind,
// .git, or any other dot-folder) -- never a user document, always internal
// tool/VCS state or deliberately hidden by convention. This is a superset
// of the old exact-match ".monomind"-only exclusion; no other caller needs
// to change.
func isDotDir(name string) bool {
	return strings.HasPrefix(name, ".")
}

// agentInstructionBasenames are conventional AI coding agent
// instruction/context filenames -- e.g. CLAUDE.md is read by Claude Code,
// AGENTS.md is the cross-tool standard read by Codex CLI/Cursor/Claude
// Code's own fallback/Continue.dev/Aider/OpenHands, GEMINI.md by Gemini
// CLI. These are never dot-prefixed (so the dot-dir walk exclusion above
// doesn't catch them) and always carry a real extension (in practice
// always .md, so the extension allowlist doesn't catch them either), yet
// they're tool/agent configuration, not content a user would want listed
// as one of their documents. Deliberately NOT exhaustive of every
// dot-prefixed convention (.cursorrules, .windsurfrules, .clinerules,
// .github/copilot-instructions.md, .cursor/rules/*, .windsurf/rules/*):
// those already fail either the dot-dir exclusion or the extension check
// on their own, so listing them here would be redundant. Matched against
// the bare basename only (case-insensitive), so a per-subproject copy in
// a monorepo-style layout (e.g. packages/foo/CLAUDE.md) is excluded too.
var agentInstructionBasenames = map[string]bool{
	"claude.md":          true,
	"agents.md":          true,
	"agents.override.md": true,
	"agent.md":           true,
	"gemini.md":          true,
	"qwen.md":            true,
	"warp.md":            true,
	"conventions.md":     true,
}

// IsDocumentFile reports whether filename's extension is in
// AllowedExtensions (case-insensitive, dot-insensitive) AND its basename
// isn't a conventional AI agent instruction file (see
// agentInstructionBasenames) -- the latter technically satisfy the
// extension check (almost always .md) but are tool configuration, not a
// document worth indexing or showing to the user.
func IsDocumentFile(filename string) bool {
	if agentInstructionBasenames[strings.ToLower(filename)] {
		return false
	}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(filename), "."))
	if ext == "" {
		return false
	}
	return AllowedExtensions[ext]
}

// FileInfo is one matched file from a scan.
type FileInfo struct {
	Path      string // absolute; root + relpath, root used verbatim -- see Scan
	Filename  string
	SizeBytes int64
	ModTime   int64 // UnixNano
}

// Scan walks root recursively and returns every regular file matching
// IsDocumentFile, skipping any dot-prefixed directory (.monomind, .git,
// etc.) other than root itself -- a profile may be pointed at a
// dot-prefixed folder via root_dir, and only its descendants are excluded.
// root is used
// exactly as given -- never resolved via filepath.Abs/EvalSymlinks -- so
// every FileInfo.Path is root's own string value plus a relative suffix.
// This must stay true because monomind.IngestDocument's path-traversal
// guard compares an ingested path's prefix against
// profiledir.Root(db, profileID) called completely independently at index
// time; if this scan silently normalized root (e.g. resolving a symlink)
// while IngestDocument's own fresh call to profiledir.Root did not, a
// discovered file could fail to index with a misleading "success"-shaped
// failure and no obvious cause. Callers MUST pass
// profiledir.Root(db, profileID) unmodified.
//
// A missing root (profile folder not yet created) returns (nil, nil), not
// an error -- mirrors orgdesign/watch.go's os.IsNotExist(err) handling, so
// a fresh profile never causes an error banner. Symlinked subdirectories
// are not followed (fs.WalkDir's default), avoiding cyclic-symlink loops.
func Scan(root string) ([]FileInfo, error) {
	found, _, err := ScanLimited(root, MaxFiles)
	return found, err
}

// MaxFiles is how many files Scan returns at most; MaxFileBytes is the
// largest file it reports. A profile root_dir pointed at a home or project
// tree must not make every scan hold tens of thousands of entries or hand
// huge files to ingestion.
const (
	MaxFiles     = 20000
	MaxFileBytes = 100 << 20
)

// heavyDirs are never worth indexing and can hold tens of thousands of files.
var heavyDirs = map[string]bool{
	"node_modules": true, "vendor": true, "__pycache__": true, "site-packages": true,
}

// ScanLimited is Scan with an explicit file cap. truncated reports that the
// walk stopped at the cap and more files may exist.
func ScanLimited(root string, maxFiles int) (found []FileInfo, truncated bool, err error) {
	if _, statErr := os.Stat(root); os.IsNotExist(statErr) {
		return nil, false, nil
	}

	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // transient read error on one entry -- skip it, keep walking
		}
		if d.IsDir() {
			if path != root && (isDotDir(d.Name()) || heavyDirs[d.Name()]) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !IsDocumentFile(d.Name()) {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}
		if info.Size() > MaxFileBytes {
			return nil
		}
		if len(found) >= maxFiles {
			truncated = true
			return filepath.SkipAll
		}
		found = append(found, FileInfo{
			Path:      path,
			Filename:  d.Name(),
			SizeBytes: info.Size(),
			ModTime:   info.ModTime().UnixNano(),
		})
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return found, truncated, nil
}
