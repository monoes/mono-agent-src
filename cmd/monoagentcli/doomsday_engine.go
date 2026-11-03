//go:build doomsday

package main

import (
	"path/filepath"

	doomsdaycli "github.com/monoes/mono-agent/internal/cli/doomsday"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/doomsday/stop"
	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// This file is compiled only with -tags doomsday. A default build never wires
// an engine, so the mode cannot start there (doomsday_gate_test.go holds it to
// that). Containment is the stop package's probe: where it fails (no cgroup v2
// delegation, macOS, Windows) the engine refuses to start.
const doomsdayBuilt = true

func init() {
	doomsdayEngine = func(cfg *globalConfig) doomsdaycli.Engine {
		db, err := initDB(cfg)
		if err != nil {
			return doomsdaycli.Off{} // fails closed: nothing starts
		}
		keys := doomsdaycli.VaultKeyer{DB: db.DB, Profile: cfg.ProfileID}
		runDir := filepath.Join(filepath.Dir(expandPath(cfg.DBPath)), "doomsday", cfg.ProfileID)
		return doomsdaycli.Wiring{
			Store:   store.New(db.DB, keys),
			Profile: cfg.ProfileID,
			RunDir:  stop.RunDir{Dir: runDir},
			Probe:   stop.Probe,
			Kernel:  doomsdayKernel(),
			Tokens:  doomsdaycli.CoderTokens{DB: db.DB, Profile: cfg.ProfileID, Host: coderhost.NewStore(db.DB)},
		}.Engine()
	}
}
