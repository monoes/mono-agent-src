package budget

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/aitask"
)

// unenforceable are the runtimes that can't take a spend cap (chat's
// turnBudget refuses a capped turn on them too), so no capped task runs on them.
var unenforceable = map[string]bool{"kilo": true}

// Enforceable says whether a task on runtime can be held to a cap.
func Enforceable(runtime string) bool { return !unenforceable[runtime] }

// taskSpent is what one reservation's task has spent, over the lead and every
// worker of its conversation.
const taskSpent = `COALESCE((SELECT SUM(s.spent_usd) FROM ai_budget_spend s WHERE s.task_id = r.task_id), 0)`

// taskUsage is what a task counts for against a cap: while it runs, its whole
// cap (or its spend, if that is more); once it is over, what it spent. A task
// that ended without any cost reported keeps counting for its whole cap,
// since nothing says it spent less.
const taskUsage = `CASE WHEN r.released = 1 THEN ` + taskSpent + ` ELSE MAX(r.reserved_usd, ` + taskSpent + `) END`

// Admission is what a task asks the ledger for when it starts.
type Admission struct {
	TaskID         string
	ConversationID string
	Kind           string
	// Role is the agent role the cap should also hold for; "" for none.
	Role    string
	Runtime string
	// Requested is the cap the task was submitted with.
	Requested float64
}

type scopeCond struct {
	sql  string
	args []any
	cap  float64
	// role is set for a role cap, which counts what agents of the role spent.
	role string
}

// Admit reserves the task's cap and returns the cap its turn runs under: the
// requested one, lowered to the lowest cap that applies. It refuses a runtime
// that can't hold a cap, a cap of 0 (no spend), a missing cap (no_budget), and
// a task that doesn't fit in what is left under some cap (budget_exceeded).
// The check and the reservation are one INSERT ... SELECT, so two tasks
// racing for the last of a cap can't both win. Admitting a task twice returns
// its first reservation.
func (l *Ledger) Admit(ctx context.Context, in Admission) (float64, error) {
	if !Enforceable(in.Runtime) {
		return 0, &aitask.Error{Code: CodeUnenforceable,
			Message: fmt.Sprintf("%s can't enforce or report a spend cap, so a task with a budget does not run on it", in.Runtime)}
	}
	var held float64
	err := l.DB.QueryRowContext(ctx, `SELECT reserved_usd FROM ai_budget_reservations WHERE task_id = ?`, in.TaskID).Scan(&held)
	if err == nil {
		return held, nil
	}
	l.syncOpen(ctx) // what finished tasks spent frees their reservations
	eff, err := l.Resolve(in.Kind, in.ConversationID, in.Role)
	if err != nil {
		return 0, err
	}
	if !eff.Set {
		return 0, &aitask.Error{Code: aitask.CodeNoBudget,
			Message: "no spend cap is set (no global budget and no coder budget), so the task does not run; " + aitask.BudgetHint}
	}
	if eff.USD <= 0 || in.Requested <= 0 {
		return 0, &aitask.Error{Code: aitask.CodeNoBudget,
			Message: fmt.Sprintf("the budget that applies (%s) is %v USD: 0 means no spend, not unlimited, so the task does not run; %s", eff.Source, minPositive(eff.USD, in.Requested), aitask.BudgetHint)}
	}
	reserve := min(in.Requested, eff.USD)

	where, wargs, err := l.fitWhere(in, reserve)
	if err != nil {
		return 0, err
	}
	args := append([]any{in.TaskID, in.ConversationID, in.Kind, reserve, l.now()}, wargs...)
	q := `INSERT INTO ai_budget_reservations (task_id, conversation_id, kind, reserved_usd, released, created_at)
		SELECT ?, ?, ?, ?, 0, ?`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, " AND ")
	}
	res, err := l.DB.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, &aitask.Error{Code: CodeExceeded,
			Message: fmt.Sprintf("the task doesn't fit under its spend caps: %v USD would go over a cap (the lowest that applies is %s, %v USD) with what other tasks have spent or hold. Wait for them, or the operator raises a cap in Settings", reserve, eff.Source, eff.USD)}
	}
	_, _ = l.DB.ExecContext(ctx, `UPDATE ai_tasks SET budget_usd = ? WHERE id = ? AND budget_usd > ?`, reserve, in.TaskID, reserve)
	return reserve, nil
}

