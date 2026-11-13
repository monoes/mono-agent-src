package main

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
)

func TestOpenModeDoctorExtra(t *testing.T) {
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, active, why := openModeDoctorExtra(db.DB, "P"); active || why != "" { // unmigrated: no run, no error
		t.Errorf("unmigrated: %v %q", active, why)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	if steps, active, why := openModeDoctorExtra(db.DB, "P"); active || why != "" || (runtime.GOOS == "linux" && len(steps) == 0) {
		t.Errorf("fresh: %v %q %d steps", active, why, len(steps))
	}
	if _, err := db.DB.Exec(`INSERT INTO doomsday_runs (profile, generation, charter_id, state, mac) VALUES ('P', 1, 'c', 'running', x'00')`); err != nil {
		t.Fatal(err)
	}
	if _, active, _ := openModeDoctorExtra(db.DB, "P"); !active {
		t.Error("a running run of the profile is not seen")
	}
	if _, active, _ := openModeDoctorExtra(db.DB, "Q"); active {
		t.Error("another profile's run is seen")
	}
}
