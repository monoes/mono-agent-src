package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"unicode/utf8"
)

// actorIn judges who writes to a session's plan: the operator, or a named agent that is
// not bound to another session.
func actorIn(actor Actor, session int64) error {
	switch actor.Kind {
	case Human:
	case Agent:
		if err := needName(actor); err != nil {
			return err
		}
	default:
		return operatorOnly("change a session's tasks")
	}
	if actor.Verifier && actor.Task == 0 {
		return operatorOnly("act as a verifier: it is bound to no task")
	}
	return bound(actor, session)
}

// openSession reads a session for a write to its tasks: it must exist, not be archived and
// not be closed.
func (s *Store) openSession(ctx context.Context, x dbx, profileID string, id int64) (Session, error) {
	se, err := s.sessionTx(ctx, x, profileID, id)
	if err != nil {
		return Session{}, err
	}
	if se.Archived || se.State.closed() {
		return Session{}, sessionNotReady("session #%d is %s: nothing more is added to it", id, closedWord(se))
	}
	return se, nil
}

func closedWord(se Session) string {
	if se.Archived {
		return "archived"
	}
	return string(se.State)
}

// sessionTaskTx is one task of a session, with its dependencies.
func (s *Store) sessionTaskTx(ctx context.Context, x dbx, profileID string, id int64) (Task, error) {
	t, err := s.getTx(ctx, x, profileID, id)
	if err != nil {
		return Task{}, err
	}
	ts := []Task{t}
	if err := s.attachDeps(ctx, x, ts); err != nil {
		return Task{}, err
	}
	return ts[0], nil
}

