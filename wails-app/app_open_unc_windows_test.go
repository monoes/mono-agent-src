//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A symlink in the chat folder whose target is a UNC share must be refused
// when the user acts on it (never silently followed). Skipped without the
// privilege to create symlinks.
func TestWindowsLinkToUNCShareIsRefused(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(root, "share.md")
	if err := os.Symlink(`\\127.0.0.1\nonexistent-share\x.md`, link); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}
	if g := classifyChatToken(root, "share.md"); g != "inside" {
		t.Fatalf("render-time class %q", g)
	}
	if _, _, err := resolveChatToken(root, "share.md"); err == nil {
		t.Fatal("a link to a UNC share must not resolve")
	}
	if g := resolveChatPathInfo(root, "share.md"); g.Kind != "missing" {
		t.Fatalf("%+v", g)
	}
	if !networkVolume(`\\host\share\x`) || !networkVolume(`\\?\UNC\host\share\x`) {
		t.Fatal("UNC volumes are network volumes")
	}
}
