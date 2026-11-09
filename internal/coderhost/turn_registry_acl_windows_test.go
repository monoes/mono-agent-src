//go:build windows

package coderhost

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// icacls changes the real DACL of a test directory (no window: it is run without a console of its own).
func icacls(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("icacls", args...).CombinedOutput(); err != nil {
		t.Fatalf("icacls %v: %v\n%s", args, err, out)
	}
}

// The directory the registry creates is private to this user by the real ACL, is accepted, and works.
func TestWindowsRegistryDirIsCreatedPrivateAndWorks(t *testing.T) {
	needAncestry(t)
	dir := registryDirOf(t)
	unregister, err := RegisterHostDB(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatalf("RegisterHostDB: %v", err)
	}
	defer unregister()
	if err := checkRegistryDir(dir); err != nil {
		t.Fatalf("the directory it created was refused: %v", err)
	}
	dbs, err := RegisteredHostDBs()
	if err != nil || len(dbs) != 1 {
		t.Fatalf("RegisteredHostDBs = %v, %v; want the one entry just written", dbs, err)
	}
}

// Another user's write access to the directory or to an entry makes it unsafe; a read-only grant does not.
func TestWindowsRegistryRefusesAForeignWriteACE(t *testing.T) {
	needAncestry(t)
	dir := registryDirOf(t)
	unregister, err := RegisterHostDB(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()
	icacls(t, dir, "/grant", "*S-1-1-0:(OI)(CI)(R)")
	if err := checkRegistryDir(dir); err != nil {
		t.Fatalf("a read grant to Everyone made the directory unsafe: %v", err)
	}
	icacls(t, dir, "/grant", "*S-1-5-32-545:(W)")
	err = checkRegistryDir(dir)
	if !errors.Is(err, ErrRegistryUnsafe) {
		t.Fatalf("a write grant to Users: err = %v, want ErrRegistryUnsafe", err)
	}
	if _, err := RegisterHostDB(filepath.Join(t.TempDir(), "b.db")); !errors.Is(err, ErrRegistryUnsafe) {
		t.Errorf("registered into a directory others can write: %v", err)
	}
	if _, err := RegisteredHostDBs(); !errors.Is(err, ErrRegistryUnsafe) {
		t.Errorf("read a directory others can write: %v", err)
	}
}

func TestWindowsRegistryEntryWithAForeignWriteACEIsSkipped(t *testing.T) {
	needAncestry(t)
	dir := registryDirOf(t)
	unregister, err := RegisterHostDB(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()
	files := entryFiles(t, dir)
	if len(files) != 1 {
		t.Fatalf("entries = %v", files)
	}
	icacls(t, filepath.Join(dir, files[0]), "/grant", "*S-1-1-0:(W)")
	dbs, err := RegisteredHostDBs()
	if err != nil || len(dbs) != 0 {
		t.Fatalf("an entry others can write was listed: %v, %v", dbs, err)
	}
}

// A directory somebody else made (inherited ACL of an open parent) is refused, not adopted.
func TestWindowsRegistryRefusesAnOpenDirectoryItDidNotCreate(t *testing.T) {
	needAncestry(t)
	dir := registryDirOf(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	icacls(t, dir, "/grant", "*S-1-1-0:(OI)(CI)(M)")
	if _, err := RegisterHostDB(filepath.Join(t.TempDir(), "a.db")); !errors.Is(err, ErrRegistryUnsafe) {
		t.Fatalf("err = %v, want ErrRegistryUnsafe", err)
	}
}