// attachDeps fills DependsOn of the tasks that belong to a session.
func (s *Store) attachDeps(ctx context.Context, x dbx, ts []Task) error {
	index := map[int64]int{}
	var args []any
	for i, t := range ts {
		if t.SessionID != 0 {
			index[t.ID] = i
			args = append(args, t.ID)
		}
	}
	if len(args) == 0 {
		return nil
	}
	rows, err := x.QueryContext(ctx,
		`SELECT task_id, depends_on_task_id FROM task_deps WHERE task_id IN (`+placeholders(len(args))+`) ORDER BY task_id, depends_on_task_id`, args...)
	if err != nil {
		return fmt.Errorf("tasks: reading dependencies: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, on int64
		if err := rows.Scan(&id, &on); err != nil {
			return fmt.Errorf("tasks: reading dependencies: %w", err)
		}
		ts[index[id]].DependsOn = append(ts[index[id]].DependsOn, on)
	}
	return rows.Err()
}

// depthOf is how deep a task of the session sits: 1 for one with no parent.
func depthOf(ctx context.Context, x dbx, id int64) (int, error) {
	depth := 0
	for cur := id; cur != 0; depth++ {
		if depth > MaxSessionDepthCeiling+1 {
			return 0, fmt.Errorf("tasks: the parents of #%d do not end", id)
		}
		var parent sql.NullInt64
		if err := x.QueryRowContext(ctx, `SELECT parent_id FROM tasks WHERE id = ?`, cur).Scan(&parent); err != nil {
			return 0, fmt.Errorf("tasks: reading the parent of #%d: %w", cur, err)
		}
		cur = parent.Int64
	}
	return depth, nil
}

// inSession says whether every id is a task of the session.
func (s *Store) inSession(ctx context.Context, x dbx, session int64, ids []int64) error {
	for _, id := range ids {
		var n int
		if err := x.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE id = ? AND session_id = ?`, id, session).Scan(&n); err != nil {
			return fmt.Errorf("tasks: reading #%d: %w", id, err)
		}
		if n == 0 {
			return fmt.Errorf("%w: #%d is not a task of session #%d", ErrNotFound, id, session)
		}
	}
	return nil
}

func uniq(ids []int64) ([]int64, error) {
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, invalid("%d is not a task id", id)
		}
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out, nil
}

// insertSessionTask writes a new task of the session (not its dependencies) and returns
// its id. status is inbox in a draft and ready in a session that runs.
func (s *Store) insertSessionTask(ctx context.Context, x dbx, profileID string, se Session, status Status, in SessionTaskInput, title, notes, acceptance, source string, actor Actor) (int64, error) {
	pos, err := s.edgePositionIn(ctx, x, profileID, se.ID, status, atTop(status))
	if err != nil {
		return 0, err
	}
	var parent any
	if in.ParentID != 0 {
		parent = in.ParentID
	}
	stamp := s.stamp()
	res, err := x.ExecContext(ctx,
		`INSERT INTO tasks (profile_id, title, notes, status, position, source_kind, created_at, updated_at,
		                    session_id, kind, parent_id, acceptance, assignee, suggested_executor)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		profileID, title, notes, string(status), pos, source, stamp, stamp,
		se.ID, in.Kind, parent, acceptance, in.Assignee, in.SuggestedExecutor)
	if err != nil {
		return 0, fmt.Errorf("tasks: adding a task: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("tasks: adding a task: %w", err)
	}
	if err := s.event(ctx, x, id, stamp, actor.Label(), "created", "", string(status), ""); err != nil {
		return 0, err
	}
	return id, nil
}

// edgePositionIn is a position at the top or the bottom of a column of one session.
func (s *Store) edgePositionIn(ctx context.Context, x dbx, profileID string, session int64, status Status, top bool) (int64, error) {
	var lo, hi sql.NullInt64
	err := x.QueryRowContext(ctx,
		`SELECT MIN(position), MAX(position) FROM tasks WHERE profile_id = ? AND status = ? AND session_id = ?`,
		profileID, string(status), session).Scan(&lo, &hi)
	if err != nil {
		return 0, fmt.Errorf("tasks: reading a column: %w", err)
	}
	switch {
	case !lo.Valid:
		return positionGap, nil
	case top:
		return gapAbove(lo.Int64)
	}
	return gapBelow(hi.Int64)
}

// noEpicDeps refuses a dependency on an epic or an archived task: neither ever settles, so
// what waits for it would wait for good (a split hands its dependents to the parts instead).
func (s *Store) noEpicDeps(ctx context.Context, x dbx, on []int64) error {
	for _, d := range on {
		var kind, status string
		err := x.QueryRowContext(ctx, `SELECT kind, status FROM tasks WHERE id = ?`, d).Scan(&kind, &status)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("tasks: reading #%d: %w", d, err)
		}
		if kind == "epic" {
			return invalid("task #%d is an epic, which is never done: depend on one of its parts instead", d)
		}
		if status == string(StatusArchived) {
			return invalid("task #%d is archived and never settles: depend on a task that is not", d)
		}
	}
	return nil
}

func (s *Store) insertDeps(ctx context.Context, x dbx, task int64, on []int64) error {
	for _, d := range on {
		if _, err := x.ExecContext(ctx, `INSERT OR IGNORE INTO task_deps (task_id, depends_on_task_id) VALUES (?,?)`, task, d); err != nil {
			return fmt.Errorf("tasks: adding a dependency: %w", err)
		}
	}
	return nil
}

func (s *Store) dropDeps(ctx context.Context, x dbx, task int64, on []int64) error {
	for _, d := range on {
		if _, err := x.ExecContext(ctx, `DELETE FROM task_deps WHERE task_id = ? AND depends_on_task_id = ?`, task, d); err != nil {
			return fmt.Errorf("tasks: removing a dependency: %w", err)
		}
	}
	return nil
}

// AddSessionTask adds a task to a session. In a draft it waits in the inbox column for the
// operator's approval; in a running or paused session it is Ready at once (D5: no separate
// cap on additions after approval: max_tasks bounds the session). The task needs acceptance
// criteria, a parent of the session within max_depth, and dependencies of the session.
// The operator and a named agent bound to this session (or to none) may add.
func (s *Store) AddSessionTask(ctx context.Context, profileID string, sessionID int64, in SessionTaskInput, actor Actor) (Task, error) {
	if err := actorIn(actor, sessionID); err != nil {
		return Task{}, err
	}
	if err := noPicking(actor, "add a task"); err != nil {
		return Task{}, err
	}
	if n := utf8.RuneCountInString(oneLine(dropZeroWidthSpaces(in.Title))); n > MaxTitleRunes {
		return Task{}, invalid("the title is %d characters: a title is at most %d (put the detail in notes or the acceptance criteria)", n, MaxTitleRunes)
	}
	title, notes, err := deriveTitleNotes(in.Title, in.Notes, "")
	if err != nil {
		return Task{}, err
	}
	acceptance := cutBytes(cleanText(in.Acceptance), MaxAcceptanceBytes)
	if blank(acceptance) {
		return Task{}, invalid("a session task needs acceptance criteria: how will anyone know it is done?")
	}
	if in.Kind == "" {
		in.Kind = KindTask
	}
	if !slices.Contains([]string{KindTask, KindEpic, KindCheck}, in.Kind) {
		return Task{}, invalid("unknown kind %q (use task, epic or check)", echo(in.Kind))
	}
	in.SuggestedExecutor = cutRunes(oneLine(in.SuggestedExecutor), MaxNameLen)
	in.Assignee = cutRunes(oneLine(in.Assignee), MaxNameLen)
	if in.DependsOn, err = uniq(in.DependsOn); err != nil {
		return Task{}, err
	}
	source, err := sourceKindFor(actor, "")
	if err != nil {
		return Task{}, err
	}
	var out Task
	err = s.tx(ctx, func(x dbx) error {
		if _, err := s.profileOf(ctx, x, profileID); err != nil {
			return err
		}
		se, err := s.openSession(ctx, x, profileID, sessionID)
		if err != nil {
			return err
		}
		if se.Counts.Stored() >= se.MaxTasks {
			return limit("session #%d already has %d tasks: the operator raises its max tasks", sessionID, se.MaxTasks)
		}
		if err := s.inSession(ctx, x, sessionID, in.DependsOn); err != nil {
			return err
		}
		if in.ParentID != 0 {
			if err := s.inSession(ctx, x, sessionID, []int64{in.ParentID}); err != nil {
				return err
			}
			d, err := depthOf(ctx, x, in.ParentID)
			if err != nil {
				return err
			}
			if d+1 > se.MaxDepth {
				return limit("session #%d allows tasks %d deep: the operator raises its max depth", sessionID, se.MaxDepth)
			}
		}
		status := StatusInbox
		if se.State != SessionDraft {
			status = StatusReady
		}
		id, err := s.insertSessionTask(ctx, x, profileID, se, status, in, title, notes, acceptance, source, actor)
		if err != nil {
			return err
		}
		if err := s.noEpicDeps(ctx, x, in.DependsOn); err != nil {
			return err
		}
		if err := s.insertDeps(ctx, x, id, in.DependsOn); err != nil {
			return err
		}
		if se.State != SessionDraft {
			if err := s.sessionEvent(ctx, x, sessionID, actor, "task_added", fmt.Sprintf("#%d %s", id, title)); err != nil {
				return err
			}
		}
		if err := s.touchSession(ctx, x, sessionID); err != nil {
			return err
		}
		out, err = s.sessionTaskTx(ctx, x, profileID, id)
		return err
	})
	if err != nil {
		return Task{}, err
	}
	return out, nil
}

// reaches says whether task from leads to task target through the dependencies (from waits
// for what it depends on, and so on).
func reaches(ctx context.Context, x dbx, from, target int64) (bool, error) {
	seen := map[int64]bool{}
	stack := []int64{from}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if cur == target {
			return true, nil
		}
		if seen[cur] {
			continue
		}
		seen[cur] = true
		rows, err := x.QueryContext(ctx, `SELECT depends_on_task_id FROM task_deps WHERE task_id = ?`, cur)
		if err != nil {
			return false, fmt.Errorf("tasks: reading dependencies: %w", err)
		}
		for rows.Next() {
			var d int64
			if err := rows.Scan(&d); err != nil {
				rows.Close()
				return false, fmt.Errorf("tasks: reading dependencies: %w", err)
			}
			stack = append(stack, d)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return false, err
		}
	}
	return false, nil
}

