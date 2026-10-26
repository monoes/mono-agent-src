package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// bound refuses an actor that is bound to another session than the one it asks to
// act in (0 is the personal board): a worker's token names one session, and that is
// all it may touch.
func bound(actor Actor, session int64) error {
	if actor.Session == 0 || actor.Session == session {
		return nil
	}
	where := "the personal board"
	if session != 0 {
		where = fmt.Sprintf("session #%d", session)
	}
	return bindingRefused("this agent is bound to session #%d: it cannot act on %s", actor.Session, where)
}

// boundTask refuses an actor that is bound to one task when it acts on another.
func boundTask(actor Actor, id int64) error {
	if actor.Verifier && actor.Task == 0 {
		return bindingRefused("this agent is a verifier bound to no task: it acts on none")
	}
	if actor.Task == 0 || actor.Task == id {
		return nil
	}
	return bindingRefused("this agent is bound to task #%d: it cannot act on task #%d", actor.Task, id)
}

// noPicking refuses an actor bound to one task what has no task id: taking whatever is next,
// and adding tasks. A worker does the task it was given.
func noPicking(actor Actor, what string) error {
	if actor.Task == 0 {
		return nil
	}
	return bindingRefused("this agent is bound to task #%d: it cannot %s", actor.Task, what)
}

// noVerifier refuses a verifier's binding what only the executor of a task does: taking it,
// handing it back, splitting it.
func noVerifier(actor Actor, what string) error {
	if !actor.Verifier {
		return nil
	}
	return bindingRefused("this agent is bound to verify task #%d: it cannot %s", actor.Task, what)
}

// sessionIDOf is the session a task belongs to (0 for a task of the personal
// board) and whether the task exists in the profile.
func sessionIDOf(ctx context.Context, x dbx, profileID string, id int64) (int64, bool, error) {
	var session sql.NullInt64
	err := x.QueryRowContext(ctx, `SELECT session_id FROM tasks WHERE profile_id = ? AND id = ?`, profileID, id).Scan(&session)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("tasks: reading the session of #%d: %w", id, err)
	}
	return session.Int64, true, nil
}

// checkScope says whether the actor may work on task id as a task of session (0: the
// personal board): the actor's binding must allow that board, and the task must be on
// it. A task of another board is not found from a session; from the personal board it
// is refused with the session it belongs to, so that the caller learns where to work it.
func (s *Store) checkScope(ctx context.Context, x dbx, profileID string, id, session int64, actor Actor) error {
	if err := bound(actor, session); err != nil {
		return err
	}
	if err := boundTask(actor, id); err != nil {
		return err
	}
	have, ok, err := sessionIDOf(ctx, x, profileID, id)
	if err != nil {
		return err
	}
	switch {
	case !ok:
		return notFoundTask(id)
	case have == session:
		return nil
	case session != 0:
		return fmt.Errorf("%w: #%d is not a task of session #%d", ErrNotFound, id, session)
	}
	return invalid("task #%d belongs to session #%d: work it through the session (--session %d)", id, have, have)
}

// bumpSession moves a session's board revision.
func (s *Store) bumpSession(ctx context.Context, x dbx, sessionID int64) error {
	_, err := x.ExecContext(ctx,
		`INSERT INTO task_session_rev (session_id, rev) VALUES (?, 1) ON CONFLICT(session_id) DO UPDATE SET rev = rev + 1`, sessionID)
	if err != nil {
		return fmt.Errorf("tasks: bumping the session revision: %w", err)
	}
	return nil
}

// bumpScope moves the revision of the board a write was made on: the profile's for the
// personal board (session 0), else the session's.
func (s *Store) bumpScope(ctx context.Context, x dbx, profileID string, session int64) error {
	if session == 0 {
		return s.bump(ctx, x, profileID)
	}
	return s.bumpSession(ctx, x, session)
}

// bumpSessionsOf moves the revision of every session that the tasks named belong to,
// once each: the operator's verbs of the personal board (edit, move, archive) also act on
// a session's tasks, and a watcher of the session must see that.
func (s *Store) bumpSessionsOf(ctx context.Context, x dbx, ids ...int64) error {
	seen := map[int64]bool{}
	for _, id := range ids {
		var session sql.NullInt64
		err := x.QueryRowContext(ctx, `SELECT session_id FROM tasks WHERE id = ?`, id).Scan(&session)
		if errors.Is(err, sql.ErrNoRows) || err == nil && !session.Valid || err == nil && seen[session.Int64] {
			continue
		}
		if err != nil {
			return fmt.Errorf("tasks: reading the session of #%d: %w", id, err)
		}
		seen[session.Int64] = true
		if err := s.bumpSession(ctx, x, session.Int64); err != nil {
			return err
		}
	}
	return nil
}

func notFoundTask(id int64) error { return fmt.Errorf("%w: #%d", ErrNotFound, id) }
