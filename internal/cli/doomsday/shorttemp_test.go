package doomsday

import (
	"os"
	"testing"
)

// shortTempDir is a private directory with a SHORT path, for tests that bind a unix socket (macOS limits a socket path to
// 104 bytes and t.TempDir() under $TMPDIR is longer).
func shortTempDir(t *testing.T) string {
	t.Helper()
	base := ""
	if fi, err := os.Stat("/tmp"); err == nil && fi.IsDir() {
		base = "/tmp"
	}
	d, err := os.MkdirTemp(base, "ma")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}