// AddDependencies makes task id wait for the tasks in on. A task that depends on itself or
// would close a cycle is refused; so is a task that has begun (only inbox and ready tasks
// take new dependencies). The operator and a named agent of the session may.
func (s *Store) AddDependencies(ctx context.Context, profileID string, sessionID, id int64, on []int64, actor Actor) (Task, error) {
	if err := actorIn(actor, sessionID); err != nil {
		return Task{}, err
	}
	if err := noPicking(actor, "add dependencies"); err != nil {
		return Task{}, err
	}
	on, err := uniq(on)
	if err == nil && len(on) == 0 {
		err = invalid("name at least one task to depend on")
	}
	if err != nil {
		return Task{}, err
	}
	var out Task
	err = s.tx(ctx, func(x dbx) error {
		if _, err := s.profileOf(ctx, x, profileID); err != nil {
			return err
		}
		if _, err := s.openSession(ctx, x, profileID, sessionID); err != nil {
			return err
		}
		if err := s.inSession(ctx, x, sessionID, append([]int64{id}, on...)); err != nil {
			return err
		}
		cur, err := s.getTx(ctx, x, profileID, id)
		if err != nil {
			return err
		}
		if cur.Status != StatusInbox && cur.Status != StatusReady {
			return invalid("task #%d is %s: only a task that has not begun takes new dependencies", id, cur.Status)
		}
		for _, d := range on {
			if d == id {
				return invalid("task #%d cannot depend on itself", id)
			}
			cycle, err := reaches(ctx, x, d, id)
			if err != nil {
				return err
			}
			if cycle {
				return invalid("task #%d cannot depend on #%d: #%d already waits for #%d, so that would be a cycle", id, d, d, id)
			}
		}
		if err := s.noEpicDeps(ctx, x, on); err != nil {
			return err
		}
		if err := s.insertDeps(ctx, x, id, on); err != nil {
			return err
		}
		if err := s.event(ctx, x, id, s.stamp(), actor.Label(), "depends", "", "", idList(on)); err != nil {
			return err
		}
		if err := s.touchSession(ctx, x, sessionID); err != nil {
			return err
		}
		out, err = s.sessionTaskTx(ctx, x, profileID, id)
		return err
	})
	if err != nil {
		return Task{}, err
	}
	return out, nil
}

