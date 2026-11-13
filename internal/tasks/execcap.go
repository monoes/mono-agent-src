package tasks

import (
	"context"
	"fmt"
)

// ExecCap is what one cap-mode run on a session may still spend: the smaller of the session's own remainder
// (its Commitment left) and what every budgeted ancestor allows, which is the largest grant a reservation on
// the session could get (grantFor with no wanted amount), read in one snapshot. An ancestor that is already
// over its budget (its children's budgets add up to more than it has) allows nothing and the call is refused
// with ErrLimit: a cap run never gets more than a reservation would. bounded is false only when neither the
// session nor an ancestor has a budget. The cap run still holds nothing, so concurrent runs can each spend up
// to this number (the documented limit of cap mode).
func (s *Store) ExecCap(ctx context.Context, profileID string, sessionID int64) (left float64, bounded bool, err error) {
	err = s.snapshot(ctx, func(x dbx) error {
		if _, err := s.profileOf(ctx, x, profileID); err != nil {
			return err
		}
		c, err := s.commitmentTx(ctx, x, profileID, sessionID)
		if err != nil {
			return err
		}
		g := int64(toMicros(MaxBudgetUSD))
		own, ownBudgeted := c.leftMicros()
		if ownBudgeted {
			g = own
		}
		grant, anc, err := s.ancestorGrant(ctx, x, profileID, sessionID, g)
		if err != nil {
			return err
		}
		if !ownBudgeted && !anc {
			return nil
		}
		bounded = true
		if grant < 0 {
			return fmt.Errorf("tasks: negative grant for session #%d", sessionID)
		}
		left = fromMicros(grant)
		return nil
	})
	return left, bounded, err
}
