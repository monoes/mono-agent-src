package main

import (
	"testing"

	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/storage"
)

// The capture commands' tests live in internal/cli/capture; these are the wiring test and the
// helpers other commands' tests share.

// TestCaptureCommandIsRegistered guards the wiring: a command that exists
// but is never added to the root tree is invisible to every caller.
func TestCaptureCommandIsRegistered(t *testing.T) {
	root := newRootCmd()
	for _, path := range [][]string{{"capture"}, {"capture", "page"}, {"capture", "list"}, {"capture", "classify"}, {"capture", "export"}, {"capture", "import"}, {"capture", "task"}} {
		sub, _, err := root.Find(path)
		if err != nil || sub == nil || sub.Name() != path[len(path)-1] {
			t.Fatalf("root.Find(%v) = %v, %v", path, sub, err)
		}
	}
}

// withJevDB and enableJev are the capture classify tests' helpers the people classify tests
// share.
func withJevDB(t *testing.T, dbPath string, fn func(db *storage.Database)) {
	t.Helper()
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fn(db)
}

func enableJev(t *testing.T, dbPath, profile string, s jevconf.Surface) {
	t.Helper()
	withJevDB(t, dbPath, func(db *storage.Database) {
		if err := jevconf.SetEnabled(db.DB, profile, s, true); err != nil {
			t.Fatal(err)
		}
	})
}
