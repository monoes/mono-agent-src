//go:build doomsday

package main

import (
	"context"
	"database/sql"
	"path/filepath"

	chatcli "github.com/monoes/mono-agent/internal/cli/chat"
	doomsdaycli "github.com/monoes/mono-agent/internal/cli/doomsday"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/doomsday/stop"
	"github.com/monoes/mono-agent/internal/doomsday/store"
	"github.com/monoes/mono-agent/internal/dynorg"
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
		return doomsdayWiring(cfg, db.DB).Engine()
	}
	doomsdayChat = func(cfg *globalConfig, d *chatcli.Deps) {
		// A conductor's merges go through the open run's gate and journal; the resolver reads the run on every
		// merge, so a run that starts or stops after the turn began is seen.
		d.DoomsdayMerge = func(db *sql.DB) func(ctx context.Context) (*dynorg.DoomsdayMerge, error) {
			w := doomsdayWiring(cfg, db)
			g := &doomsdaycli.HostGate{Store: w.Store, Profile: w.Profile, RunDir: w.RunDir, Journal: doomsdaycli.MergeJournal(db, w.Store)}
			return g.MergeFor
		}
		// Reconcile of worktrees resolves begun merges first and keeps the worktrees of unresolved ones (note
		// 13.3 section 5.1). A database that cannot be opened keeps every turn's worktree.
		dynorg.SetReconcileGuard(func(ctx context.Context, cwd string, active func(string) bool) func(string) bool {
			db, err := initDB(cfg)
			if err != nil {
				return func(string) bool { return true }
			}
			defer db.Close()
			r := doomsdayWiring(cfg, db.DB).MergeRecovery()
			return r.GuardTurns(ctx, cwd, active)
		})
	}
}

// doomsdayWiring is the one construction of the engine's seams: the store over the profile's vault key, the run
// directory next to the database, the stop package's containment probe and kernel, real token revocation.
func doomsdayWiring(cfg *globalConfig, db *sql.DB) doomsdaycli.Wiring {
	keys := doomsdaycli.VaultKeyer{DB: db, Profile: cfg.ProfileID}
	runDir := filepath.Join(filepath.Dir(expandPath(cfg.DBPath)), "doomsday", cfg.ProfileID)
	return doomsdaycli.Wiring{
		Store:   store.New(db, keys),
		Profile: cfg.ProfileID,
		RunDir:  stop.RunDir{Dir: runDir},
		Probe:   stop.Probe,
		Kernel:  doomsdayKernel(),
		Tokens:  doomsdaycli.CoderTokens{DB: db, Profile: cfg.ProfileID, Host: coderhost.NewStore(db)},
		DB:      db,
	}
}
