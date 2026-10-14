package main

import (
	"context"
	"database/sql"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/monoes/mono-agent/internal/agentroster"
	agentcli "github.com/monoes/mono-agent/internal/cli/agent"
	"github.com/monoes/mono-agent/internal/cli/orgsvc"
	"github.com/monoes/mono-agent/internal/daemonhb"
	"github.com/monoes/mono-agent/internal/monomind"
)

// The daemon's automatic re-validation of stale roster entries
// (monoes/mono-agent#230): the scheduler and its Busy check. The setting and
// its status are `agent roster auto-revalidate` (internal/cli/agent).

// chatTurnActiveWindow bounds how long an "active" chat turn row counts as
// running without any update: a turn left active by a crash stops blocking
// re-validation after this long.
const chatTurnActiveWindow = 2 * time.Hour

// appBusy reports whether a chat turn, a workflow run or an org run is
// active. Every check is a cheap read: two queries and each profile
// folder's `org serve` heartbeat file.
func appBusy(ctx context.Context, db *sql.DB) (bool, string) {
	var n int
	since := time.Now().Add(-chatTurnActiveWindow).UTC().Format(time.RFC3339)
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM ai_chat_turns WHERE status = 'active' AND updated_at >= ?`, since).Scan(&n); err == nil && n > 0 {
		return true, "chat turn"
	}
	if rows, err := db.QueryContext(ctx, `SELECT COALESCE(pid, 0) FROM workflow_executions WHERE status = 'RUNNING'`); err == nil {
		var pids []int
		for rows.Next() {
			var pid int
			if rows.Scan(&pid) == nil {
				pids = append(pids, pid)
			}
		}
		rows.Close()
		for _, pid := range pids {
			if pid > 0 && (pid == os.Getpid() || daemonhb.ProcessAlive(pid)) {
				return true, "workflow run"
			}
		}
	}
	if roots, err := orgsvc.ProfileRoots(db); err == nil {
		for _, pr := range roots {
			if hb, live := monomind.ReadServeHeartbeat(pr.Root); live && len(hb.Running) > 0 {
				return true, "org run"
			}
		}
	}
	return false, ""
}

// newAutoRevalidator is the daemon's scheduler over the real roster,
// monomind and validation lock.
func newAutoRevalidator(db *sql.DB, logf func(string, ...interface{})) *agentroster.AutoScheduler {
	var scan *monomind.ScanResult // the last Pick's scan, reused by Validate
	return &agentroster.AutoScheduler{
		DB:   db,
		Now:  time.Now,
		Busy: func(ctx context.Context) (bool, string) { return busyOrLocked(ctx, db) },
		Pick: func(ctx context.Context, maxModels int) (*agentroster.AutoPlan, error) {
			s, err := monomind.Scan(ctx)
			if err != nil {
				return nil, err
			}
			scan = s
			return agentcli.AutoRosterPick(ctx, db, s, maxModels)
		},
		Repick: func(ctx context.Context, maxModels int) (*agentroster.AutoPlan, error) {
			return agentcli.AutoRosterPick(ctx, db, scan, maxModels)
		},
		Validate: func(ctx context.Context, p agentroster.AutoPlan) (agentroster.AutoRunResult, error) {
			return runAutoRevalidation(ctx, db, scan, p)
		},
		Lock: func() (func(), bool) {
			release, err := agentcli.LockValidation()
			return release, err == nil
		},
		Logf: func(format string, args ...any) { logf(format, args...) },
	}
}

// runAutoRevalidation tests the plan's models one at a time and stores the
// results as a normal validate run.
func runAutoRevalidation(ctx context.Context, db *sql.DB, scan *monomind.ScanResult, p agentroster.AutoPlan) (agentroster.AutoRunResult, error) {
	var out agentroster.AutoRunResult
	bin, _, err := monomind.Ensure(ctx)
	if err != nil {
		return out, err
	}
	caps, _ := monomind.Capabilities(ctx)
	test := agentroster.AgentTestFunc(caps, bin, agentroster.SandboxModes(scan))
	runID := uuid.NewString()
	if err := agentroster.StartRun(ctx, db, runID, len(p.Targets), time.Now()); err != nil {
		return out, err
	}
	saveCtx := context.WithoutCancel(ctx)
	out.Summary = agentroster.Run(ctx, p.Targets, agentroster.RunOptions{
		RunID: runID, Bin: bin, Timeout: 60 * time.Second, Concurrency: 1, Test: test,
		Save: func(r agentroster.Result) error { return agentroster.Save(saveCtx, db, r) },
	}, out.Add)
	return out, agentroster.FinishRun(saveCtx, db, runID, out.Summary, time.Now())
}

// startAutoRevalidation runs the scheduler until ctx ends; wait blocks
// until it has stopped (a run in progress is cancelled and its finished
// results stored).
func startAutoRevalidation(ctx context.Context, db *sql.DB, logf func(string, ...interface{})) (wait func()) {
	s := newAutoRevalidator(db, logf)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Run(ctx)
	}()
	return func() { <-done }
}
