//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/account/accounttest"
)

// openHandlesOn counts this process's open descriptors on the database file (and its -wal/-shm companions).
func openHandlesOn(t *testing.T, db string) int {
	t.Helper()
	fds, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skip("no /proc/self/fd")
	}
	n := 0
	for _, fd := range fds {
		if to, err := os.Readlink(filepath.Join("/proc/self/fd", fd.Name())); err == nil && strings.HasPrefix(to, db) {
			n++
		}
	}
	return n
}

// Every doomsday verb closes the database its engine or strategy reader opened, once it has run (on Windows an open
// handle keeps the file from being removed or replaced, so the test home could not be deleted).
func TestDoomsdayVerbsCloseTheDatabaseTheyOpened(t *testing.T) {
	accounttest.Install(t, accounttest.SignedIn)
	for _, verb := range [][]string{{"stop"}, {"strategy", "status"}} {
		t.Run(strings.Join(verb, "_"), func(t *testing.T) {
			home := freshHome(t)
			db := filepath.Join(home, "db", "monoagent.db")
			// A run that came back from the command line, whatever its exit code: the handle must be gone.
			runMain(t, append([]string{"--db-path", db, "--profile", "default", "doomsday"}, verb...)...)
			if _, err := os.Stat(db); err != nil {
				t.Skipf("the verb opened no database here: %v", err)
			}
			if n := openHandlesOn(t, db); n != 0 {
				t.Fatalf("doomsday %v left %d handle(s) open on the database", verb, n)
			}
		})
	}
}
