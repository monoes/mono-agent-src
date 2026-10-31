package tasks

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
)

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
			// for the operator and for the agent that runs the session (the lead and the conductor, whose
			// in-process actor is bound to the session: Session set, no task).
			return Session{}, bindingRefused("this agent is bound to task #%d: it cannot %s the session", actor.Task, kind)
		}
	}
	return s.sessionWrite(ctx, profileID, id, func(x dbx, se Session) error {
		// In a session a conversation manages, only the operator and the lead (bound to the
		// session) may pause or stop it: an agent with no binding may not end the others' work.
		if err := s.noUnboundInManaged(ctx, x, profileID, id, actor, kind+" a session"); err != nil {
			return err
		}
		if !slices.Contains(from, se.State) || se.Archived {
			return sessionNotReady("session #%d is %s: it cannot %s now", id, se.State, kind)
		}
		return s.endTx(ctx, x, profileID, id, actor, to, kind, note)
	})
}

// endTx moves session id to a new state inside a write: a stop or a close releases the
// session's claims first.
func (s *Store) endTx(ctx context.Context, x dbx, profileID string, id int64, actor Actor, to SessionState, kind, note string) error {
	if to == SessionStopped || kind == "close" {
		why := "the session was stopped"
		if kind == "close" {
			why = "the session was closed"
		}
		if err := s.releaseClaims(ctx, x, profileID, id, actor, why); err != nil {
			return err
		}
	}
	if _, err := x.ExecContext(ctx, `UPDATE task_sessions SET state = ? WHERE id = ?`, string(to), id); err != nil {
		return fmt.Errorf("tasks: %s session #%d: %w", kind, id, err)
	}
	return s.sessionEvent(ctx, x, id, actor, kind, note)
}

// releaseClaims takes every claim of the session's tasks back: the tasks go to Ready.
func (s *Store) releaseClaims(ctx context.Context, x dbx, profileID string, id int64, actor Actor, why string) error {
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
		if err := s.moveTx(ctx, x, profileID, cur, StatusReady, Placement{Bottom: true}, actor, "released", why); err != nil {
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
		all, err := s.subtree(ctx, x, profileID, id)
		if err != nil {
			return err
		}
		if open := openIn(all); len(open) > 0 {
			return sessionNotReady("%s", openChildrenMessage(id, open))
		}
		// Only finished sessions are below it: they go with it, without a prompt.
		if _, err := s.archiveBelow(ctx, x, profileID, all, actor); err != nil {
			return err
		}
		return s.archiveTx(ctx, x, id, actor, "")
	})
}

