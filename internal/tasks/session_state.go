package tasks

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// ApproveSession is the operator's approval of the plan: the session's draft tasks move
// to Ready, in their order, and the session runs. Only the operator may, and only a draft
// that holds a task: an agent cannot approve its own plan.
func (s *Store) ApproveSession(ctx context.Context, profileID string, id int64, actor Actor) (Session, error) {
	if actor.Kind != Human {
		return Session{}, operatorOnly("approve a session's plan")
	}
	return s.sessionWrite(ctx, profileID, id, func(x dbx, se Session) error {
		if se.State != SessionDraft || se.Archived {
			return sessionNotReady("session #%d is %s: only a draft is approved", id, se.State)
		}
		ids, _, err := s.sessionColumn(ctx, x, id, StatusInbox)
		if err != nil {
			return err
		}
		// Inbox cards stack newest on top; the plan's order is the order they were added.
		slices.Sort(ids)
		if se.Counts.Total == 0 {
			return invalid("session #%d has no tasks: plan some before approving", id)
		}
		for _, tid := range ids {
			cur, err := s.getTx(ctx, x, profileID, tid)
			if err != nil {
				return err
			}
			if err := s.moveTx(ctx, x, profileID, cur, StatusReady, Placement{Bottom: true}, actor, "moved", "approved with the plan"); err != nil {
				return err
			}
		}
		stamp := s.stamp()
		if _, err := x.ExecContext(ctx, `UPDATE task_sessions SET state = 'running', approved_by = ?, approved_at = ? WHERE id = ?`, actor.Label(), stamp, id); err != nil {
			return fmt.Errorf("tasks: approving session #%d: %w", id, err)
		}
		return s.sessionEvent(ctx, x, id, actor, "approved", fmt.Sprintf("%d tasks to Ready", len(ids)))
	})
}

// sessionColumn lists the ids and positions of a session's cards in one column.
func (s *Store) sessionColumn(ctx context.Context, x dbx, id int64, st Status) ([]int64, []int64, error) {
	rows, err := x.QueryContext(ctx, `SELECT id, position FROM tasks WHERE session_id = ? AND status = ? ORDER BY position, id`, id, string(st))
	if err != nil {
		return nil, nil, fmt.Errorf("tasks: reading a column: %w", err)
	}
	defer rows.Close()
	var ids, pos []int64
	for rows.Next() {
		var i, p int64
		if err := rows.Scan(&i, &p); err != nil {
			return nil, nil, fmt.Errorf("tasks: reading a column: %w", err)
		}
		ids, pos = append(ids, i), append(pos, p)
	}
	return ids, pos, rows.Err()
}

// transition moves a session from one of the states from to another. An agent bound to
// another session is refused.
func (s *Store) transition(ctx context.Context, profileID string, id int64, actor Actor, from []SessionState, to SessionState, kind, note string) (Session, error) {
	if actor.Kind != Human && actor.Kind != Agent {
		return Session{}, operatorOnly(kind + " a session")
	}
	if actor.Kind == Agent {
		if err := needName(actor); err != nil {
			return Session{}, err
		}
		if actor.Session != 0 && actor.Session != id {
			return Session{}, sessionNotFound(id)
		}
		if actor.Task != 0 || actor.Verifier {
			// A worker or a verifier does its one task; stopping or pausing the whole session is
			// for the operator and for the agent that runs the session (the lead, bound to none).
			return Session{}, bindingRefused("this agent is bound to task #%d: it cannot %s the session", actor.Task, kind)
		}
	}
	return s.sessionWrite(ctx, profileID, id, func(x dbx, se Session) error {
		if !slices.Contains(from, se.State) || se.Archived {
			return sessionNotReady("session #%d is %s: it cannot %s now", id, se.State, kind)
		}
		if to == SessionStopped {
			if err := s.releaseClaims(ctx, x, profileID, id, actor); err != nil {
				return err
			}
		}
		if _, err := x.ExecContext(ctx, `UPDATE task_sessions SET state = ? WHERE id = ?`, string(to), id); err != nil {
			return fmt.Errorf("tasks: %s session #%d: %w", kind, id, err)
		}
		return s.sessionEvent(ctx, x, id, actor, kind, note)
	})
}

