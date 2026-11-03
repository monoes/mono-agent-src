//go:build doomsday

package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	chatcli "github.com/monoes/mono-agent/internal/cli/chat"
	doomsdaycli "github.com/monoes/mono-agent/internal/cli/doomsday"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/doomsday/ops"
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
			g := &doomsdaycli.HostGate{Store: w.Chained(), Fresh: w.Fresh(), Profile: w.Profile, RunDir: w.RunDir, Kernel: w.Kernel, Journal: doomsdaycli.MergeJournal(db, w.Store)}
			return g.MergeFor
		}
		// The coder host gets the open run's dispatch gate: tickets for spawns, token mints and turn releases, the run
		// stamp, and the bound-mode requirement. With no open run the gate does nothing, so the host is what it was. A
		// gate whose launcher hooks cannot be installed still stands (the ticket is then taken around Start).
		chatcli.HostBuilt = func(st *coderhost.Store, h *coderhost.Host, db *sql.DB) {
			w := doomsdayWiring(cfg, db)
			g := &doomsdaycli.HostGate{Store: w.Chained(), Fresh: w.Fresh(), Profile: w.Profile, RunDir: w.RunDir, Kernel: w.Kernel, Journal: doomsdaycli.MergeJournal(db, w.Store),
				BindingOn: func() (bool, error) { return chatcli.PeercredBindingOn(db) },
				InstallHooks: func(st *coderhost.Store, h *coderhost.Host, hk doomsdaycli.Hooks) error {
					return chatcli.InstallRunHooks(st, h, chatcli.RunHooks{RunStamp: hk.RunStamp, BeforeRelease: hk.BeforeRelease, Place: hk.Place})
				}}
			if err := g.Install(st, h); err != nil {
				fmt.Fprintf(os.Stderr, "coder host: the open-ended mode's launcher hooks could not be installed (turns are ticketed around their start instead): %v\n", err)
			}
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
		// The entry refuses a charter whose 13.4 constraints would only ever deny (ops.EntryCheck, under the host's own spend bounds).
		Store:   store.New(db, keys).WithEntryCheck(ops.EntryCheck(coderhost.SpendBounds())),
		Profile: cfg.ProfileID,
		Binding: func(context.Context) (bool, error) { return chatcli.PeercredBindingOn(db) },
		RunDir:  stop.RunDir{Dir: runDir},
		Probe:   stop.Probe,
		Kernel:  doomsdayKernel(),
		Host:    stop.SystemHost(),
		Tokens:  doomsdaycli.CoderTokens{DB: db, Profile: cfg.ProfileID, Host: coderhost.NewStore(db)},
		DB:      db,
		// The witness of note 13.5: the pinned connection on the database file, the vault-derived MAC keys, and the OS
		// secret store of the anchor (a run on a machine with none is `anchor-bound: no` and says so).
		Witness: &doomsdaycli.WitnessSetup{DBPath: expandPath(cfg.DBPath), Keys: keys, Secrets: stop.OSSecretStore{},
			Warn: func(m string) { fmt.Fprintln(os.Stderr, "doomsday: "+m) }},
	}
}
