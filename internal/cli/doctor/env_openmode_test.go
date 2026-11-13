package doctor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/storage"
)

// The macOS gate is the mode's own predicate, not a copy: the facts carry its reason on macOS and nothing elsewhere.
func TestOpenModeFactsFollowTheModesOwnOSGate(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "x.db")
	restore := coderhost.SetOpenModeBindingProbe("darwin", func() error { return nil })
	defer restore()
	if f := openModeFacts(nil, "P", dbPath, nil); f.Unsupported != coderhost.OpenModeMacOSReason {
		t.Errorf("darwin: %q", f.Unsupported)
	}
	restore()
	coderhost.SetOpenModeBindingProbe("linux", func() error { return nil })
	if f := openModeFacts(nil, "P", dbPath, nil); f.Unsupported != "" {
		t.Errorf("linux: %q", f.Unsupported)
	}
}

// Binding is off in a fresh database (off by default), no run is active, no host lock file exists and none is created.
func TestOpenModeFactsOnAFreshProfile(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "x.db")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	check := func(stage string) {
		f := openModeFacts(db.DB, "P", dbPath, nil)
		if f.BindingOn || f.BindingErr != "" || f.ActiveRun || f.ActiveErr != "" || f.HostRunning {
			t.Errorf("%s: %+v", stage, f)
		}
	}
	check("unmigrated")
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	check("migrated")
	openModeFacts(db.DB, "P", dbPath, nil)
	if lock := filepath.Join(coderhost.HostDir(dbPath), "host.lock"); fileExists(lock) {
		t.Errorf("reading created %s", lock)
	}
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
