package update

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// On Windows ownedByUser cannot say anything, so it must not decide: a leftover named exactly like a kept previous binary
// is swept (the running image could not be removed at Commit), and a file of any other name is still left alone.
func TestSweepPreviousWindowsSweepsStrictNameWithoutOwnerCheck(t *testing.T) {
	oldGOOS, oldUID := sweepGOOS, uidOf
	t.Cleanup(func() { sweepGOOS, uidOf = oldGOOS, oldUID })
	sweepGOOS = "windows"
	uidOf = func() int { return os.Getuid() + 1 } // ownership cannot be established: nothing is "ours"
	dir := t.TempDir()
	old := time.Now().Add(-48 * time.Hour).Unix()
	mk := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	strict := mk(fmt.Sprintf(".monoagentcli-previous-%d-123", old))
	badRandom := mk(fmt.Sprintf(".monoagentcli-previous-%d-x", old))
	other := mk("monoagentcli.bak")
	fresh := mk(fmt.Sprintf(".monoagentcli-previous-%d-456", time.Now().Add(-time.Minute).Unix()))
	sweepPrevious(dir)
	if _, err := os.Stat(strict); !os.IsNotExist(err) {
		t.Fatalf("strictly named old previous binary was not swept on windows: %v", err)
	}
	for _, p := range []string{badRandom, other, fresh} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s must be kept: %v", p, err)
		}
	}
	// off Windows the owner check still applies
	sweepGOOS = "linux"
	again := mk(fmt.Sprintf(".monoagentcli-previous-%d-789", old))
	sweepPrevious(dir)
	if _, err := os.Stat(again); err != nil {
		t.Fatalf("a file of another owner was swept on a POSIX platform: %v", err)
	}
}

func TestInstallBinaryReplacesInPlace(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "monoagentcli")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := installBinary([]byte("new"), target); err != nil {
		t.Fatalf("installBinary: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "new" {
		t.Fatalf("target = %q, %v; want new", got, err)
	}
	fi, _ := os.Stat(target)
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0o100 == 0 { // Windows has no execute bit
		t.Fatalf("target not executable: %v", fi.Mode())
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "monoagentcli" {
			t.Fatalf("leftover file %q (temp or backup not cleaned up)", e.Name())
		}
	}
}

func TestInstallBinaryTempLivesNextToTarget(t *testing.T) {
	t.Parallel()
	// A read-only directory makes CreateTemp in the target dir fail; the
	// error must come from there (not from os.TempDir), and the target
	// must be untouched.
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not enforce directory permission bits")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "monoagentcli")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o755)
	err := installBinary([]byte("new"), target)
	if err == nil || !strings.Contains(err.Error(), "temp file") {
		t.Fatalf("err = %v; want a temp file error from the target dir", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "old" {
		t.Fatalf("target changed to %q", got)
	}
}
