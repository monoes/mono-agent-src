package budget

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/monoes/mono-agent/internal/aitask"
)

func noBudget(format string, a ...any) error {
	return &aitask.Error{Code: aitask.CodeNoBudget, Message: fmt.Sprintf(format, a...) + "; " + aitask.BudgetHint}
}

// TaskRoom is what a started task has to run another turn on.
type TaskRoom struct {
	// CapUSD is the lower of the task's cap and the cap that applies now.
	CapUSD   float64
	SpentUSD float64
	// Source names the scope of the cap that applies now.
	Source string
	// Set is false when no cap applies, so no further turn runs.
	Set bool
}

// Left is what the next turn may spend; 0 when nothing is left.
func (r TaskRoom) Left() float64 {
	if left := r.CapUSD - r.SpentUSD; r.Set && left > epsilon {
		return left
	}
	return 0
}

// Room reads the room of task in.TaskID, reserving and writing nothing: the
// figures AdmitTurn grants a follow-up turn from, and what a chat shows
// beforehand.
func (l *Ledger) Room(ctx context.Context, in Admission) (TaskRoom, error) {
	var taskCap float64
	err := l.DB.QueryRowContext(ctx, `SELECT budget_usd FROM ai_tasks WHERE id = ?`, in.TaskID).Scan(&taskCap)
	if err == sql.ErrNoRows {
		return TaskRoom{}, &aitask.Error{Code: aitask.CodeNotFound, Message: "no AI task " + in.TaskID}
	}
	if err != nil {
		return TaskRoom{}, err
	}
	var out TaskRoom
	if out.SpentUSD, err = l.spent(ctx, in.TaskID, in.ConversationID); err != nil {
		return TaskRoom{}, err
	}
	eff, err := l.Resolve(in.Kind, in.ConversationID, in.Role)
	if err != nil || !eff.Set {
		return out, err
	}
	out.CapUSD, out.Source, out.Set = min(taskCap, eff.USD), eff.Source, true
	return out, nil
}

// AdmitTurn admits a follow-up turn of a task that has already started. Its
// first turn was admitted by Admit; this one is admitted again, against what
// is left: the task's cap (the one it started under, or a lower one that
// applies now) minus everything its conversation has spent, bounded by
// turnCap (the turn's own --budget-usd; 0 for none). It returns the cap the
// turn runs under. The task's reservation is re-opened at spent + that cap in
// one conditional UPDATE, so it can't take what another task holds, and
// Settle at the turn's end closes it again. Nothing left is no_budget, and
// what earlier turns spent is never forgiven: a released reservation counts
// its spend.
func (l *Ledger) AdmitTurn(ctx context.Context, in Admission, turnCap float64) (float64, error) {
	var taskCap float64
	err := l.DB.QueryRowContext(ctx, `SELECT budget_usd FROM ai_tasks WHERE id = ?`, in.TaskID).Scan(&taskCap)
	if err == sql.ErrNoRows {
		return 0, &aitask.Error{Code: aitask.CodeNotFound, Message: "no AI task " + in.TaskID}
	}
	if err != nil {
		return 0, err
	}
	var held float64
	if l.DB.QueryRowContext(ctx, `SELECT reserved_usd FROM ai_budget_reservations WHERE task_id = ?`, in.TaskID).Scan(&held) == sql.ErrNoRows {
		// Never admitted (a task from before budgets): admit it as a first turn.
		in.Requested = taskCap
		if turnCap > 0 && turnCap < in.Requested {
			in.Requested = turnCap
		}
		return l.Admit(ctx, in)
	}
	if err := l.Sync(ctx, in.TaskID, in.ConversationID); err != nil {
		return 0, err
	}
	room, err := l.Room(ctx, in)
	if err != nil {
		return 0, err
	}
	if !room.Set {
		return 0, noBudget("no spend cap is set, so the task's turn does not run")
	}
	spent, grant := room.SpentUSD, room.CapUSD-room.SpentUSD
	if turnCap > 0 {
		grant = min(grant, turnCap)
	}
	if grant <= epsilon {
		return 0, noBudget("the task has spent $%.4f of its $%.4f cap (%s), so nothing is left for another turn", spent, room.CapUSD, room.Source)
	}
	total := spent + grant
	where, wargs, err := l.fitWhere(in, total)
	if err != nil {
		return 0, err
	}
	q := `UPDATE ai_budget_reservations SET reserved_usd = ?, released = 0 WHERE task_id = ?`
	args := []any{total, in.TaskID}
	for _, w := range where {
		q += ` AND ` + w
	}
	res, err := l.DB.ExecContext(ctx, q, append(args, wargs...)...)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, &aitask.Error{Code: CodeExceeded,
			Message: fmt.Sprintf("another turn of this task doesn't fit under the spend caps: %v USD would go over a cap with what other tasks have spent or hold", grant)}
	}
	return grant, nil
}
