package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/stop"
	"github.com/monoes/mono-agent/internal/storage"
)

func TestOpenModeDoctorExtra(t *testing.T) {
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := &globalConfig{DBPath: filepath.Join(t.TempDir(), "x.db"), ProfileID: "P"}
	if _, active, why, _ := openModeDoctorRead(cfg, db.DB, "P", -1); active || why != "" { // unmigrated: no run, no error
		t.Errorf("unmigrated: %v %q", active, why)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	if steps, active, why, _ := openModeDoctorRead(cfg, db.DB, "P", -1); active || why != "" || (runtime.GOOS == "linux" && len(steps) == 0) {
		t.Errorf("fresh: %v %q %d steps", active, why, len(steps))
	}
	if _, err := db.DB.Exec(`INSERT INTO doomsday_runs (profile, generation, charter_id, state, mac) VALUES ('P', 1, 'c', 'running', x'00')`); err != nil {
		t.Fatal(err)
	}
	if _, active, _, _ := openModeDoctorRead(cfg, db.DB, "P", -1); !active {
		t.Error("a running run of the profile is not seen")
	}
	if _, active, _, _ := openModeDoctorRead(cfg, db.DB, "Q", -1); active {
		t.Error("another profile's run is seen")
	}
}

// An open run whose process group a past host made is reported by the doctor's read; the host that is the anchor's parent is not.
func TestOpenModeDoctorStrandedUnit(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the process-group unit is the macOS one")
	}
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	cfg := &globalConfig{DBPath: filepath.Join(t.TempDir(), "x.db"), ProfileID: "P"}
	if _, err := db.DB.Exec(`INSERT INTO doomsday_runs (profile, generation, charter_id, state, mac) VALUES ('P', 7, 'c', 'running', x'00')`); err != nil {
		t.Fatal(err)
	}
	// This test process stands for the anchor, its parent for the host that started it.
	rec := stop.UnitRecord{Kind: stop.KindPGroup, Generation: 7, AnchorPID: os.Getpid(), Handle: `{"pgid":1,"sid":1}`}
	if err := os.MkdirAll(doomsdayRunDirPath(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := (stop.RunDir{Dir: doomsdayRunDirPath(cfg)}).WriteUnitFile(rec); err != nil {
		t.Fatal(err)
	}
	if _, _, _, why := openModeDoctorRead(cfg, db.DB, "P", os.Getppid()); why != "" {
		t.Errorf("the anchor's own host reported stranded: %q", why)
	}
	if _, _, _, why := openModeDoctorRead(cfg, db.DB, "P", os.Getppid()+1); why == "" {
		t.Error("an anchor of another host is not reported")
	}
}

// `doctor --profile smoke` leaves cfg.ProfileID the name; the read must find the run's unit under the resolved id it is given (#1703).
func TestOpenModeDoctorStrandedUnitProfileName(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the process-group unit is the macOS one")
	}
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "x.db")
	cfg := &globalConfig{DBPath: dbPath, ProfileID: "smoke"}
	if _, err := db.DB.Exec(`INSERT INTO doomsday_runs (profile, generation, charter_id, state, mac) VALUES ('P-id', 7, 'c', 'running', x'00')`); err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(filepath.Dir(dbPath), "doomsday", "P-id")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	rec := stop.UnitRecord{Kind: stop.KindPGroup, Generation: 7, AnchorPID: os.Getpid(), Handle: `{"pgid":1,"sid":1}`}
	if err := (stop.RunDir{Dir: runDir}).WriteUnitFile(rec); err != nil {
		t.Fatal(err)
	}
	if _, _, _, why := openModeDoctorRead(cfg, db.DB, "P-id", os.Getppid()+1); why == "" {
		t.Error("a stranded unit is not reported when --profile names the profile")
	}
}
