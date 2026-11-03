//go:build doomsday

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	doomsdaycli "github.com/monoes/mono-agent/internal/cli/doomsday"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/doomsday/ops"
	"github.com/monoes/mono-agent/internal/mcp"
)

// The strategy layer's wiring (behind -tags doomsday, like the rest of the mode): the read-only verbs' reader, the engine
// loop that ticks the strategy host, and the boss tools' caller. A default build links none of it, and with no run open
// every part is a no-op.

// strategyReaderFor is the reader of `doomsday strategy status|epochs|directions`. A database that cannot be opened is the
// layer being off.
func strategyReaderFor(cfg *globalConfig) doomsdaycli.StrategyReader {
	db, err := initDB(cfg)
	if err != nil {
		return nil
	}
	w := doomsdayWiring(cfg, db.DB)
	return w.StrategyReader(db.DB, w.Witness.Keys)
}

// startStrategyLoop runs the strategy host's tick in the background of the coder host process, until the process is told to
// stop. The coder host has no hook for its context, so the loop watches the same signals the host does; the process ending
// ends it either way (it holds no state a restart cannot rebuild from the database).
func startStrategyLoop(cfg *globalConfig, db *sql.DB) {
	w := doomsdayWiring(cfg, db)
	loop := &doomsdaycli.StrategyLoop{
		Build: func(ctx context.Context) (*doomsdaycli.StrategyHost, error) {
			return w.StrategyHost(ctx, doomsdaycli.StrategyExtras{})
		},
		Logf: func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) },
	}
	go func() {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		loop.Run(ctx)
	}()
}

// bossStrategyOp is mcp.BossStrategyCaller. Like the other boss tools it asks first whether the profile has a running run
// and constructs nothing (no pinned connection, no key read) when it has none.
func bossStrategyOp(ctx context.Context, db *sql.DB, dbPath, profile string, session int64, op string, args json.RawMessage) (mcp.BossStrategyResult, error) {
	var running int
	if err := db.QueryRow(`SELECT COUNT(*) FROM doomsday_runs WHERE profile = ? AND state = 'running'`, profile).Scan(&running); err != nil || running == 0 {
		return mcp.BossStrategyResult{}, fmt.Errorf("%w: %v", coderhost.ErrDoomsdayOff, ops.ErrNoCharter)
	}
	res, err := doomsdayWiring(&globalConfig{DBPath: dbPath, ProfileID: profile}, db).BossStrategy(ctx, session, op, args)
	if err != nil {
		return mcp.BossStrategyResult{}, mapStrategyErr(err)
	}
	return mcp.BossStrategyResult{Op: res.Op, Accepted: res.Accepted, DirectionID: res.DirectionID, Outcome: string(res.Outcome),
		MatchedID: res.MatchedID, State: string(res.State)}, nil
}

// mapStrategyErr puts the ops package's refusals on coderhost's, which the MCP layer turns into its codes.
func mapStrategyErr(err error) error {
	switch {
	case errors.Is(err, ops.ErrNoCharter):
		return fmt.Errorf("%w: %v", coderhost.ErrDoomsdayOff, err)
	case errors.Is(err, ops.ErrCharterStale), errors.Is(err, ops.ErrCallerMismatch), errors.Is(err, ops.ErrNotEnabled), errors.Is(err, ops.ErrNotAllowlisted),
		errors.Is(err, ops.ErrNotCallable), errors.Is(err, ops.ErrNotYourStrat):
		return fmt.Errorf("%w: %v", coderhost.ErrNotYours, err)
	case errors.Is(err, ops.ErrStrategyCap):
		return fmt.Errorf("%w: %v", coderhost.ErrLimit, err)
	case errors.Is(err, ops.ErrBadArguments):
		return fmt.Errorf("%w: %v", coderhost.ErrInvalid, err)
	}
	return err
}
