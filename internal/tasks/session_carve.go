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
	left, given, err := sessionLeft(ctx, x, par)
	if err != nil {
		return 0, err
	}
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
	closed, err := heldByChildren(ctx, x, id, true)
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

// heldByChildren is what the children of parent hold of its budget. A child that can still spend
// (draft, running, paused) holds its whole budget, which covers its own subtree because the same rule
// limits its work and its children. A closed one holds what it spent plus what its own children hold,
// so a stopped intermediate keeps the spend of its subtree and the commitments of any open
// descendant. A child's spend never reaches its parent's spent_usd, so counting only the open
// children would return a closed child's budget to the pool though it was spent. closedOnly leaves
// the open direct children out. The walk is profile-scoped, visits a session once and stops at
// HardMaxSessionDepth levels.
func heldByChildren(ctx context.Context, x dbx, parent int64, closedOnly bool) (float64, error) {
	return heldBelow(ctx, x, parent, closedOnly, 1, map[int64]bool{parent: true})
}

func heldBelow(ctx context.Context, x dbx, parent int64, closedOnly bool, level int, seen map[int64]bool) (float64, error) {
	type kid struct {
		id            int64
		open          bool
		budget, spent float64
	}
	rows, err := x.QueryContext(ctx, `SELECT id, state IN `+openChildStates+`, budget_usd, spent_usd FROM task_sessions
		WHERE parent_session_id = ? AND profile_id = (SELECT profile_id FROM task_sessions WHERE id = ?)`, parent, parent)
	if err != nil {
		return 0, fmt.Errorf("tasks: reading the budgets of session #%d's children: %w", parent, err)
	}
	var kids []kid
	for rows.Next() {
		var k kid
		if err := rows.Scan(&k.id, &k.open, &k.budget, &k.spent); err != nil {
			rows.Close()
			return 0, fmt.Errorf("tasks: reading the budgets of session #%d's children: %w", parent, err)
		}
		kids = append(kids, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("tasks: reading the budgets of session #%d's children: %w", parent, err)
	}
	var held float64
	for _, k := range kids {
		if seen[k.id] {
			continue
		}
		seen[k.id] = true
		switch {
		case k.open:
			if !closedOnly {
				held += k.budget
			}
		default:
			held += k.spent
			if level < HardMaxSessionDepth {
				below, err := heldBelow(ctx, x, k.id, false, level+1, seen)
				if err != nil {
					return 0, err
				}
				held += below
			}
		}
	}
	return held, nil
}

// sessionLeft is what session se can still use itself or hand to a new child: its budget less its own
// spend and less what its children hold. Only meaningful for a session with a budget.
func sessionLeft(ctx context.Context, x dbx, se Session) (left, held float64, err error) {
	held, err = heldByChildren(ctx, x, se.ID, false)
	if err != nil {
		return 0, 0, err
	}
	return se.BudgetUSD - se.SpentUSD - held, held, nil
}

// heldOfSession is what one session holds of its parent's budget, by the rule of heldByChildren.
func heldOfSession(ctx context.Context, x dbx, se Session) (float64, error) {
	if se.State == SessionDraft || se.State == SessionRunning || se.State == SessionPaused {
		return se.BudgetUSD, nil
	}
	below, err := heldByChildren(ctx, x, se.ID, false)
	return se.SpentUSD + below, err
}
