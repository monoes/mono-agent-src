package tasks

import "context"

// writableBy refuses an agent's write that changes a task's outcome (finish, artifact) while
// the session does not run: paused or stopped, the operator has said to stop work. A comment
// stays allowed, and so does the operator. Draft sessions hold no claim, so they are refused
// the same.
func writableBy(se Session, actor Actor, what string) error {
	if actor.Kind != Agent || (se.State == SessionRunning && !se.Archived) {
		return nil
	}
	if se.State == SessionPaused {
		return sessionNotReady("session #%d is paused: you cannot %s until the operator resumes it (a comment is still allowed)", se.ID, what)
	}
	return sessionNotReady("session #%d is %s: you cannot %s (a comment is still allowed)", se.ID, closedWord(se), what)
}

// agentWritable is writableBy for a caller that has not read the session.
func (s *Store) agentWritable(ctx context.Context, profileID string, sessionID int64, actor Actor, what string) error {
	if actor.Kind != Agent {
		return nil
	}
	return s.snapshot(ctx, func(x dbx) error {
		se, err := s.sessionTx(ctx, x, profileID, sessionID)
		if err != nil {
			return err
		}
		return writableBy(se, actor, what)
	})
}