// fitWhere are the conditions, and their arguments, for the task to hold total
// USD at once without any cap it falls in going over (not counting the task's
// own earlier hold).
func (l *Ledger) fitWhere(in Admission, total float64) ([]string, []any, error) {
	conds, err := l.conditions(in)
	if err != nil {
		return nil, nil, err
	}
	var where []string
	var args []any
	for _, c := range conds {
		if c.role != "" {
			where = append(where, `(SELECT COALESCE(SUM(spent_usd), 0) FROM ai_budget_spend WHERE role = ? AND task_id != ?) + ? <= ?`)
			args = append(args, c.role, in.TaskID, total, c.cap+epsilon)
			continue
		}
		where = append(where, fmt.Sprintf(`(SELECT COALESCE(SUM(%s), 0) FROM ai_budget_reservations r WHERE r.task_id != ? %s) + ? <= ?`, taskUsage, c.sql))
		args = append(args, append(append([]any{in.TaskID}, c.args...), total, c.cap+epsilon)...)
	}
	return where, args, nil
}

func minPositive(a, b float64) float64 {
	if a <= 0 {
		return a
	}
	return b
}

// conditions are the caps the task must fit under, with the SQL that picks
// out the reservations counted against each: the global one (all tasks), the
// kind's, the conversation's, and the operator's coder budget (all tasks,
// per task: it bounds one task, so it needs no sum).
func (l *Ledger) conditions(in Admission) ([]scopeCond, error) {
	var out []scopeCond
	add := func(scope, key, sql string, args ...any) error {
		v, ok, err := l.get(scope, key)
		if err != nil {
			return err
		}
		if ok {
			out = append(out, scopeCond{sql: sql, args: args, cap: v})
		}
		return nil
	}
	if err := add(ScopeGlobal, "", ""); err != nil {
		return nil, err
	}
	if err := add(ScopeKind, in.Kind, `AND r.kind = ?`, in.Kind); err != nil {
		return nil, err
	}
	if in.Role != "" {
		v, ok, err := l.get(ScopeRole, in.Role)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, scopeCond{role: in.Role, cap: v})
		}
	}
	if in.ConversationID != "" {
		if err := add(ScopeConversation, in.ConversationID, `AND r.conversation_id = ?`, in.ConversationID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// syncOpen brings the spend of every task that still holds a reservation up
// to date, and releases those that are over.
func (l *Ledger) syncOpen(ctx context.Context) {
	rows, err := l.DB.QueryContext(ctx, `SELECT r.task_id, r.conversation_id, COALESCE(t.status, '') FROM ai_budget_reservations r
		LEFT JOIN ai_tasks t ON t.id = r.task_id WHERE r.released = 0`)
	if err != nil {
		return
	}
	type open struct{ id, conv, status string }
	var all []open
	for rows.Next() {
		var o open
		if rows.Scan(&o.id, &o.conv, &o.status) == nil {
			all = append(all, o)
		}
	}
	rows.Close()
	for _, o := range all {
		if err := l.Sync(ctx, o.id, o.conv); err == nil && (o.status == "" || aitask.Terminal(o.status) || o.status == aitask.StatusNeedsUser) {
			_ = l.Release(ctx, o.id)
		}
	}
}

// usagePayload is the part of a usage.updated event the ledger reads.
type usagePayload struct {
	AgentID string   `json:"agentId"`
	CostUSD *float64 `json:"costUsd"`
}

// Sync records what task id's conversation has spent, per agent, from the
// cost its turns journaled. A usage.updated snapshot is the agent's running
// total within its turn, so the turn's figure is the largest it reported,
// and the agent's spend is the sum over its turns. A worker counts as part of
// the task however it was spawned. Missing cost is not zero: an agent that
// never reported one has no row.
func (l *Ledger) Sync(ctx context.Context, taskID, conversationID string) error {
	rows, err := l.DB.QueryContext(ctx, `SELECT turn_id, payload FROM ai_chat_events WHERE conversation_id = ? AND type = 'usage.updated'`, conversationID)
	if err != nil {
		return err
	}
	perTurn := map[[2]string]float64{}
	for rows.Next() {
		var turn, raw string
		var p usagePayload
		if rows.Scan(&turn, &raw) != nil || json.Unmarshal([]byte(raw), &p) != nil || p.CostUSD == nil || *p.CostUSD < 0 {
			continue
		}
		k := [2]string{turn, p.AgentID}
		if cur, seen := perTurn[k]; !seen || *p.CostUSD > cur {
			perTurn[k] = *p.CostUSD
		}
	}
	rows.Close()
	perAgent := map[string]float64{}
	for k, v := range perTurn {
		perAgent[k[1]] += v
	}
	if len(perTurn) == 0 {
		// A conversation that journaled nothing never ran a turn (cancelled or
		// refused before it began): it spent nothing, and says so.
		var n int
		if l.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM ai_chat_events WHERE conversation_id = ?`, conversationID).Scan(&n) == nil && n == 0 {
			perAgent[""] = 0
		}
	}
	// A conversation's spend is charged once, to the first task that reserved
	// it (every task has a conversation of its own today); any other task
	// sharing it is charged nothing, so the conversation's total stays right.
	var first string
	if l.DB.QueryRowContext(ctx, `SELECT task_id FROM ai_budget_reservations WHERE conversation_id = ? ORDER BY created_at, rowid LIMIT 1`, conversationID).Scan(&first) == nil && first != taskID {
		perAgent = map[string]float64{"": 0}
	}
	roles := map[string]string{}
	wr, err := l.DB.QueryContext(ctx, `SELECT agent_id, role FROM ai_chat_org_workers WHERE conversation_id = ?`, conversationID)
	if err == nil {
		for wr.Next() {
			var a, r string
			if wr.Scan(&a, &r) == nil {
				roles[a] = r
			}
		}
		wr.Close()
	}
	for agent, spent := range perAgent {
		if _, err := l.DB.ExecContext(ctx, `INSERT INTO ai_budget_spend (task_id, agent_id, role, spent_usd, updated_at) VALUES (?,?,?,?,?)
			ON CONFLICT(task_id, agent_id) DO UPDATE SET spent_usd = excluded.spent_usd, role = excluded.role, updated_at = excluded.updated_at`,
			taskID, agent, roles[agent], spent, l.now()); err != nil {
			return err
		}
	}
	return nil
}

// Release ends task id's hold on its reserved cap, so only what it spent
// counts. A task whose conversation reported no cost keeps its reservation:
// nothing says it spent less than its cap.
func (l *Ledger) Release(ctx context.Context, taskID string) error {
	_, err := l.DB.ExecContext(ctx, `UPDATE ai_budget_reservations SET released = 1
		WHERE task_id = ? AND EXISTS (SELECT 1 FROM ai_budget_spend s WHERE s.task_id = ?)`, taskID, taskID)
	return err
}

// ReleaseUnspent ends task id's hold when its start failed: the turn never ran,
// so unlike Release it frees the reservation even though no spend was recorded.
func (l *Ledger) ReleaseUnspent(ctx context.Context, taskID string) error {
	_, err := l.DB.ExecContext(ctx, `UPDATE ai_budget_reservations SET released = 1
		WHERE task_id = ? AND NOT EXISTS (SELECT 1 FROM ai_budget_spend s WHERE s.task_id = ?)`, taskID, taskID)
	return err
}

// Settle is what a task's end calls: sync its spend, then release it.
func (l *Ledger) Settle(ctx context.Context, taskID, conversationID string) error {
	if err := l.Sync(ctx, taskID, conversationID); err != nil {
		return err
	}
	return l.Release(ctx, taskID)
}

// Admitter is the aitask.Service hook that admits a task at start: it reserves
// the task's cap and returns the cap its turn runs under.
func (l *Ledger) Admitter() func(context.Context, aitask.Task) (float64, error) {
	return func(ctx context.Context, t aitask.Task) (float64, error) {
		return l.Admit(ctx, Admission{TaskID: t.ID, ConversationID: t.ConversationID, Kind: t.Kind,
			Runtime: t.Runtime, Requested: t.BudgetUSD})
	}
}
