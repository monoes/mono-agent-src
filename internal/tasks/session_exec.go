package tasks

import (
	"context"
	"fmt"
)

// SessionReady lists what a scheduler may start in a session, in the order SessionNext
// would hand it out: the Ready tasks whose dependencies are done (the position order), then
// the claims whose lease has ended (the one that ended first first). It is SessionNext's own
// condition read as a list, so the two never differ. A session that does not run, or has
// spent its budget, has none.
func (s *Store) SessionReady(ctx context.Context, profileID string, sessionID int64, actor Actor) ([]Task, error) {
	if err := readerOf(actor, sessionID); err != nil {
		return nil, err
	}
	var out []Task
	err := s.snapshot(ctx, func(x dbx) error {
		if _, err := s.profileOf(ctx, x, profileID); err != nil {
			return err
		}
		se, err := s.sessionTx(ctx, x, profileID, sessionID)
		if err != nil {
			return err
		}
		if runnable(se) != nil {
			return nil
		}
		ids, err := s.idsOf(ctx, x, `SELECT id FROM tasks WHERE `+readyInSession+` ORDER BY position, id`, sessionID, MaxEventsToClaim)
		if err != nil {
			return err
		}
		stale, err := s.idsOf(ctx, x,
			`SELECT id FROM tasks WHERE session_id = ? AND status = 'in_progress' AND claimed_by <> '' AND claim_until <= ?
			   AND (SELECT COUNT(*) FROM task_events WHERE task_id = tasks.id) < ?
			 ORDER BY claim_until, id`, sessionID, s.now().UTC().Format(timeFmt), MaxEventsToClaim)
		if err != nil {
			return err
		}
		for _, id := range append(ids, stale...) {
			t, err := s.sessionTaskTx(ctx, x, profileID, id)
			if err != nil {
				return err
			}
			out = append(out, t)
		}
		return nil
	})
	return out, err
}

func (s *Store) idsOf(ctx context.Context, x dbx, query string, args ...any) ([]int64, error) {
	rows, err := x.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("tasks: reading ready tasks: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("tasks: reading ready tasks: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// SetAssignee records which executor carries a task out: "lead", "worker:<id>" or
// "subagent:<id>". The operator may on any task of the session; an agent only on a task it
// holds.
func (s *Store) SetAssignee(ctx context.Context, profileID string, sessionID, id int64, assignee string, actor Actor) (Task, error) {
	if err := actorIn(actor, sessionID); err != nil {
		return Task{}, err
	}
	assignee = cutRunes(oneLine(assignee), MaxNameLen)
	var out Task
	err := s.tx(ctx, func(x dbx) error {
		if _, err := s.profileOf(ctx, x, profileID); err != nil {
			return err
		}
		if err := s.checkScope(ctx, x, profileID, id, sessionID, actor); err != nil {
			return err
		}
		if actor.Kind == Agent {
			if _, err := s.heldBy(ctx, x, profileID, id, actor); err != nil {
				return err
			}
		}
		stamp := s.stamp()
		if _, err := x.ExecContext(ctx, `UPDATE tasks SET assignee = ?, updated_at = ? WHERE id = ?`, assignee, stamp, id); err != nil {
			return fmt.Errorf("tasks: assigning #%d: %w", id, err)
		}
		if err := s.event(ctx, x, id, stamp, actor.Label(), "assigned", "", "", assignee); err != nil {
			return err
		}
		if err := s.touchSession(ctx, x, sessionID); err != nil {
			return err
		}
		var err error
		out, err = s.sessionTaskTx(ctx, x, profileID, id)
		return err
	})
	return out, err
}
