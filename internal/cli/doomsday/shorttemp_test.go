package doomsday

import (
	"os"
	"runtime"
	"testing"
	"time"
)

// shortTempDir is a private directory with a SHORT path, for tests that bind a unix socket (macOS limits a socket path to
// 104 bytes and t.TempDir() under $TMPDIR is longer).
func shortTempDir(t *testing.T) string {
	t.Helper()
	base := os.TempDir() // never a fixed directory: on Windows C:\tmp may exist outside the test's scratch area
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat("/tmp"); err == nil && fi.IsDir() {
			base = "/tmp"
		}
	}
	d, err := os.MkdirTemp(base, "ma")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		var rerr error
		for i := 0; i < 30; i++ { // Windows refuses to remove a file something still has open: retry briefly
			if rerr = os.RemoveAll(d); rerr == nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Logf("scratch directory left behind: %v", rerr)
	})
	return d
}
