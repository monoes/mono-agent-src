package tasks

import (
	"context"
	"fmt"
)

// BlockSessionTask flags a task waiting in Review for the operator: the scheduler's way to
// stop a task nothing can judge (a verifier that keeps ending without a verdict). The task
// stays in Review, the flag is set, and the note goes in its history and the session's. It is
// the operator's or an unbound agent's (the scheduler's) to do; an agent bound to a task is
// refused. A task that is not in Review, is blocked already or is verified is left alone.
func (s *Store) BlockSessionTask(ctx context.Context, profileID string, sessionID, id int64, note string, actor Actor) (Task, error) {
	if err := actorIn(actor, sessionID); err != nil {
		return Task{}, err
	}
	if err := noPicking(actor, "block a task"); err != nil {
		return Task{}, err
	}
	note = cutBytes(cleanText(note), MaxCommentBytes)
	if note == "" {
		return Task{}, invalid("blocking a task needs a note: the operator reads it")
	}
	var out Task
	err := s.tx(ctx, func(x dbx) error {
		if _, err := s.profileOf(ctx, x, profileID); err != nil {
			return err
		}
		se, err := s.openSession(ctx, x, profileID, sessionID)
		if err != nil {
			return err
		}
		if err := s.checkScope(ctx, x, profileID, id, sessionID, actor); err != nil {
			return err
		}
		cur, err := s.getTx(ctx, x, profileID, id)
		if err != nil {
			return err
		}
		if cur.Status != StatusReview || cur.Blocked || cur.Verified {
			return invalid("task #%d is %s: only a task in Review that is not blocked or verified is blocked", id, cur.Status)
		}
		stamp := s.stamp()
		if _, err := x.ExecContext(ctx, `UPDATE tasks SET blocked = 1, updated_at = ? WHERE id = ?`, stamp, id); err != nil {
			return fmt.Errorf("tasks: blocking #%d: %w", id, err)
		}
		if err := s.event(ctx, x, id, stamp, actor.Label(), "blocked", string(StatusReview), string(StatusReview), note); err != nil {
			return err
		}
		if err := s.sessionEvent(ctx, x, se.ID, actor, "blocked", fmt.Sprintf("#%d %s", cur.ID, cur.Title)); err != nil {
			return err
		}
		if err := s.touchSession(ctx, x, sessionID); err != nil {
			return err
		}
		out, err = s.sessionTaskTx(ctx, x, profileID, id)
		return err
	})
	return out, err
}
