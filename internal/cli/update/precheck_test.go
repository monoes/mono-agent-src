package update

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCheckInstallDirWritable(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "monoagentcli")
	if err := checkInstallDirWritable(target); err != nil {
		t.Fatalf("writable dir refused: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("probe left files behind: %v", entries)
	}
}

func TestCheckInstallDirNotWritableNamesTheDir(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("chmod does not restrict this user")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o755) //nolint:errcheck
	err := checkInstallDirWritable(filepath.Join(dir, "monoagentcli"))
	if err == nil {
		t.Fatal("read-only dir accepted")
	}
	for _, w := range []string{dir, "cannot write to", "sudo", "install.sh", "permission denied"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("%q missing from %v", w, err)
		}
	}
}
