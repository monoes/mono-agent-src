package main

import (
	"testing"

	doomsdaycli "github.com/monoes/mono-agent/internal/cli/doomsday"
)

// closeEngineDBsAtEnd swaps the three doomsday seams that open the profile's database for a command and never close it
// (the process exit does) for the same construction with the connection closed when the test ends. A connection left open
// keeps the test home from being removed on Windows. The bodies mirror doomsday_engine.go's init and doomsday_strategy.go.
func closeEngineDBsAtEnd(t *testing.T) {
	t.Helper()
	engine, read, strat := doomsdayEngine, doomsdayReadEngine, doomsdayStrategyReader
	t.Cleanup(func() { doomsdayEngine, doomsdayReadEngine, doomsdayStrategyReader = engine, read, strat })
	if engine != nil {
		doomsdayEngine = func(cfg *globalConfig) doomsdaycli.Engine {
			db, err := initDB(cfg)
			if err != nil {
				return engineForOpenFailure(err)
			}
			t.Cleanup(func() { db.Close() })
			w := doomsdayWiring(cfg, db.DB)
			w.Progress = doomsdayProgress
			return w.FinishEngine()
		}
	}
	if read != nil {
		doomsdayReadEngine = func(cfg *globalConfig) doomsdaycli.Engine {
			db, err := openReadDB(cfg)
			if err != nil {
				return engineForOpenFailure(err)
			}
			t.Cleanup(func() { db.Close() })
			return doomsdayWiring(cfg, db).ReadEngine()
		}
	}
	if strat != nil {
		doomsdayStrategyReader = func(cfg *globalConfig) doomsdaycli.StrategyReader {
			db, err := initDB(cfg)
			if err != nil {
				return nil
			}
			t.Cleanup(func() { db.Close() })
			w := doomsdayWiring(cfg, db.DB)
			return w.StrategyReader(db.DB, w.Witness.Keys)
		}
	}
}