// RemoveDependencies drops dependencies of task id. Only the operator may: a wrong
// dependency would otherwise be undone by the agent that made it, unseen.
func (s *Store) RemoveDependencies(ctx context.Context, profileID string, sessionID, id int64, on []int64, actor Actor) (Task, error) {
	if actor.Kind != Human {
		return Task{}, operatorOnly("remove a task's dependencies")
	}
	on, err := uniq(on)
	if err == nil && len(on) == 0 {
		err = invalid("name at least one dependency to remove")
	}
	if err != nil {
		return Task{}, err
	}
	var out Task
	err = s.tx(ctx, func(x dbx) error {
		if _, err := s.profileOf(ctx, x, profileID); err != nil {
			return err
		}
		if _, err := s.sessionTx(ctx, x, profileID, sessionID); err != nil {
			return err
		}
		if err := s.inSession(ctx, x, sessionID, []int64{id}); err != nil {
			return err
		}
		if err := s.dropDeps(ctx, x, id, on); err != nil {
			return err
		}
		if err := s.event(ctx, x, id, s.stamp(), actor.Label(), "undepends", "", "", idList(on)); err != nil {
			return err
		}
		if err := s.touchSession(ctx, x, sessionID); err != nil {
			return err
		}
		out, err = s.sessionTaskTx(ctx, x, profileID, id)
		return err
	})
	if err != nil {
		return Task{}, err
	}
	return out, nil
}
