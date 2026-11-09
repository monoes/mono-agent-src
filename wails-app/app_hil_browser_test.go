//go:build !windows

package main

import (
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ReadHILScreenshot reads only images in ~/.monoagent/browser/hil/.
func TestReadHILScreenshotIsConfinedToTheBrowserApprovalsFolder(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	dir := filepath.Join(home, ".monoagent", "browser", "hil")
	shot := mustWrite(t, filepath.Join(dir, "4f1c.png"), pngBytes)
	secret := mustWrite(t, filepath.Join(home, ".monoagent", "vault", "secret.png"), pngBytes)
	mustWrite(t, filepath.Join(dir, "notes.png"), []byte("not an image"))
	if err := os.Symlink(secret, filepath.Join(dir, "link.png")); err != nil {
		t.Fatal(err)
	}
	big := mustWrite(t, filepath.Join(dir, "big.png"), pngBytes)
	os.Truncate(big, maxWorkspaceImageBytes+1)

	a := &App{}
	for _, p := range []string{shot, "4f1c.png"} {
		if got, err := a.ReadHILScreenshot(p); err != nil || !strings.HasPrefix(got, "data:image/png;base64,") {
			t.Errorf("%s: %.40q, %v", p, got, err)
		}
	}
	for name, p := range map[string]string{
		"another folder":  secret,
		"dot-dot":         filepath.Join(dir, "..", "..", "vault", "secret.png"),
		"relative escape": "../../vault/secret.png",
		"symlink out":     filepath.Join(dir, "link.png"),
		"not an image":    filepath.Join(dir, "notes.png"),
		"too big":         big,
		"folder":          dir,
	} {
		if got, err := a.ReadHILScreenshot(p); err == nil {
			t.Errorf("%s (%s) was served: %.40q", name, p, got)
		}
	}
}
