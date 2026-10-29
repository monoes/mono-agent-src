package tasks

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
)

// MaxOpenChildren is how many open (draft, running or paused) child sessions one session holds: the
// coder host's default max_children for a spawning session.
const MaxOpenChildren = 4

// ChildSharePct is the share of what a parent has left that a child gets when it names no budget:
// the coder host's default mission share_pct.
const ChildSharePct = 50

const openChildStates = `('draft','running','paused')`

// carveChild gives a new child its budget out of the parent's: what the parent has left (its budget,
// less what it spent, less what its other open children were given). A child that named no budget
// gets ChildSharePct of that, so one child cannot take it all; one that named more than is left is
// refused with the numbers. A parent without a budget hands out any amount. The number of open
// children an agent starts below one parent is bounded by MaxOpenChildren, whatever the budget (the
// operator is not held to it).
func (s *Store) carveChild(ctx context.Context, x dbx, par Session, budget float64, actor Actor) (float64, error) {
	var kids int
	var given float64
	if err := x.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(budget_usd), 0) FROM task_sessions WHERE parent_session_id = ? AND archived_at = '' AND state IN `+openChildStates, par.ID).Scan(&kids, &given); err != nil {
		return 0, fmt.Errorf("tasks: counting the children of session #%d: %w", par.ID, err)
	}
	if actor.Kind == Agent && kids >= MaxOpenChildren {
		return 0, limit("session #%d already has %d open child sessions (the most is %d)", par.ID, kids, MaxOpenChildren)
	}
	if par.BudgetUSD <= 0 {
		return budget, nil
	}
	left := par.BudgetUSD - par.SpentUSD - given
	detail := fmt.Sprintf("$%.2f left of $%.2f: $%.2f spent, $%.2f given to its other open children", max(left, 0), par.BudgetUSD, par.SpentUSD, given)
	switch {
	case left < 0.01:
		return 0, limit("session #%d has no budget left to give a child (%s)", par.ID, detail)
	case budget == 0:
		return math.Floor(left*ChildSharePct) / 100, nil
	case budget > left+1e-9:
		return 0, limit("budget $%.2f is above what the parent session #%d has left (%s)", budget, par.ID, detail)
	}
	return budget, nil
}

// clampChildren brings the open children of session id (and theirs) within its limits after the
// operator lowered them: down only, never up, each change an event of the child. The children share
// what the parent has left, in id order.
func (s *Store) clampChildren(ctx context.Context, x dbx, id int64, actor Actor) error {
	var level string
	var maxTasks, maxDepth int
	var budget, spent float64
	if err := x.QueryRowContext(ctx, `SELECT access_level, max_tasks, max_depth, budget_usd, spent_usd FROM task_sessions WHERE id = ?`, id).Scan(&level, &maxTasks, &maxDepth, &budget, &spent); err != nil {
		return fmt.Errorf("tasks: reading session #%d: %w", id, err)
	}
	rows, err := x.QueryContext(ctx, `SELECT id, access_level, max_tasks, max_depth, budget_usd FROM task_sessions WHERE parent_session_id = ? AND archived_at = '' AND state IN `+openChildStates+` ORDER BY id`, id)
	if err != nil {
		return fmt.Errorf("tasks: reading the children of session #%d: %w", id, err)
	}
	type kid struct {
		id                int64
		level             string
		maxTasks, maxDeep int
		budget            float64
	}
	var kids []kid
	for rows.Next() {
		var k kid
		if err := rows.Scan(&k.id, &k.level, &k.maxTasks, &k.maxDeep, &k.budget); err != nil {
			rows.Close()
			return fmt.Errorf("tasks: reading the children of session #%d: %w", id, err)
		}
		kids = append(kids, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("tasks: reading the children of session #%d: %w", id, err)
	}
	left := budget - spent
	for _, k := range kids {
		var changed []string
		if slices.Index(AccessLevels, k.level) > slices.Index(AccessLevels, level) {
			k.level = level
			changed = append(changed, "access_level "+level)
		}
		if k.maxTasks > maxTasks {
			k.maxTasks = maxTasks
			changed = append(changed, fmt.Sprintf("max_tasks %d", maxTasks))
		}
		if k.maxDeep > maxDepth {
			k.maxDeep = maxDepth
			changed = append(changed, fmt.Sprintf("max_depth %d", maxDepth))
		}
		if budget > 0 {
			if cap := math.Floor(max(left, 0.01)*100) / 100; k.budget <= 0 || k.budget > cap {
				k.budget = cap
				changed = append(changed, fmt.Sprintf("budget_usd %.2f", cap))
			}
			left -= k.budget
		}
		if len(changed) == 0 {
			continue
		}
		if _, err := x.ExecContext(ctx, `UPDATE task_sessions SET access_level = ?, max_tasks = ?, max_depth = ?, budget_usd = ? WHERE id = ?`, k.level, k.maxTasks, k.maxDeep, k.budget, k.id); err != nil {
			return fmt.Errorf("tasks: clamping session #%d: %w", k.id, err)
		}
		if err := s.sessionEvent(ctx, x, k.id, actor, "clamped", fmt.Sprintf("to session #%d's lowered limits: %s", id, strings.Join(changed, ", "))); err != nil {
			return err
		}
		if err := s.clampChildren(ctx, x, k.id, actor); err != nil {
			return err
		}
	}
	return nil
}
