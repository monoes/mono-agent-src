//go:build doomsday

package main

import (
	doomsdaycli "github.com/monoes/mono-agent/internal/cli/doomsday"
	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// This file is compiled only with -tags doomsday. A default build never wires
// an engine, so the mode cannot start there (doomsday_gate_test.go holds it to
// that). The containment probe and Stop come from the stop package when it is
// merged; until they are set the engine refuses to start and cannot stop.
const doomsdayBuilt = true

func init() {
	doomsdayEngine = func(cfg *globalConfig) doomsdaycli.Engine {
		db, err := initDB(cfg)
		if err != nil {
			return doomsdaycli.Off{} // fails closed: nothing starts
		}
		keys := doomsdaycli.VaultKeyer{DB: db.DB, Profile: cfg.ProfileID}
		return &doomsdaycli.StoreEngine{Store: store.New(db.DB, keys), Profile: cfg.ProfileID}
	}
}
