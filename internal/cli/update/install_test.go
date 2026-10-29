package update

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

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
