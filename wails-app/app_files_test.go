package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestOpenFileCommand mirrors TestRevealFolderCommand's pattern for
// revealFolderCommand: verifies the per-GOOS command selection used to hand
// a file off to the OS's default application, without actually launching one.
func TestOpenFileCommand(t *testing.T) {
	const path = "/some/vault/documents/doc-001.pdf"
	cases := []struct {
		goos     string
		wantName string
		wantArgs []string
	}{
		{"darwin", "open", []string{path}},
		{"windows", "cmd", []string{"/c", "start", "", path}},
		{"linux", "xdg-open", []string{path}},
		// Other unix-likes fall back to xdg-open too.
		{"freebsd", "xdg-open", []string{path}},
	}
	for _, tc := range cases {
		t.Run(tc.goos, func(t *testing.T) {
			name, args := openFileCommand(tc.goos, path)
			if name != tc.wantName {
				t.Fatalf("openFileCommand(%q, ...) name = %q, want %q", tc.goos, name, tc.wantName)
			}
			if len(args) != len(tc.wantArgs) {
				t.Fatalf("openFileCommand(%q, ...) args = %v, want %v", tc.goos, args, tc.wantArgs)
			}
			for i := range args {
				if args[i] != tc.wantArgs[i] {
					t.Fatalf("openFileCommand(%q, ...) args = %v, want %v", tc.goos, args, tc.wantArgs)
				}
			}
		})
	}
}

func TestConfineOpenPath(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	write := func(dir, name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	ok := write(root, "doc.pdf")
	if _, err := confineOpenPath(ok, []string{root}); err != nil {
		t.Fatalf("file inside root refused: %v", err)
	}
	if _, err := confineOpenPath(write(outside, "doc.pdf"), []string{root}); err == nil {
		t.Fatal("file outside every root was allowed")
	}
	link := filepath.Join(root, "link.pdf")
	if err := os.Symlink(write(outside, "secret.pdf"), link); err == nil {
		if _, err := confineOpenPath(link, []string{root}); err == nil {
			t.Fatal("symlink escaping the root was allowed")
		}
	}
	for _, name := range []string{"run.exe", "run.BAT", "x.lnk", "x.desktop", "x.sh", "x.ps1", "x.msi", "x.cmd"} {
		if _, err := confineOpenPath(write(root, name), []string{root}); err == nil {
			t.Errorf("launchable file %s was allowed", name)
		}
	}
	if _, err := confineOpenPath(root, []string{root}); err == nil {
		t.Fatal("a directory was allowed")
	}
	if _, err := confineOpenPath(ok, nil); err == nil {
		t.Fatal("no roots must allow nothing")
	}
}

func TestOpenPathWithOSRejectsMissingFile(t *testing.T) {
	a := &App{}
	err := a.OpenPathWithOS(filepath.Join(t.TempDir(), "does-not-exist.pdf"))
	if err == nil {
		t.Fatal("expected an error for a nonexistent path, got nil")
	}
}
