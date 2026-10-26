package tasks

import (
	"context"
	"errors"
)

// errClaimMoved is the claim the caller meant to give back no longer being the one held.
var errClaimMoved = errors.New("tasks: the claim changed")

// SessionReleaseIfHeld is SessionRelease, but only while the task is still held by the
// actor under the claim read earlier (the same lease end): the compare and the release
// are one transaction, so a claim that was released and taken again under the same name
// since (a restarted turn's worker) is not given back on the strength of a stale read.
// It returns false, and changes nothing, when the claim is not the one given; that is not
// an error.
func (s *Store) SessionReleaseIfHeld(ctx context.Context, profileID string, sessionID, id int64, held Claim, note string, actor Actor) (bool, error) {
	if err := needName(actor); err != nil {
		return false, err
	}
	_, err := s.handBack(ctx, profileID, sessionID, id, actor, StatusReady, "released", cutBytes(cleanText(note), MaxCommentBytes), &held)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, errClaimMoved), errors.Is(err, ErrNotClaimant):
		return false, nil
	}
	return false, err
}
