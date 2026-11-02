//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Between opening the file and re-resolving its name, another regular file
// inside the chat folder takes the name (a rename over it, or a hard link to
// another file). The file we hold is not the one the name now means, so the
// read must be refused. Removing the os.SameFile check makes this fail.
func TestReadChatMarkdownRefusesAnotherInsideFileSwappedInAfterOpen(t *testing.T) {
	root, _ := chatTree(t)
	t.Cleanup(func() { chatAfterOpen = nil })
	target := filepath.Join(root, "docs", "plan.md")
	other := filepath.Join(root, "docs", "other.md")
	write := func(p, s string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// a different .md is renamed over the name
	write(other, "# other\n")
	chatAfterOpen = func() { _ = os.Rename(other, target) }
	if s, err := readChatMarkdown(root, "docs/plan.md"); err == nil {
		t.Fatalf("read after a rename over the name: %q", s)
	}

	// a hard link to another file replaces the name
	write(target, "# Plan\n")
	write(other, "# other\n")
	chatAfterOpen = func() {
		tmp := filepath.Join(root, "docs", "tmp-link.md")
		if err := os.Link(other, tmp); err != nil {
			t.Skipf("hard links unavailable: %v", err)
		}
		_ = os.Rename(tmp, target)
	}
	if s, err := readChatMarkdown(root, "docs/plan.md"); err == nil {
		t.Fatalf("read after a hard link replaced the name: %q", s)
	}

	// a hard link to the SAME file is the same file: still readable
	write(target, "# Plan\n")
	chatAfterOpen = func() {
		tmp := filepath.Join(root, "docs", "same-link.md")
		if err := os.Link(target, tmp); err == nil {
			_ = os.Rename(tmp, target)
		}
	}
	if s, err := readChatMarkdown(root, "docs/plan.md"); err != nil || s != "# Plan\n" {
		t.Fatalf("an unchanged file reads: %q %v", s, err)
	}
}
