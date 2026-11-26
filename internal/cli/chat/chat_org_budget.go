package chat

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/monoes/mono-agent/internal/aitask"
	"github.com/monoes/mono-agent/internal/aitask/budget"
	"github.com/monoes/mono-agent/internal/dynorg"
	"github.com/monoes/mono-agent/internal/monomind"
)

// taskWorkers bounds the dynamic-org workers of a turn in a conversation that
// belongs to an AI task. The turn's cap (opts.BudgetUSD, what admission
// granted) is shared: the workers get a pool of up to half of it, or the
// operator's org budget when that is lower, and the lead keeps the rest, so
// together they can't spend more than the task has left. An org budget of 0
// is not "no limit" here. Each worker run reserves part of the pool in the
// ledger before it starts (atomically, so workers running together can't
// reserve more than the pool) and settles what it spent when it ends.
//
// ok is false for a conversation with no open task: nothing changes. A task
// turn with no cap to share, or one whose reason to refuse is returned, runs
// without workers.
func taskWorkers(db *sql.DB, cfg *Deps, conversationID, turnID string, settings coderSettings, opts *monomind.ExecOptions, inner dynorg.ExecFunc) (pool float64, exec dynorg.ExecFunc, ok bool, refuse string) {
	t, found, err := (&aitask.Store{DB: db, Profile: cfg.ProfileID()}).OpenForConversation(conversationID)
	if err != nil || !found {
		return 0, nil, false, ""
	}
	if opts.BudgetUSD <= 0 {
		return 0, nil, true, "no spend cap applies to this task's turn, so it works without workers"
	}
	pool = opts.BudgetUSD / 2
	if settings.OrgBudgetUSD > 0 && settings.OrgBudgetUSD < pool {
		pool = settings.OrgBudgetUSD
	}
	opts.BudgetUSD -= pool
	l := taskLedger(db)
	key := t.ID + "/" + turnID
	exec = func(ctx context.Context, o monomind.ExecOptions, on func(monomind.Event)) (*monomind.TurnResult, error) {
		if !budget.Enforceable(o.Runtime) {
			return nil, fmt.Errorf("%s can't hold a spend cap, so it can't run as a worker of an AI task", o.Runtime)
		}
		id, granted, err := l.ReservePool(ctx, key, pool, o.BudgetUSD)
		if err != nil {
			return nil, err
		}
		o.BudgetUSD = granted
		res, err := inner(ctx, o, on)
		// A run that reported no cost counts for everything it reserved.
		spent := granted
		if res != nil && res.HasCostUSD {
			spent = dynorg.RunCostUSD(ctx, *res)
		}
		_ = l.SettlePool(context.WithoutCancel(ctx), id, spent)
		return res, err
	}
	return pool, exec, true, ""
}
