package main

import (
	"context"
	"database/sql"
	"path/filepath"

	"github.com/spf13/cobra"

	chatcli "github.com/monoes/mono-agent/internal/cli/chat"
	doomsdaycli "github.com/monoes/mono-agent/internal/cli/doomsday"
	"github.com/monoes/mono-agent/internal/cli/opgate"
)

// doomsdayEngine is the store/stop adapter of the open-ended mode. doomsday_engine.go sets it in init() in every
// build; it is called each time a verb asks for the engine (Deps.Engine), so the database is opened and the engine built
// then, not at startup. The mode is off until a charter starts a run: with no run the engine refuses or reads nothing,
// and where containment is unavailable (no cgroup v2 delegation, macOS, Windows) it refuses to start. Nothing reads a
// setting or an environment variable to turn the mode on. Nil (only in a test that clears it) means doomsdaycli.Off.
var doomsdayEngine func(*globalConfig) doomsdaycli.Engine

// doomsdayReadEngine is set in the same init: the engine of the control room's read verbs (no witness sequencer
// just to read). Nil falls back to doomsdayEngine.
var doomsdayReadEngine func(*globalConfig) doomsdaycli.Engine

// doomsdayStrategyReader is set in the same init: the read-only reader of the `doomsday strategy` verbs. Nil leaves
// them printing "off".
var doomsdayStrategyReader func(*globalConfig) doomsdaycli.StrategyReader

// doomsdayChat is set in the same init: it hands the chat commands the resolver of the open run's merge gate
// (chatcli.Deps.DoomsdayMerge) and installs the worktree reconcile guard. Nil leaves merges and reconcile as they were.
var doomsdayChat func(cfg *globalConfig, d *chatcli.Deps)

// doomsdayExtra is set in the same init: it adds the groups beyond the base verbs (the governor's read-only
// status). Nil leaves the group as it is.
var doomsdayExtra func(cfg *globalConfig, grp *cobra.Command, d doomsdaycli.Deps)

// doomsdayFirstTurnHook is nil in every shipped build. Only the e2estub file sets it, to make the first-turn queue fail on demand.
var doomsdayFirstTurnHook func() error

func newDoomsdayCmd(cfg *globalConfig) *cobra.Command {
	// Both questions are the turn-aware ones every operator-only verb asks: requireOperator, and opgate.Refusal
	// (agent tokens, markers, a coder session's turn). doomsday_gate_test.go and the rows of
	// operatorOnlyRows hold them.
	gate := cfg.requireOperator
	deps := doomsdaycli.Deps{
		Engine: func() doomsdaycli.Engine {
			if doomsdayEngine == nil {
				return nil
			}
			return doomsdayEngine(cfg)
		},
		ReadEngine: func() doomsdaycli.Engine {
			if doomsdayReadEngine == nil {
				return nil
			}
			return doomsdayReadEngine(cfg)
		},
		RequireOperator: gate,
		OperatorRefusal: opgate.Refusal,
		StdinIsTerminal: stdinIsTerminal,
		JSONOutput:      func() bool { return cfg.JSONOutput },
		StrategyReader: func() doomsdaycli.StrategyReader {
			if doomsdayStrategyReader == nil {
				return nil
			}
			return doomsdayStrategyReader(cfg)
		},
	}
	// `status` shows a first turn the host refused after the start succeeded (#1521): read-only, from the host's own queue.
	deps.FirstTurn = func(ctx context.Context) (doomsdaycli.FirstTurnState, error) {
		db, err := initDB(cfg)
		if err != nil {
			return doomsdaycli.FirstTurnState{}, err
		}
		defer db.Close()
		return doomsdaycli.Wiring{DB: db.DB, Profile: cfg.ProfileID}.FirstTurnOf(ctx)
	}
	deps.ChatStart = func() doomsdaycli.ChatStarter {
		return doomsdaycli.DBChatStart{
			Open: func() (*sql.DB, func(), error) {
				db, err := initDB(cfg)
				if err != nil {
					return nil, nil, err
				}
				return db.DB, func() { db.Close() }, nil
			},
			Profile: func() string { return cfg.ProfileID },
			// One start per profile at a time, across processes: a lock file next to the database.
			LockPath: func() string {
				return filepath.Join(filepath.Dir(expandPath(cfg.DBPath)), "doomsday-chat-start-"+cfg.ProfileID+".lock")
			},
			// The first turn is the chat commands' own queue (the user's gate included); this only hands it the run.
			Queue: func(_ context.Context, conv string, in doomsdaycli.FirstTurn) error {
				if doomsdayFirstTurnHook != nil { // nil in every shipped build (see doomsday_e2e_stub.go)
					if err := doomsdayFirstTurnHook(); err != nil {
						return err
					}
				}
				d := chatDeps(cfg)
				_, err := chatcli.QueueBossFirstTurn(&d, conv, chatcli.BossFirstTurn{RunID: in.RunID, CharterHash: in.CharterHash, Goal: in.Goal})
				return err
			},
		}
	}
	grp := doomsdaycli.NewCommand(deps)
	grp.AddCommand(doomsdaycli.BossCommand(doomsdaycli.BossDeps{
		Open: func() (*sql.DB, func(), error) {
			db, err := initDB(cfg)
			if err != nil {
				return nil, nil, err
			}
			return db.DB, func() { db.Close() }, nil
		},
		Profile:      func() string { return cfg.ProfileID },
		OperatorGate: deps.OperatorGate,
		JSON:         deps.JSONOutput,
	}))
	if doomsdayExtra != nil {
		doomsdayExtra(cfg, grp, deps)
	}
	return grp
}
