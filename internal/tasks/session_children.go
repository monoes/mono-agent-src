package tasks

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// MaxOpenChildren is how many open (draft, running or paused) child sessions one session holds: a
// fence for an agent that starts sessions through the store, equal to the coder host's live-session
// cap per tree. The host's finer, operator-raisable max_children is enforced where it spawns.
const MaxOpenChildren = 12

// isOpen is whether a session can still spend: draft, running or paused.
func isOpen(st SessionState) bool {
	return st == SessionDraft || st == SessionRunning || st == SessionPaused
}

const openChildStates = `('draft','running','paused')`

// checkChildFence bounds the number of open children an agent starts below one parent by
// MaxOpenChildren (the operator is not held to it).
func (s *Store) checkChildFence(ctx context.Context, x dbx, par Session, actor Actor) error {
	if actor.Kind != Agent {
		return nil
	}
	var kids int
	if err := x.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_sessions WHERE parent_session_id = ? AND archived_at = '' AND state IN `+openChildStates, par.ID).Scan(&kids); err != nil {
		return fmt.Errorf("tasks: counting the children of session #%d: %w", par.ID, err)
	}
	if kids >= MaxOpenChildren {
		return limit("session #%d already has %d open child sessions (the most is %d)", par.ID, kids, MaxOpenChildren)
	}
	return nil
}

// lowered names the limits of a session that an update lowered: only those are clamped on its children.
type lowered struct{ level, maxTasks, maxDepth, budget bool }

func (l lowered) any() bool { return l.level || l.maxTasks || l.maxDepth || l.budget }

// clampChildren brings every open session below session id, at any depth and through closed ones, within
// the limits low names, after the operator lowered them: down only, never up, each change an event of
// the session. A budget is clamped session by session to id's; the budgets are not summed. The walk
// visits each session once and fails closed on a cycle or a tree deeper than HardMaxSessionDepth.
func (s *Store) clampChildren(ctx context.Context, x dbx, id int64, actor Actor, low lowered) error {
	if !low.any() {
		return nil
	}
	var level string
	var maxTasks, maxDepth int
	var budget float64
	if err := x.QueryRowContext(ctx, `SELECT access_level, max_tasks, max_depth, budget_usd FROM task_sessions WHERE id = ?`, id).Scan(&level, &maxTasks, &maxDepth, &budget); err != nil {
		return fmt.Errorf("tasks: reading session #%d: %w", id, err)
	}
	seen := []int64{id}
	frontier := []int64{id}
	for depth := 1; len(frontier) > 0; depth++ {
		var next []int64
		for _, parent := range frontier {
			kids, err := s.childrenOf(ctx, x, parent)
			if err != nil {
				return err
			}
			if depth >= HardMaxSessionDepth && len(kids) > 0 {
				return limit("session #%d has sessions deeper than %d levels below it", id, HardMaxSessionDepth)
			}
			for _, k := range kids {
				if slices.Contains(seen, k.id) {
					return invalid("session #%d is in a cycle of parents", k.id)
				}
				seen = append(seen, k.id)
				next = append(next, k.id)
				if !k.open {
					continue
				}
				var changed []string
				if low.level && slices.Index(AccessLevels, k.level) > slices.Index(AccessLevels, level) {
					k.level = level
					changed = append(changed, "access_level "+level)
				}
				if low.maxTasks && k.maxTasks > maxTasks {
					k.maxTasks = maxTasks
					changed = append(changed, fmt.Sprintf("max_tasks %d", maxTasks))
				}
				if low.maxDepth && k.maxDeep > maxDepth {
					k.maxDeep = maxDepth
					changed = append(changed, fmt.Sprintf("max_depth %d", maxDepth))
				}
				if low.budget && budget > 0 && (k.budget <= 0 || k.budget > budget) {
					k.budget = budget
					changed = append(changed, fmt.Sprintf("budget_usd %.2f", budget))
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
			}
		}
		frontier = next
	}
	return nil
}

// childKid is a session below another, as clampChildren reads it.
type childKid struct {
	id                int64
	level             string
	maxTasks, maxDeep int
	budget            float64
	open              bool
}

// childrenOf reads the sessions started directly below session id, archived or closed ones included
// (marked not open), so that a lowered limit passes through them.
func (s *Store) childrenOf(ctx context.Context, x dbx, id int64) ([]childKid, error) {
	rows, err := x.QueryContext(ctx, `SELECT id, access_level, max_tasks, max_depth, budget_usd, archived_at = '' AND state IN `+openChildStates+` FROM task_sessions WHERE parent_session_id = ? ORDER BY id`, id)
	if err != nil {
		return nil, fmt.Errorf("tasks: reading the children of session #%d: %w", id, err)
	}
	defer rows.Close()
	var kids []childKid
	for rows.Next() {
		var k childKid
		if err := rows.Scan(&k.id, &k.level, &k.maxTasks, &k.maxDeep, &k.budget, &k.open); err != nil {
			return nil, fmt.Errorf("tasks: reading the children of session #%d: %w", id, err)
		}
		kids = append(kids, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tasks: reading the children of session #%d: %w", id, err)
	}
	return kids, nil
}
