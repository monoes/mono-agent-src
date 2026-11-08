//go:build darwin

package procbind

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A profile id whose plain directory name would not fit the socket address under a deep root gets its hashed name, and the
// service still listens and is dialed there.
func TestDarwinALongProfileUnderADeepRootUsesTheHashedLeaf(t *testing.T) {
	root := filepath.Join(shortDir(t), strings.Repeat("d", 18))
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("p", 60)
	o := SocketOptions{Root: root, Profile: long}
	if got := leafFor(o); got != hashedLeaf(long) {
		t.Fatalf("leaf %q, want the hashed one", got)
	}
	if got := leafFor(SocketOptions{Root: shortDir(t), Profile: "p1"}); got != "p1" {
		t.Errorf("a short id under a short root got %q", got)
	}
	l, err := ListenSecure(o)
	if err != nil {
		t.Fatalf("ListenSecure: %v", err)
	}
	defer l.Close()
	if _, err := os.Lstat(filepath.Join(root, runDirName, hashedLeaf(long), sockName)); err != nil {
		t.Fatalf("the socket is not under the hashed leaf: %v", err)
	}
	go func() {
		if c, err := l.AcceptUnix(); err == nil {
			_ = c.Close()
		}
	}()
	c, err := DialSecure(o)
	if err != nil {
		t.Fatalf("DialSecure: %v", err)
	}
	_ = c.Close()
}
