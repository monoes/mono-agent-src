package doctor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/procbind"
	"github.com/monoes/mono-agent/internal/storage"
)

// A fresh database stores nothing, so the DEFAULT applies: binding is on where this machine can run the service, and off (with
// the reason, and runs starting unbound) where it cannot; no run is active, no host lock file exists and none is created.
// Revert proof: restoring the old off-by-default turns the capable case red.
func TestOpenModeFactsOnAFreshProfile(t *testing.T) {
	procbind.ForTestSetIssuance(t, true)
	for name, c := range map[string]struct {
		ready   error
		wantOn  bool
		unbound string
	}{"capable": {nil, true, ""}, "unsupported": {errors.New("old kernel"), false, "old kernel"}} {
		procbind.ForTestSetReady(t, c.ready)
		dbPath := filepath.Join(t.TempDir(), "x.db")
		db, err := storage.NewDatabase(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		check := func(stage string) {
			f := openModeFacts(db.DB, "P", dbPath, nil)
			if f.BindingOn != c.wantOn || !strings.Contains(f.BindingUnbound, c.unbound) || (c.unbound == "") != (f.BindingUnbound == "") || f.BindingExplicitOnBroken != "" ||
				f.BindingErr != "" || f.ActiveRun || f.ActiveErr != "" || f.HostRunning {
				t.Errorf("%s/%s: %+v", name, stage, f)
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
		db.Close()
	}
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
