package tasks

import (
	"context"
	"time"
)

// SessionOfTask is the session a task belongs to, 0 for a task of the personal board. The
// profile is not checked: an unknown task is ErrNotFound.
func (s *Store) SessionOfTask(ctx context.Context, profileID string, id int64) (int64, error) {
	var session int64
	err := s.snapshot(ctx, func(x dbx) error {
		have, ok, err := sessionIDOf(ctx, x, profileID, id)
		if err != nil {
			return err
		}
		if !ok {
			return notFoundTask(id)
		}
		session = have
		return nil
	})
	return session, err
}

// The Any verbs are what the CLI and the MCP tools call for a task named by its number
// alone: a task of a session is worked as a task of that session (its state, dependencies
// and the actor's binding apply), any other as a task of the personal board.

// ClaimAny is Claim, or SessionClaim for a task of a session.
func (s *Store) ClaimAny(ctx context.Context, profileID string, id int64, actor Actor, lease time.Duration) (Task, error) {
	session, err := s.SessionOfTask(ctx, profileID, id)
	if err != nil || session == 0 {
		if err != nil {
			return Task{}, err
		}
		return s.Claim(ctx, profileID, id, actor, lease)
	}
	return s.SessionClaim(ctx, profileID, session, id, actor, lease)
}

// CommentAny is Comment, or SessionComment for a task of a session.
func (s *Store) CommentAny(ctx context.Context, profileID string, id int64, text string, actor Actor) (Task, error) {
	session, err := s.SessionOfTask(ctx, profileID, id)
	if err != nil {
		return Task{}, err
	}
	return s.commentIn(ctx, profileID, session, id, text, actor)
}

// FinishAny is Finish, or SessionFinish for a task of a session.
func (s *Store) FinishAny(ctx context.Context, profileID string, id int64, o Outcome, actor Actor) (Task, error) {
	session, err := s.SessionOfTask(ctx, profileID, id)
	if err != nil {
		return Task{}, err
	}
	return s.finishIn(ctx, profileID, session, id, o, actor)
}

// ReleaseAny is Release, or SessionRelease for a task of a session.
func (s *Store) ReleaseAny(ctx context.Context, profileID string, id int64, note string, actor Actor) (Task, error) {
	session, err := s.SessionOfTask(ctx, profileID, id)
	if err != nil {
		return Task{}, err
	}
	return s.releaseIn(ctx, profileID, session, id, note, actor)
}
