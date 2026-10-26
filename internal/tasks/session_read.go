package tasks

import (
	"context"
	"fmt"
	"slices"
)

// SessionBoard returns a session's board in one snapshot, the five columns of its tasks
// (a draft's tasks are in the inbox column). Like the personal board it is the operator's,
// or an agent's the operator let view the board (task agents allow --view).
func (s *Store) SessionBoard(ctx context.Context, profileID string, sessionID int64, doneLimit int, actor Actor) (SessionBoard, error) {
	if err := readerOf(actor, sessionID); err != nil {
		return SessionBoard{}, err
	}
	if actor.Kind != Human && !s.agentMay(ctx, profileID, actor, func(a AgentAccess) bool { return a.View }) {
		return SessionBoard{}, operatorOnly("show a session's board")
	}
	b := SessionBoard{Tasks: map[Status][]Task{}}
	err := s.snapshot(ctx, func(x dbx) error {
		var err error
		if b.Session, err = s.sessionTx(ctx, x, profileID, sessionID); err != nil {
			return err
		}
		b.Rev, b.Counts = b.Session.Rev, b.Session.Counts
		for _, st := range BoardStatuses {
			q := `SELECT ` + taskCols + ` FROM tasks WHERE session_id = ? AND status = ? ORDER BY position, id`
			args := []any{sessionID, string(st)}
			if st == StatusDone && doneLimit > 0 {
				q += ` LIMIT ?`
				args = append(args, doneLimit)
			}
			ts, err := s.queryTasks(ctx, x, q, args...)
			if err != nil {
				return err
			}
			if err := s.attachDeps(ctx, x, ts); err != nil {
				return err
			}
			b.Tasks[st] = ts
		}
		return nil
	})
	if err != nil {
		return SessionBoard{}, err
	}
	return b, nil
}

// SessionTasks lists a session's tasks by column, then position, then id, at most f.Limit of
// them (500 when it is not above 0, never more than 2,000), from the statuses named or, when
// none is, the five columns for the operator and ready, in_progress and review for an agent
// (a draft's tasks are in the inbox column: an agent names it to read them, or reads the plan).
func (s *Store) SessionTasks(ctx context.Context, profileID string, sessionID int64, f Filter, actor Actor) ([]Task, error) {
	if err := readerOf(actor, sessionID); err != nil {
		return nil, err
	}
	statuses := make([]Status, 0, len(BoardStatuses)+1)
	for _, name := range f.Statuses {
		st, err := ParseStatus(string(name))
		if err != nil {
			return nil, err
		}
		if !slices.Contains(statuses, st) {
			statuses = append(statuses, st)
		}
	}
	if len(statuses) == 0 {
		statuses = defaultStatuses(actor)
		if s.agentMay(ctx, profileID, actor, func(a AgentAccess) bool { return a.View }) {
			statuses = BoardStatuses
		}
	}
	n := f.Limit
	if n <= 0 {
		n = DefaultListLimit
	}
	n = min(n, MaxListLimit)
	q := `SELECT ` + taskCols + ` FROM tasks WHERE session_id = ? AND profile_id = ? AND status IN (` + placeholders(len(statuses)) + `)`
	args := []any{sessionID, profileID}
	for _, st := range statuses {
		args = append(args, string(st))
	}
	if f.ClaimedBy != "" {
		q += ` AND claimed_by = ?`
		args = append(args, f.ClaimedBy)
	}
	q += ` ORDER BY ` + statusOrder + `, position, id LIMIT ?`
	args = append(args, n)
	var out []Task
	err := s.snapshot(ctx, func(x dbx) error {
		if _, err := s.sessionTx(ctx, x, profileID, sessionID); err != nil {
			return err
		}
		var err error
		if out, err = s.queryTasks(ctx, x, q, args...); err != nil {
			return err
		}
		return s.attachDeps(ctx, x, out)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// PlanOfSession returns a session with all its tasks that are not archived, in the order
// they were written, each with its dependencies. The operator and agents read it.
func (s *Store) PlanOfSession(ctx context.Context, profileID string, sessionID int64, actor Actor) (SessionPlan, error) {
	if err := readerOf(actor, sessionID); err != nil {
		return SessionPlan{}, err
	}
	var p SessionPlan
	err := s.snapshot(ctx, func(x dbx) error {
		var err error
		if p.Session, err = s.sessionTx(ctx, x, profileID, sessionID); err != nil {
			return err
		}
		if p.Tasks, err = s.queryTasks(ctx, x,
			`SELECT `+taskCols+` FROM tasks WHERE session_id = ? AND status <> 'archived' ORDER BY id`, sessionID); err != nil {
			return err
		}
		return s.attachDeps(ctx, x, p.Tasks)
	})
	if err != nil {
		return SessionPlan{}, err
	}
	return p, nil
}

// SessionTask returns one task of the session with its history and its artifacts. A task of
// another session, or of the personal board, is not found.
func (s *Store) SessionTask(ctx context.Context, profileID string, sessionID, id int64, actor Actor) (SessionTaskDetail, error) {
	if err := readerOf(actor, sessionID); err != nil {
		return SessionTaskDetail{}, err
	}
	var d SessionTaskDetail
	err := s.snapshot(ctx, func(x dbx) error {
		if _, err := s.sessionTx(ctx, x, profileID, sessionID); err != nil {
			return err
		}
		if err := s.inSession(ctx, x, sessionID, []int64{id}); err != nil {
			return err
		}
		var err error
		if d.Task, err = s.sessionTaskTx(ctx, x, profileID, id); err != nil {
			return err
		}
		d.Events, err = s.eventsOf(ctx, x, id)
		if err != nil {
			return err
		}
		d.Artifacts, err = s.artifactsOf(ctx, x, id)
		return err
	})
	if err != nil {
		return SessionTaskDetail{}, err
	}
	return d, nil
}

func (s *Store) eventsOf(ctx context.Context, x dbx, id int64) ([]Event, error) {
	events := []Event{}
	rows, err := x.QueryContext(ctx, `SELECT id, at, actor, kind, from_status, to_status, note FROM task_events WHERE task_id = ? ORDER BY id`, id)
	if err != nil {
		return nil, fmt.Errorf("tasks: reading events: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var e Event
		var at string
		if err := rows.Scan(&e.ID, &at, &e.Actor, &e.Kind, &e.FromStatus, &e.ToStatus, &e.Note); err != nil {
			return nil, fmt.Errorf("tasks: reading events: %w", err)
		}
		if e.At, err = storedTime("time of an event", at); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

func (s *Store) artifactsOf(ctx context.Context, x dbx, id int64) ([]Artifact, error) {
	out := []Artifact{}
	rows, err := x.QueryContext(ctx, `SELECT id, task_id, kind, ref, note, at, actor FROM task_artifacts WHERE task_id = ? ORDER BY id`, id)
	if err != nil {
		return nil, fmt.Errorf("tasks: reading artifacts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var a Artifact
		var at string
		if err := rows.Scan(&a.ID, &a.TaskID, &a.Kind, &a.Ref, &a.Note, &at, &a.Actor); err != nil {
			return nil, fmt.Errorf("tasks: reading artifacts: %w", err)
		}
		if a.At, err = storedTime("time of an artifact", at); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
