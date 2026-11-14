//go:build linux || darwin

package procbind

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// A runtime root whose socket path does not fit a socket address is replaced by a short per-user one; a root that fits is
// left alone (an unchanged path keeps every existing install where it is).
func TestShortenRootMovesOnlyARootThatDoesNotFit(t *testing.T) {
	const limit = 103 // a macOS socket address
	fits := "/Users/someone/.monoagent/run"
	if got := shortenRoot(fits, 501, limit); got != fits {
		t.Fatalf("a root that fits moved to %s", got)
	}
	long := "/Users/" + strings.Repeat("n", 40) + "/.monoagent/run"
	got := shortenRoot(long, 501, limit)
	if got != "/tmp/monoagent-501" {
		t.Fatalf("a root that does not fit became %s", got)
	}
	if n := len(got) + 1 + len(runDirName) + 1 + len(hashedLeaf("p")) + 1 + len(sockName); n > limit {
		t.Fatalf("the short root still gives %d bytes", n)
	}
}

// The short root is made and checked like any other: mode 0700, the user's, last component never a symlink.
func TestShortRootIsRefusedWhenItIsASymlinkOrOpenToOthers(t *testing.T) {
	base := shortRoot0(t)
	root := filepath.Join(base, "monoagent-501")
	o := SocketOptions{Root: root, Profile: "p", CreateRoot: true}
	fd, err := openRoot(o)
	if err != nil {
		t.Fatalf("a missing short root is made: %v", err)
	}
	unix.Close(fd)
	if fi, err := os.Lstat(root); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("short root: %v %v", fi, err)
	}
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := openRoot(o); err == nil {
		t.Fatal("a short root open to others was accepted")
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "monoagent-777")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if _, err := openRoot(SocketOptions{Root: link, Profile: "p", CreateRoot: true}); err == nil {
		t.Fatal("a short root that is a symlink was accepted")
	}
}

func shortRoot0(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "sr")
	if err != nil {
		t.Skip("no /tmp")
	}
	t.Cleanup(func() {
		os.Remove(filepath.Join(d, "monoagent-777"))
		os.Remove(filepath.Join(d, "monoagent-501"))
		os.Remove(d)
	})
	return d
}