// releaseClaims takes every claim of the session's tasks back: the tasks go to Ready.
func (s *Store) releaseClaims(ctx context.Context, x dbx, profileID string, id int64, actor Actor) error {
	rows, err := x.QueryContext(ctx, `SELECT id FROM tasks WHERE session_id = ? AND claimed_by <> ''`, id)
	if err != nil {
		return fmt.Errorf("tasks: reading the claims of session #%d: %w", id, err)
	}
	var held []int64
	for rows.Next() {
		var tid int64
		if err := rows.Scan(&tid); err != nil {
			rows.Close()
			return fmt.Errorf("tasks: reading the claims of session #%d: %w", id, err)
		}
		held = append(held, tid)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, tid := range held {
		cur, err := s.getTx(ctx, x, profileID, tid)
		if err != nil {
			return err
		}
		if err := s.moveTx(ctx, x, profileID, cur, StatusReady, Placement{Bottom: true}, actor, "released", "the session was stopped"); err != nil {
			return err
		}
	}
	return nil
}

// PauseSession stops a running session handing out tasks. The operator and an agent may.
func (s *Store) PauseSession(ctx context.Context, profileID string, id int64, actor Actor) (Session, error) {
	return s.transition(ctx, profileID, id, actor, []SessionState{SessionRunning}, SessionPaused, "pause", "")
}

// ResumeSession runs a paused session again. Only the operator may: an agent could undo
// the operator's pause.
func (s *Store) ResumeSession(ctx context.Context, profileID string, id int64, actor Actor) (Session, error) {
	if actor.Kind != Human {
		return Session{}, operatorOnly("resume a session")
	}
	return s.transition(ctx, profileID, id, actor, []SessionState{SessionPaused}, SessionRunning, "resume", "")
}

// StopSession is the kill switch: every claim of the session is released and its tasks
// stay where they are. A stopped session does not run again. The operator and an agent
// may.
func (s *Store) StopSession(ctx context.Context, profileID string, id int64, actor Actor) (Session, error) {
	return s.transition(ctx, profileID, id, actor, []SessionState{SessionDraft, SessionRunning, SessionPaused}, SessionStopped, "stop", "")
}

// CloseSession ends a session as done or failed, with a note. Only the operator may.
func (s *Store) CloseSession(ctx context.Context, profileID string, id int64, to SessionState, note string, actor Actor) (Session, error) {
	if actor.Kind != Human {
		return Session{}, operatorOnly("close a session")
	}
	if to != SessionDone && to != SessionFailed {
		return Session{}, invalid("a session is closed as done or failed")
	}
	return s.transition(ctx, profileID, id, actor, []SessionState{SessionRunning, SessionPaused}, to, "close", string(to)+": "+note)
}

// ArchiveSession hides a session that is not running or paused (stop it first). Its
// tasks stay. Only the operator may.
func (s *Store) ArchiveSession(ctx context.Context, profileID string, id int64, actor Actor) (Session, error) {
	if actor.Kind != Human {
		return Session{}, operatorOnly("archive a session")
	}
	return s.sessionWrite(ctx, profileID, id, func(x dbx, se Session) error {
		if se.Archived {
			return invalid("session #%d is already archived", id)
		}
		if se.State == SessionRunning || se.State == SessionPaused {
			return sessionNotReady("session #%d is %s: stop it before archiving", id, se.State)
		}
		if _, err := x.ExecContext(ctx, `UPDATE task_sessions SET archived_at = ? WHERE id = ?`, s.stamp(), id); err != nil {
			return fmt.Errorf("tasks: archiving session #%d: %w", id, err)
		}
		return s.sessionEvent(ctx, x, id, actor, "archived", "")
	})
}

// UpdateSession changes a session's budget, caps, access level or auto-close setting.
// Only the operator may: this is where a cap is raised.
func (s *Store) UpdateSession(ctx context.Context, profileID string, id int64, u SessionUpdate, actor Actor) (Session, error) {
	return s.ChangeSession(ctx, profileID, id, u, 0, "", actor)
}

// ChangeSession is UpdateSession and AddSessionSpend in one write: the limits in u (when it
// holds any) and spend (when above 0) are applied together or not at all, so that a refused
// spend never leaves the limits changed.
func (s *Store) ChangeSession(ctx context.Context, profileID string, id int64, u SessionUpdate, spend float64, note string, actor Actor) (Session, error) {
	if actor.Kind != Human {
		return Session{}, operatorOnly("change a session's budget, caps, access level or auto-close setting")
	}
	update := u != (SessionUpdate{})
	if !update && spend == 0 {
		return Session{}, invalid("nothing to change")
	}
	if spend != 0 && (!finite(spend) || spend < 0) {
		return Session{}, invalid("spend is an amount in dollars above 0")
	}
	if u.AccessLevel != nil && !validAccess(*u.AccessLevel) {
		return Session{}, invalid("unknown access level %q (use %s)", echo(*u.AccessLevel), strings.Join(AccessLevels, ", "))
	}
	if u.BudgetUSD != nil && (!finite(*u.BudgetUSD) || *u.BudgetUSD < 0) {
		return Session{}, invalid("the budget is an amount in dollars, 0 or more")
	}
	return s.sessionWrite(ctx, profileID, id, func(x dbx, se Session) error {
		if update {
			if err := s.applyUpdate(ctx, x, se, u, actor); err != nil {
				return err
			}
		}
		if spend > 0 {
			return s.applySpend(ctx, x, id, spend, note, actor)
		}
		return nil
	})
}

func (s *Store) applyUpdate(ctx context.Context, x dbx, se Session, u SessionUpdate, actor Actor) error {
	id := se.ID
	maxTasks, maxDepth, maxRetries, budget, level, auto := se.MaxTasks, se.MaxDepth, se.MaxRetries, se.BudgetUSD, se.AccessLevel, se.AutoCloseVerified
	var changed []string
	if u.MaxTasks != nil {
		maxTasks = *u.MaxTasks
		changed = append(changed, fmt.Sprintf("max_tasks %d", maxTasks))
	}
	if u.MaxDepth != nil {
		maxDepth = *u.MaxDepth
		changed = append(changed, fmt.Sprintf("max_depth %d", maxDepth))
	}
	if u.MaxRetries != nil {
		maxRetries = *u.MaxRetries
		changed = append(changed, fmt.Sprintf("max_retries %d", maxRetries))
	}
	if err := checkCaps(maxTasks, maxDepth, maxRetries); err != nil {
		return err
	}
	if u.BudgetUSD != nil {
		budget = *u.BudgetUSD
		changed = append(changed, fmt.Sprintf("budget_usd %.2f", budget))
	}
	if u.AccessLevel != nil {
		level = *u.AccessLevel
		changed = append(changed, "access_level "+level)
	}
	if u.AutoCloseVerified != nil {
		auto = *u.AutoCloseVerified
		changed = append(changed, fmt.Sprintf("auto_close_verified %t", auto))
	}
	on := 0
	if auto {
		on = 1
	}
	if _, err := x.ExecContext(ctx,
		`UPDATE task_sessions SET max_tasks = ?, max_depth = ?, max_retries = ?, budget_usd = ?, access_level = ?, auto_close_verified = ? WHERE id = ?`,
		maxTasks, maxDepth, maxRetries, budget, level, on, id); err != nil {
		return fmt.Errorf("tasks: updating session #%d: %w", id, err)
	}
	return s.sessionEvent(ctx, x, id, actor, "limits", strings.Join(changed, ", "))
}

// AddSessionSpend adds usd to what the session has spent. Only the operator's process
// records spend: an agent could otherwise hide its cost.
func (s *Store) AddSessionSpend(ctx context.Context, profileID string, id int64, usd float64, note string, actor Actor) (Session, error) {
	if actor.Kind != Human {
		return Session{}, operatorOnly("record a session's spend")
	}
	if !finite(usd) || usd <= 0 {
		return Session{}, invalid("spend is an amount in dollars above 0")
	}
	return s.sessionWrite(ctx, profileID, id, func(x dbx, se Session) error {
		return s.applySpend(ctx, x, id, usd, note, actor)
	})
}

func (s *Store) applySpend(ctx context.Context, x dbx, id int64, usd float64, note string, actor Actor) error {
	if _, err := x.ExecContext(ctx, `UPDATE task_sessions SET spent_usd = spent_usd + ? WHERE id = ?`, usd, id); err != nil {
		return fmt.Errorf("tasks: recording spend of session #%d: %w", id, err)
	}
	return s.sessionEvent(ctx, x, id, actor, "spend", fmt.Sprintf("$%.4f %s", usd, note))
}

// overBudget refuses work on a session that has spent its budget (0: none set).
func overBudget(se Session) error {
	if se.BudgetUSD > 0 && se.SpentUSD >= se.BudgetUSD {
		return limit("session #%d has spent its budget ($%.2f of $%.2f): the operator raises it", se.ID, se.SpentUSD, se.BudgetUSD)
	}
	return nil
}
