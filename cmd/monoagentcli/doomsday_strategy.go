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
	"sync"
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

// strategyLoopStop ends the loop started by startStrategyLoop and waits for it (nil when none runs). The coder host's
// own shutdown (internal/cli/chat/coder_host.go, which this stage may not edit) has no hook after Run returns, so it is
// not called there yet: it is what a hook, or an embedding test, calls before closing the database.
var (
	strategyLoopMu   sync.Mutex
	strategyLoopStop func()
)

// stopStrategyLoop stops the loop and joins it. Safe to call with no loop and more than once.
func stopStrategyLoop() {
	strategyLoopMu.Lock()
	stop := strategyLoopStop
	strategyLoopStop = nil
	strategyLoopMu.Unlock()
	if stop != nil {
		stop()
	}
}

// startStrategyLoop runs the strategy host's tick in the background of the coder host process, until the process is told to
// stop (the same signals the host watches) or stopStrategyLoop is called. The loop is joined by its stop function, and a
// panic in a tick is recovered inside it, so it can neither outlive the database nor take the host down. It holds no state
// a restart cannot rebuild from the database. A second call replaces the first loop (the first is stopped and joined).
func startStrategyLoop(cfg *globalConfig, db *sql.DB) {
	stopStrategyLoop()
	loop := newStrategyLoop(cfg, db)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	stop := loop.Start(ctx)
	strategyLoopMu.Lock()
	strategyLoopStop = func() { stop(); cancel() }
	strategyLoopMu.Unlock()
}

// newStrategyLoop is the loop of startStrategyLoop, not yet started.
func newStrategyLoop(cfg *globalConfig, db *sql.DB) *doomsdaycli.StrategyLoop {
	w := doomsdayWiring(cfg, db)
	return &doomsdaycli.StrategyLoop{
		Build: func(ctx context.Context) (*doomsdaycli.StrategyHost, error) {
			return w.StrategyHost(ctx, doomsdaycli.StrategyExtras{})
		},
		Logf: func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) },
		// The resource governor pumps its queue and reports on this loop; with no running run it does nothing.
		AfterStep: processGovernor(cfg, db).Tick,
	}
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

// bossFinishReady is mcp.BossFinishReadyReader: the boss's read-only reading of whether the run looks ready for a finish.
// It asks first whether the profile has a running run and constructs nothing when it has none; a session that is not the
// run's recorded boss reads nothing. It begins nothing: finish.begin is session_boss_finish_begin, which checks the boss
// itself.
func bossFinishReady(ctx context.Context, db *sql.DB, dbPath, profile string, session int64) (mcp.BossFinishReadyResult, error) {
	var running int
	if err := db.QueryRow(`SELECT COUNT(*) FROM doomsday_runs WHERE profile = ? AND state = 'running'`, profile).Scan(&running); err != nil || running == 0 {
		return mcp.BossFinishReadyResult{}, fmt.Errorf("%w: %v", coderhost.ErrDoomsdayOff, ops.ErrNoCharter)
	}
	v, err := doomsdayWiring(&globalConfig{DBPath: dbPath, ProfileID: profile}, db).FinishReady(ctx, session)
	switch {
	case errors.Is(err, doomsdaycli.ErrReadyNoRun):
		return mcp.BossFinishReadyResult{}, fmt.Errorf("%w: %v", coderhost.ErrDoomsdayOff, err)
	case errors.Is(err, doomsdaycli.ErrReadyNotBoss):
		return mcp.BossFinishReadyResult{}, fmt.Errorf("%w: %v", coderhost.ErrNotYours, err)
	case err != nil:
		return mcp.BossFinishReadyResult{}, err
	}
	return mcp.BossFinishReadyResult{Ready: v.Ready, Reason: v.Reason, Epoch: v.Epoch, JudgmentAgeSeconds: v.JudgmentAgeSeconds, MarkerRecorded: v.Recorded, GaveUp: v.GaveUp}, nil
}
