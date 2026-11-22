package docscan_test

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/monoes/mono-agent/internal/docscan"
)

func TestIsDocumentFile(t *testing.T) {
	cases := map[string]bool{
		"resume.md":      true,
		"resume.MD":      true,
		"notes.markdown": true,
		"report.pdf":     true,
		"cv.docx":        true,
		"cv.doc":         true,
		"letter.odt":     true,
		"budget.xlsx":    true,
		"budget.xls":     true,
		"budget.ods":     true,
		"data.csv":       true,
		"slides.pptx":    true,
		"slides.ppt":     true,
		"plain.txt":      true,
		"formatted.rtf":  true,
		"report.html":    true,
		"report.HTML":    true,
		"page.htm":       true,
		"main.go":        false,
		"index.js":       false,
		"style.css":      false,
		"noextension":    false,
		"image.png":      false,
		"archive.zip":    false,
		".hidden":        false,
		"config.yaml":    false,
		// AI coding agent instruction/context files -- conventionally named,
		// have a real extension (almost always .md), and don't live inside a
		// dot-prefixed directory, so neither the extension check nor the
		// dot-dir walk exclusion would otherwise catch them. Not a user
		// document even though they pass the extension test.
		"CLAUDE.md":          false,
		"claude.md":          false, // case-insensitive match
		"AGENTS.md":          false,
		"AGENTS.override.md": false,
		"AGENT.md":           false, // singular variant some tools use
		"GEMINI.md":          false,
		"QWEN.md":            false,
		"WARP.md":            false,
		"CONVENTIONS.md":     false, // Aider's convention
		// A file that merely contains one of these words isn't excluded --
		// only an exact (case-insensitive) basename match.
		"CLAUDE_NOTES.md": true,
		"my-agents.md":    true,
	}
	for name, want := range cases {
		if got := docscan.IsDocumentFile(name); got != want {
			t.Errorf("IsDocumentFile(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestScanFindsDocumentFilesRecursively(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "resume.md"), "hello")
	mustWrite(t, filepath.Join(root, "notes.txt"), "hello")
	mustWrite(t, filepath.Join(root, "image.png"), "not a doc")
	if err := os.MkdirAll(filepath.Join(root, "sub", "deeper"), 0700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "sub", "deeper", "report.pdf"), "hello")

	found, err := docscan.Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	var names []string
	for _, f := range found {
		names = append(names, f.Filename)
	}
	sort.Strings(names)
	want := []string{"notes.txt", "report.pdf", "resume.md"}
	if len(names) != len(want) {
		t.Fatalf("Scan found %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("Scan found %v, want %v", names, want)
		}
	}
}

func TestScanExcludesMonomindDir(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".monomind", "graph"), 0700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, ".monomind", "graph", "notes.md"), "internal state, not a user doc")
	mustWrite(t, filepath.Join(root, "real.md"), "a real document")

	found, err := docscan.Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(found) != 1 || found[0].Filename != "real.md" {
		t.Fatalf("expected only real.md, got %+v", found)
	}
}

func TestScanExcludesAnyDotDir(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".git", "notes.md"), "vcs internals, not a user doc")
	mustWrite(t, filepath.Join(root, ".foo", "notes.md"), "arbitrary dot-dir, not a user doc")
	mustWrite(t, filepath.Join(root, "real.md"), "a real document")

	found, err := docscan.Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(found) != 1 || found[0].Filename != "real.md" {
		t.Fatalf("expected only real.md, got %+v", found)
	}
}

func TestScanExcludesNestedDotDir(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "sub", ".hidden", "deeper", "notes.md"), "nested dot-dir, not a user doc")
	mustWrite(t, filepath.Join(root, "sub", "real.md"), "a real document")

	found, err := docscan.Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(found) != 1 || found[0].Filename != "real.md" {
		t.Fatalf("expected only real.md, got %+v", found)
	}
}

func TestScanDoesNotExcludeDirWithDotNotAtStart(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "my.project", "notes.md"), "dot not at start -- not excluded")

	found, err := docscan.Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(found) != 1 || found[0].Filename != "notes.md" {
		t.Fatalf("expected notes.md to be found (dir name has a dot but doesn't start with one), got %+v", found)
	}
}

// TestScanExcludesAgentInstructionFiles proves the exclusion applies through
// the full Scan path, not just IsDocumentFile in isolation -- both at the
// profile root and nested inside an ordinary (non-dot) subdirectory, since
// these conventions apply per-subproject in monorepo-style layouts too.
func TestScanExcludesAgentInstructionFiles(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "CLAUDE.md"), "agent instructions, not a user doc")
	mustWrite(t, filepath.Join(root, "AGENTS.md"), "agent instructions, not a user doc")
	mustWrite(t, filepath.Join(root, "GEMINI.md"), "agent instructions, not a user doc")
	mustWrite(t, filepath.Join(root, "sub", "CLAUDE.md"), "nested agent instructions, not a user doc")
	mustWrite(t, filepath.Join(root, "real.md"), "a real document")

	found, err := docscan.Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(found) != 1 || found[0].Filename != "real.md" {
		t.Fatalf("expected only real.md, got %+v", found)
	}
}

func TestScanStillWalksDotPrefixedRootItself(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, ".my-notes")
	mustWrite(t, filepath.Join(root, "real.md"), "a real document")

	found, err := docscan.Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(found) != 1 || found[0].Filename != "real.md" {
		t.Fatalf("expected real.md to be found even though root itself is dot-prefixed, got %+v", found)
	}
}

func TestScanMissingRootReturnsNilNotError(t *testing.T) {
	found, err := docscan.Scan(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("expected nil error for a missing root, got %v", err)
	}
	if found != nil {
		t.Fatalf("expected nil slice for a missing root, got %+v", found)
	}
}

func TestScanReturnsAbsolutePathBuiltFromRootVerbatim(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "sub", "resume.md"), "hello")

	found, err := docscan.Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("expected 1 file, got %d", len(found))
	}
	want := filepath.Join(root, "sub", "resume.md")
	if found[0].Path != want {
		t.Fatalf("Path = %q, want %q (root used verbatim, not resolved)", found[0].Path, want)
	}
}

func TestScanReportsSizeAndModTime(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "resume.md")
	mustWrite(t, path, "twelve bytes!")

	found, err := docscan.Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("expected 1 file, got %d", len(found))
	}
	fi, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if found[0].SizeBytes != fi.Size() {
		t.Errorf("SizeBytes = %d, want %d", found[0].SizeBytes, fi.Size())
	}
	if found[0].ModTime != fi.ModTime().UnixNano() {
		t.Errorf("ModTime = %d, want %d", found[0].ModTime, fi.ModTime().UnixNano())
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestScanSkipsHeavyDirsAndOversizeFiles(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"node_modules/x", "vendor", "__pycache__", "site-packages", "keep"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, d, "a.md"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	big := filepath.Join(root, "keep", "big.pdf")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(docscan.MaxFileBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	got, err := docscan.Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || filepath.Base(filepath.Dir(got[0].Path)) != "keep" || got[0].Filename != "a.md" {
		t.Fatalf("want only keep/a.md, got %+v", got)
	}
}

func TestScanLimitedCapsFileCount(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 5; i++ {
		if err := os.WriteFile(filepath.Join(root, string(rune('a'+i))+".md"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, truncated, err := docscan.ScanLimited(root, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || !truncated {
		t.Fatalf("got %d files truncated=%v, want 3 true", len(got), truncated)
	}
}
