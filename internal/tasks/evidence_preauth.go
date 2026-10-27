package tasks

import "context"

// How the caller must stand to the task for the git check to be worth running.
const (
	authNone           = iota // scope and session state only
	authHolder                // the task must be held by the actor (a finish)
	authHolderOrReview        // held by the actor, or in Review (an artifact attach)
)

// authorizedForEvidence reads, without the write lock, what the write transaction will refuse a
// caller for before it looks at evidence: the actor's binding and board (checkScope), the
// session state for an agent (writableBy, when what is not empty) and the claim. The git check
// costs up to the evidence budget, so a caller who will be refused anyway never starts it. A
// false answer only skips the check: the transaction raises the real refusal (and a caller that
// becomes authorized in between meets a retryable "changed while checked").
func (s *Store) authorizedForEvidence(ctx context.Context, profileID string, session, id int64, actor Actor, what string, mode int) bool {
	err := s.snapshot(ctx, func(x dbx) error {
		if _, err := s.profileOf(ctx, x, profileID); err != nil {
			return err
		}
		if err := s.checkScope(ctx, x, profileID, id, session, actor); err != nil {
			return err
		}
		if session != 0 && what != "" {
			se, err := s.sessionTx(ctx, x, profileID, session)
			if err != nil {
				return err
			}
			if err := writableBy(se, actor, what); err != nil {
				return err
			}
		}
		switch mode {
		case authHolder:
			_, err := s.heldBy(ctx, x, profileID, id, actor)
			return err
		case authHolderOrReview:
			cur, err := s.getTx(ctx, x, profileID, id)
			if err != nil {
				return err
			}
			if actor.Kind == Agent && cur.Status != StatusReview && (cur.Status != StatusInProgress || cur.Claim == nil || cur.Claim.By != actor.Label()) {
				return notClaimant("task #%d is not held by %s and not in Review", id, actor.Label())
			}
		}
		return nil
	})
	return err == nil
}

// precheckAuthorized is precheckEvidence for a caller who passes authorizedForEvidence.
func (s *Store) precheckAuthorized(ctx context.Context, profileID string, session, id int64, actor Actor, what string, mode int) *evidenceVerdict {
	if s.checker == nil || !s.authorizedForEvidence(ctx, profileID, session, id, actor, what, mode) {
		return nil
	}
	return s.precheckEvidence(ctx, profileID, session, id)
}
