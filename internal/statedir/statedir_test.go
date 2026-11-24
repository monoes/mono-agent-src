package statedir

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func perm(t *testing.T, p string) fs.FileMode {
	t.Helper()
	info, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func skipOnWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix modes do not apply on Windows")
	}
}

func TestMkdirAllAndWriteFileArePrivate(t *testing.T) {
	skipOnWindows(t)
	dir := filepath.Join(t.TempDir(), ".monoagent")
	file := filepath.Join(dir, "sub", "state.json")
	if err := WriteFile(file, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]fs.FileMode{dir: DirPerm, filepath.Dir(file): DirPerm, file: FilePerm} {
		if got := perm(t, p); got != want {
			t.Errorf("%s: mode %v, want %v", p, got, want)
		}
	}
}

func TestWriteFileTightensAnExistingFile(t *testing.T) {
	skipOnWindows(t)
	file := filepath.Join(t.TempDir(), "login-tab.json")
	if err := os.WriteFile(file, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(file, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if got := perm(t, file); got != FilePerm {
		t.Fatalf("mode %v, want %v", got, FilePerm)
	}
	if b, _ := os.ReadFile(file); string(b) != "new" {
		t.Fatalf("content %q", b)
	}
}

// The mode is fixed before the content is written: a file the owner cannot write (0444) is made 0600 first, so
// the write goes into a private file. Writing first would have failed here.
func TestWriteFileFixesTheModeBeforeWriting(t *testing.T) {
	skipOnWindows(t)
	if os.Geteuid() == 0 {
		t.Skip("root writes to any file")
	}
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("old"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(file, []byte("new")); err != nil {
		t.Fatalf("WriteFile onto a 0444 file: %v", err)
	}
	if got := perm(t, file); got != FilePerm {
		t.Fatalf("mode %v, want %v", got, FilePerm)
	}
}

func TestFindExposedAndRepair(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()
	dir := filepath.Join(root, ".monoagent")
	if err := os.MkdirAll(filepath.Join(dir, "captures"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil { // beat the umask
		t.Fatal(err)
	}
	write := func(p string, mode fs.FileMode) {
		t.Helper()
		if err := os.WriteFile(p, nil, mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
	}
	db := filepath.Join(dir, "monoagent.db")
	write(db, 0o644)
	write(db+"-wal", 0o644)
	write(filepath.Join(dir, "release-state.json"), 0o644)
	write(filepath.Join(dir, "helper.sh"), 0o755)
	write(filepath.Join(dir, "extension.token"), 0o600)
	nested := filepath.Join(dir, "captures", "page.html")
	write(nested, 0o644)
	outside := filepath.Join(root, "elsewhere.db")
	write(outside, 0o640)
	if err := os.Symlink(outside, filepath.Join(dir, "link.db")); err != nil {
		t.Fatal(err)
	}

	extra := append(DBFiles(db), DBFiles(outside)...)
	found, err := FindExposed(dir, extra...)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range found {
		got[e.Path] = true
	}
	want := []string{dir, filepath.Join(dir, "captures"), nested, db, db + "-wal", filepath.Join(dir, "release-state.json"), filepath.Join(dir, "helper.sh"), outside}
	if len(found) != len(want) {
		t.Fatalf("found %d items, want %d: %+v", len(found), len(want), found)
	}
	for _, p := range want {
		if !got[p] {
			t.Errorf("not reported: %s", p)
		}
	}

	fixed, err := Repair(dir, extra...)
	if err != nil {
		t.Fatal(err)
	}
	if len(fixed) != len(want) {
		t.Fatalf("repaired %d, want %d", len(fixed), len(want))
	}
	for p, wantMode := range map[string]fs.FileMode{
		dir: 0o700, db: 0o600, db + "-wal": 0o600, filepath.Join(dir, "release-state.json"): 0o600,
		filepath.Join(dir, "helper.sh"):       0o700, // the owner keeps execute
		filepath.Join(dir, "extension.token"): 0o600, outside: 0o600,
		nested: 0o600, filepath.Join(dir, "captures"): 0o700,
	} {
		if got := perm(t, p); got != wantMode {
			t.Errorf("%s: mode %v, want %v", p, got, wantMode)
		}
	}
	if again, err := FindExposed(dir, extra...); err != nil || len(again) != 0 {
		t.Fatalf("after repair: %+v, %v", again, err)
	}
}

func TestFindExposedMissingDir(t *testing.T) {
	found, err := FindExposed(filepath.Join(t.TempDir(), "nope"), DBFiles("")...)
	if err != nil || len(found) != 0 {
		t.Fatalf("%+v, %v", found, err)
	}
}

func TestFindExposedWalksPrivateSubfoldersOnly(t *testing.T) {
	skipOnWindows(t)
	dir := filepath.Join(t.TempDir(), ".monoagent")
	mk := func(p string) {
		t.Helper()
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p string, mode fs.FileMode) {
		t.Helper()
		mk(filepath.Dir(p))
		if err := os.WriteFile(p, nil, mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
	}
	mk(dir)
	deepWorkflow := filepath.Join(dir, "workflows", "team", "wf.json")
	write(deepWorkflow, 0o644)
	draftScript := filepath.Join(dir, "recording-drafts", "d1", "scripts", "s.js")
	write(draftScript, 0o644)
	logFile := filepath.Join(dir, "logs", "daemon.log")
	write(logFile, 0o644)
	// Installed programs are not walked: the 0700 state folder covers them.
	registry := filepath.Join(dir, "automations", "x", "automation.json")
	write(registry, 0o644)
	nodeTree := filepath.Join(dir, "node", "bin", "node")
	write(nodeTree, 0o755)

	found, err := FindExposed(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range found {
		got[e.Path] = true
	}
	for _, p := range []string{deepWorkflow, draftScript, logFile} {
		if !got[p] {
			t.Errorf("not reported: %s", p)
		}
	}
	for _, p := range []string{registry, nodeTree} {
		if got[p] {
			t.Errorf("reported a path outside the private subfolders: %s", p)
		}
	}
	if _, err := Repair(dir); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{deepWorkflow, draftScript, logFile} {
		if m := perm(t, p); m != FilePerm {
			t.Errorf("%s: mode %v after repair, want %v", p, m, FilePerm)
		}
	}
	if m := perm(t, nodeTree); m != 0o755 {
		t.Errorf("node tree touched: %v", m)
	}
}

func TestFindExposedWalkIsBounded(t *testing.T) {
	skipOnWindows(t)
	old := maxWalk
	maxWalk = 3
	t.Cleanup(func() { maxWalk = old })
	dir := filepath.Join(t.TempDir(), ".monoagent")
	caps := filepath.Join(dir, "captures")
	if err := os.MkdirAll(caps, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		p := filepath.Join(caps, fmt.Sprintf("p%02d.html", i))
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	found, err := FindExposed(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != maxWalk-1 { // the captures folder itself is 0700
		t.Fatalf("found %d, want %d: %+v", len(found), maxWalk-1, found)
	}
}