// archiveTx hides session id inside a write and records the event.
func (s *Store) archiveTx(ctx context.Context, x dbx, id int64, actor Actor, note string) error {
	if _, err := x.ExecContext(ctx, `UPDATE task_sessions SET archived_at = ? WHERE id = ?`, s.stamp(), id); err != nil {
		return fmt.Errorf("tasks: archiving session #%d: %w", id, err)
	}
	return s.sessionEvent(ctx, x, id, actor, "archived", note)
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
	if u.BudgetUSD != nil && (!finite(*u.BudgetUSD) || *u.BudgetUSD < 0 || *u.BudgetUSD > MaxBudgetUSD) {
		return Session{}, invalid("the budget is an amount in dollars, 0 to %.0f", MaxBudgetUSD)
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
	if se.ParentSessionID != 0 {
		if err := s.withinParentUpdate(ctx, x, se, u); err != nil {
			return err
		}
	}
	if u.BudgetUSD != nil {
		if err := s.budgetFloor(ctx, x, se, budget); err != nil {
			return err
		}
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
	if err := s.sessionEvent(ctx, x, id, actor, "limits", strings.Join(changed, ", ")); err != nil {
		return err
	}
	return s.clampChildren(ctx, x, id, actor, lowered{
		level:    slices.Index(AccessLevels, level) < slices.Index(AccessLevels, se.AccessLevel),
		maxTasks: maxTasks < se.MaxTasks,
		maxDepth: maxDepth < se.MaxDepth,
		budget:   budget > 0 && (se.BudgetUSD <= 0 || budget < se.BudgetUSD),
	})
}

// withinCeiling is nil when the session's own limits are within the ceiling above it: a session
// that is not (a store edited by hand) is not started.
func withinCeiling(se Session, c ceiling) error {
	switch {
	case slices.Index(AccessLevels, se.AccessLevel) > slices.Index(AccessLevels, c.level):
		return limit("session #%d has access %q, above the %q of a session above it: the operator lowers it first", se.ID, se.AccessLevel, c.level)
	case se.MaxTasks > c.maxTasks:
		return limit("session #%d has max tasks %d, above the %d of a session above it: the operator lowers it first", se.ID, se.MaxTasks, c.maxTasks)
	case se.MaxDepth > c.maxDepth:
		return limit("session #%d has max depth %d, above the %d of a session above it: the operator lowers it first", se.ID, se.MaxDepth, c.maxDepth)
	case c.budget > 0 && se.BudgetUSD > c.budget:
		return limit("session #%d has budget $%.2f, above the $%.2f of a session above it: the operator lowers it first", se.ID, se.BudgetUSD, c.budget)
	}
	return nil
}

// withinParentUpdate holds an update of a child to the rule its creation followed: what u raises may
// not pass the ceiling above it (the lowest access, task cap, depth cap and nonzero budget on the whole
// chain of ancestors, closed ones included), and under an ancestor with a budget the child's budget is
// above 0 (0 is no cap) and at most that. Each child is checked by itself: the sum
// of the children's budgets is not checked against the parent's.
func (s *Store) withinParentUpdate(ctx context.Context, x dbx, se Session, u SessionUpdate) error {
	top, err := s.ceilingAt(ctx, x, se.ProfileID, se.ParentSessionID)
	if err != nil {
		return err
	}
	par, err := s.sessionTx(ctx, x, se.ProfileID, se.ParentSessionID)
	if err != nil {
		return err
	}
	par = top.asParent(par)
	if u.AccessLevel != nil && slices.Index(AccessLevels, *u.AccessLevel) > slices.Index(AccessLevels, par.AccessLevel) {
		return limit("access %q is above the parent session #%d's %q", *u.AccessLevel, par.ID, par.AccessLevel)
	}
	if u.MaxTasks != nil && *u.MaxTasks > par.MaxTasks {
		return limit("max tasks %d is above the parent session #%d's %d", *u.MaxTasks, par.ID, par.MaxTasks)
	}
	if u.MaxDepth != nil && *u.MaxDepth > par.MaxDepth {
		return limit("max depth %d is above the parent session #%d's %d", *u.MaxDepth, par.ID, par.MaxDepth)
	}
	if u.BudgetUSD == nil || par.BudgetUSD <= 0 {
		return nil
	}
	if *u.BudgetUSD == 0 {
		return limit("budget $0.00 (no cap) is refused under the parent session #%d's $%.2f: name an amount of at most that", par.ID, par.BudgetUSD)
	}
	if *u.BudgetUSD > par.BudgetUSD {
		return limit("budget $%.2f is above the parent session #%d's $%.2f", *u.BudgetUSD, par.ID, par.BudgetUSD)
	}
	return nil
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

// Tx is the transaction AddSessionSpendWith hands its callback: the one the spend is written in.
type Tx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// AddSessionSpendWith is AddSessionSpend with a record written in the same transaction: also runs after the meter
// is updated and, if it fails, the spend is rolled back with it. A caller that must record "this was charged"
// exactly once uses it, so the meter and the record are never apart.
func (s *Store) AddSessionSpendWith(ctx context.Context, profileID string, id int64, usd float64, note string, actor Actor, also func(ctx context.Context, x Tx) error) (Session, error) {
	if actor.Kind != Human {
		return Session{}, operatorOnly("record a session's spend")
	}
	if !finite(usd) || usd <= 0 {
		return Session{}, invalid("spend is an amount in dollars above 0")
	}
	return s.sessionWrite(ctx, profileID, id, func(x dbx, se Session) error {
		if err := s.applySpend(ctx, x, id, usd, note, actor); err != nil {
			return err
		}
		return also(ctx, x)
	})
}

// applySpend adds an operator's amount to the session's spend through the micro-dollar grid, the same one the
// reservations use: next = spent + usd, each rounded to whole micro-dollars, so spent stays a multiple of 1e-6
// (a legacy total off the grid snaps to the nearest micro-dollar on this write). An amount under half a micro-dollar
// is a zero charge: nothing is written and no event is recorded. A total (or one amount) beyond MaxBudgetUSD is
// refused before anything is written: a figure past it is a typo or an overflow in the making, never a real
// bill. Spending past the session's own budget is not refused; it is recorded as it happened.
func (s *Store) applySpend(ctx context.Context, x dbx, id int64, usd float64, note string, actor Actor) error {
	if !finite(usd) || usd > MaxBudgetUSD {
		return limit("spend of $%g is beyond the supported $%g: check the figure", usd, MaxBudgetUSD)
	}
	m := toMicros(usd)
	if m == 0 {
		return nil
	}
	var spent float64
	if err := x.QueryRowContext(ctx, `SELECT spent_usd FROM task_sessions WHERE id = ?`, id).Scan(&spent); err != nil {
		return sessionLookup(err, id)
	}
	next := fromMicros(toMicros(spent) + m)
	if next > MaxBudgetUSD {
		return limit("spend of $%g would take session #%d to $%g, beyond the supported $%g", usd, id, next, MaxBudgetUSD)
	}
	if _, err := x.ExecContext(ctx, `UPDATE task_sessions SET spent_usd = ? WHERE id = ?`, next, id); err != nil {
		return fmt.Errorf("tasks: recording spend of session #%d: %w", id, err)
	}
	return s.sessionEvent(ctx, x, id, actor, "spend", fmt.Sprintf("$%.6f %s", fromMicros(m), note))
}

// overBudget refuses work on a session that has spent its budget (0: none set).
func overBudget(se Session) error {
	if se.BudgetUSD > 0 && se.SpentUSD >= se.BudgetUSD {
		return limit("session #%d has spent its budget ($%.2f of $%.2f): the operator raises it", se.ID, se.SpentUSD, se.BudgetUSD)
	}
	return nil
}
