package tasks

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
)

// MaxOpenChildren is how many open (draft, running or paused) child sessions one session holds: a
// fence for an agent that starts sessions through the store, equal to the coder host's live-session
// cap per tree. The host's finer, operator-raisable max_children is enforced where it spawns.
const MaxOpenChildren = 12

// ChildSharePct is the share of what a parent has left that a child gets when it names no budget:
// the coder host's default mission share_pct.
const ChildSharePct = 50

const openChildStates = `('draft','running','paused')`

// carveChild gives a new child its budget out of the parent's: what the parent has left (its budget,
// less what it spent, less what its children hold: the budget of an open one, what a closed one spent). A child that named no budget
// gets ChildSharePct of that, so one child cannot take it all; one that named more than is left is
// refused with the numbers. A parent without a budget hands out any amount. The number of open
// children an agent starts below one parent is bounded by MaxOpenChildren, whatever the budget (the
// operator is not held to it).
func (s *Store) carveChild(ctx context.Context, x dbx, par Session, budget float64, actor Actor) (float64, error) {
	var kids int
	if err := x.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_sessions WHERE parent_session_id = ? AND archived_at = '' AND state IN `+openChildStates, par.ID).Scan(&kids); err != nil {
		return 0, fmt.Errorf("tasks: counting the children of session #%d: %w", par.ID, err)
	}
	if actor.Kind == Agent && kids >= MaxOpenChildren {
		return 0, limit("session #%d already has %d open child sessions (the most is %d)", par.ID, kids, MaxOpenChildren)
	}
	if par.BudgetUSD <= 0 {
		return budget, nil
	}
	given, err := heldByChildren(ctx, x, par.ID, 0, `1 = 1`)
	if err != nil {
		return 0, err
	}
	left := par.BudgetUSD - par.SpentUSD - given
	detail := fmt.Sprintf("$%.2f left of $%.2f: $%.2f spent, $%.2f held by its children (open ones their budget, closed ones what they spent)", max(left, 0), par.BudgetUSD, par.SpentUSD, given)
	switch {
	case left < 0.01:
		return 0, limit("session #%d has no budget left to give a child (%s)", par.ID, detail)
	case budget == 0:
		share := math.Floor(left*ChildSharePct) / 100
		if share < 0.01 {
			// A child budget of 0 is no cap: a share that rounds down to nothing is refused instead.
			return 0, limit("session #%d has too little left to give a child a share of it (%s); name a budget of at least $0.01", par.ID, detail)
		}
		return share, nil
	case budget > left+1e-9:
		return 0, limit("budget $%.2f is above what the parent session #%d has left (%s)", budget, par.ID, detail)
	}
	return budget, nil
}

// lowered names the limits of a session that an update lowered: only those are clamped on its children.
type lowered struct{ level, maxTasks, maxDepth, budget bool }

func (l lowered) any() bool { return l.level || l.maxTasks || l.maxDepth || l.budget }

// clampChildren brings the open children of session id (and theirs) within the limits low names, after
// the operator lowered them: down only, never up, each change an event of the child. The children
// share what the parent has left, in id order; a child's budget is never cut below what it spent.
func (s *Store) clampChildren(ctx context.Context, x dbx, id int64, actor Actor, low lowered) error {
	if !low.any() {
		return nil
	}
	var level string
	var maxTasks, maxDepth int
	var budget, spent float64
	if err := x.QueryRowContext(ctx, `SELECT access_level, max_tasks, max_depth, budget_usd, spent_usd FROM task_sessions WHERE id = ?`, id).Scan(&level, &maxTasks, &maxDepth, &budget, &spent); err != nil {
		return fmt.Errorf("tasks: reading session #%d: %w", id, err)
	}
	rows, err := x.QueryContext(ctx, `SELECT id, access_level, max_tasks, max_depth, budget_usd, spent_usd FROM task_sessions WHERE parent_session_id = ? AND archived_at = '' AND state IN `+openChildStates+` ORDER BY id`, id)
	if err != nil {
		return fmt.Errorf("tasks: reading the children of session #%d: %w", id, err)
	}
	type kid struct {
		id                int64
		level             string
		maxTasks, maxDeep int
		budget, spent     float64
	}
	var kids []kid
	for rows.Next() {
		var k kid
		if err := rows.Scan(&k.id, &k.level, &k.maxTasks, &k.maxDeep, &k.budget, &k.spent); err != nil {
			rows.Close()
			return fmt.Errorf("tasks: reading the children of session #%d: %w", id, err)
		}
		kids = append(kids, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("tasks: reading the children of session #%d: %w", id, err)
	}
	closed, err := heldByChildren(ctx, x, id, 0, `state NOT IN `+openChildStates)
	if err != nil {
		return err
	}
	left := budget - spent - closed
	for _, k := range kids {
		var changed []string
		var cut lowered
		if low.level && slices.Index(AccessLevels, k.level) > slices.Index(AccessLevels, level) {
			k.level, cut.level = level, true
			changed = append(changed, "access_level "+level)
		}
		if low.maxTasks && k.maxTasks > maxTasks {
			k.maxTasks, cut.maxTasks = maxTasks, true
			changed = append(changed, fmt.Sprintf("max_tasks %d", maxTasks))
		}
		if low.maxDepth && k.maxDeep > maxDepth {
			k.maxDeep, cut.maxDepth = maxDepth, true
			changed = append(changed, fmt.Sprintf("max_depth %d", maxDepth))
		}
		if low.budget && budget > 0 {
			if cap := max(math.Floor(max(left, 0.01)*100)/100, k.spent); k.budget <= 0 || k.budget > cap {
				k.budget, cut.budget = cap, true
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
		if err := s.clampChildren(ctx, x, k.id, actor, cut); err != nil {
			return err
		}
	}
	return nil
}

// heldByChildren is what the children of parent hold of its budget: the whole budget of a child
// that can still spend (draft, running, paused), what it spent once it is over. A child's spend
// never reaches its parent's spent_usd, so counting only the open children would return a closed
// child's budget to the pool though it was spent. except is a child to leave out (0: none); where
// narrows the children (a SQL condition on task_sessions; archived children are included unless it
// says otherwise).
func heldByChildren(ctx context.Context, x dbx, parent, except int64, where string) (float64, error) {
	var held float64
	err := x.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN state IN `+openChildStates+` THEN budget_usd ELSE spent_usd END), 0)
		FROM task_sessions WHERE parent_session_id = ? AND id <> ? AND `+where, parent, except).Scan(&held)
	if err != nil {
		return 0, fmt.Errorf("tasks: reading the budgets of session #%d's children: %w", parent, err)
	}
	return held, nil
}
