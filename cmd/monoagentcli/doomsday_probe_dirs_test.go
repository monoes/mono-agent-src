package main

import (
	"path/filepath"
	"testing"
)

// The production wiring runs the containment probe's self-tests next to the profile's database and in the profile's run
// directory (a bare stop.Probe would refuse on Windows and macOS, and test the system temp directory elsewhere).
func TestDoomsdayWiringProbesNextToTheProfilesDatabaseAndRunDirectory(t *testing.T) {
	old := probeAt
	t.Cleanup(func() { probeAt = old })
	var gotDB, gotRun string
	probeAt = func(db, run string) error { gotDB, gotRun = db, run; return nil }
	cfg := &globalConfig{DBPath: filepath.Join(t.TempDir(), "mono.db"), ProfileID: "p1"}
	w := doomsdayWiring(cfg, nil)
	if err := w.Probe(); err != nil {
		t.Fatal(err)
	}
	if gotDB != filepath.Dir(cfg.DBPath) || gotRun != doomsdayRunDirPath(cfg) || gotRun == "" || gotDB == "" {
		t.Fatalf("self-tests would run in db=%q run=%q", gotDB, gotRun)
	}
}
