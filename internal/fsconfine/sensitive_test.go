package fsconfine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/monoes/mono-agent/internal/testhome"
)

func TestSensitivePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix layout")
	}
	home := t.TempDir()
	testhome.SetHome(t, home)
	mk := func(rel string) string {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for _, rel := range []string{".ssh/id_ed25519", ".aws/credentials", ".gnupg/key", ".monoagent/keyring-passphrase",
		".monoagent/monoagent.db", ".config/google-chrome/Default/Cookies", ".mozilla/firefox/p/cookies.sqlite", ".netrc"} {
		if !SensitivePath(mk(rel)) {
			t.Errorf("%s not sensitive", rel)
		}
	}
	// The folder the app generates documents into is not a secret, the rest of scratch still is.
	for _, rel := range []string{".monoagent/scratch/documents/cv-1.html", ".monoagent/scratch/documents/chrome/Default/Preferences"} {
		if SensitivePath(mk(rel)) {
			t.Errorf("%s wrongly sensitive: generated documents must be registrable", rel)
		}
	}
	if !SensitivePath(mk(".monoagent/scratch/checks/x")) {
		t.Error(".monoagent/scratch/checks/x is not sensitive")
	}
	if err := os.Symlink(filepath.Join(home, ".monoagent", "monoagent.db"), filepath.Join(home, ".monoagent", "scratch", "documents", "db-link.html")); err == nil {
		if !SensitivePath(filepath.Join(home, ".monoagent", "scratch", "documents", "db-link.html")) {
			t.Error("a link from the documents scratch folder to the database is not sensitive")
		}
	}
	for _, rel := range []string{"notes/a.txt", ".monoagent/profiles/p1/folder/a.png", "Documents/cv.pdf"} {
		if SensitivePath(mk(rel)) {
			t.Errorf("%s wrongly sensitive", rel)
		}
	}
	link := filepath.Join(home, "innocent.txt")
	if err := os.Symlink(filepath.Join(home, ".ssh", "id_ed25519"), link); err != nil {
		t.Fatal(err)
	}
	if !SensitivePath(link) {
		t.Error("symlink to a key not sensitive")
	}
	for _, p := range []string{"/etc/shadow", "/etc/sudoers", "/root/.bashrc", "/proc/self/environ", "/proc/self/maps", "/proc/self/fd/0"} {
		if !SensitivePath(p) {
			t.Errorf("%s not sensitive", p)
		}
	}
}

func TestGuardRefusesSystemCredentialFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix layout")
	}
	guardHome(t)
	ctx := GuardHiddenHome(context.Background(), Guard{})
	for _, p := range []string{"/etc/shadow", "/etc/sudoers", "/root/x", "/proc/self/maps", "/proc/self/fd/1"} {
		if _, err := Path(ctx, p); !errors.Is(err, ErrUnapprovedPath) {
			t.Errorf("read %s: %v", p, err)
		}
	}
}
