//go:build linux || darwin

package stop

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"golang.org/x/sys/unix"
)

func TestReadFDNamesListsNamesWithoutLookingAtEntries(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"0", "1", "7", "abc"} {
		mustNoErr(t, os.WriteFile(filepath.Join(dir, n), nil, 0o600))
	}
	mustNoErr(t, os.Symlink(filepath.Join(dir, "gone"), filepath.Join(dir, "9"))) // an entry whose target cannot be looked at
	got, err := readFDNames(dir)
	mustNoErr(t, err)
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"0", "1", "7", "9", "abc"}) {
		t.Fatalf("names: %v", got)
	}
	if _, err := readFDNames(filepath.Join(dir, "nope")); err == nil {
		t.Fatal("a missing directory listed")
	}
}

// A kqueue-like descriptor (not a file) among the descriptors does not fail the scan; a closed one is skipped; any other
// error fails closed.
func TestScanFDsViaSkipsNonFilesAndClosedDescriptorsAndFailsClosedOnTheRest(t *testing.T) {
	a, b := FileID{Dev: 1, Ino: 10}, FileID{Dev: 1, Ino: 11}
	probe := func(stat func(int) (FileID, bool, error)) fdProbe {
		return fdProbe{
			Names: func() ([]string, error) { return []string{"0", "3", "4", "5", "x", "6"}, nil },
			Stat:  stat,
			Path:  func(fd int) string { return map[int]string{3: "/db/a", 4: "/db/b"}[fd] },
		}
	}
	got, err := scanFDsVia(probe(func(fd int) (FileID, bool, error) {
		switch fd {
		case 0:
			return FileID{}, false, nil // a kqueue, a socket, a pipe
		case 3:
			return a, true, nil
		case 4:
			return b, true, nil
		case 5:
			return FileID{}, false, unix.EBADF // closed since the listing
		}
		return FileID{}, false, nil
	}))
	mustNoErr(t, err)
	want := []fdEntry{{fd: 3, link: "/db/a", id: a}, {fd: 4, link: "/db/b", id: b}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("entries: %+v", got)
	}
	if _, err := scanFDsVia(probe(func(fd int) (FileID, bool, error) {
		if fd == 4 {
			return FileID{}, false, unix.EIO
		}
		return FileID{}, false, nil
	})); err == nil {
		t.Fatal("a descriptor that cannot be identified did not fail the scan")
	}
	if _, err := scanFDsVia(fdProbe{Names: func() ([]string, error) { return nil, errors.New("no /dev/fd") }}); err == nil {
		t.Fatal("a listing failure did not fail the scan")
	}
}
