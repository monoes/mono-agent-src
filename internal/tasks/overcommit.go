package tasks

import (
	"context"
	"fmt"
)

// OverCommit says whether a session, or a budgeted ancestor of it, is committed past its budget (its own spend, the
// reservations held on it and what its child sessions hold add up to more than it has), and by how much. Every number is
// host-written money; nothing in it is text an agent wrote. A cap or reservation run on the session is refused while Over.
type OverCommit struct {
	Over         bool    `json:"over"`
	Session      int64   `json:"session_id"`    // the session that was asked about
	Ancestor     int64   `json:"by_session_id"` // the over-committed session: Session itself, or the nearest budgeted ancestor that is over
	BudgetUSD    float64 `json:"budget_usd"`    // its budget
	CommittedUSD float64 `json:"committed_usd"` // spent + held + children
	OverByUSD    float64 `json:"over_by_usd"`   // committed less budget
	SpentUSD     float64 `json:"spent_usd"`
	HeldUSD      float64 `json:"held_usd"`
	ChildrenUSD  float64 `json:"children_usd"`
}

// overCommitOf is the one test of "committed past the budget" (in whole micro-dollars, the budget floored and the total
// rounded, as the reservation walk does), for the session `by` asked about as `asked`.
func overCommitOf(asked, by int64, c Commitment) OverCommit {
	oc := OverCommit{Session: asked}
	bm := floorMicros(min(c.Budget, MaxBudgetUSD))
	t := toMicros(c.Total())
	if c.Budget <= 0 || t <= bm {
		return oc
	}
	oc.Over, oc.Ancestor = true, by
	oc.BudgetUSD, oc.CommittedUSD, oc.OverByUSD = c.Budget, fromMicros(t), fromMicros(t-bm)
	oc.SpentUSD, oc.HeldUSD, oc.ChildrenUSD = c.Spent, c.Held, c.Children
	return oc
}

// Words is the short, factual line for the lead, the operator and the CLI: ids and dollars only, no text an agent wrote.
func (oc OverCommit) Words() string {
	return fmt.Sprintf("over-committed: session #%d's budget of $%.2f is exceeded by $%.2f (spent $%.2f, held $%.2f, children $%.2f): the operator raises it to resume",
		oc.Ancestor, oc.BudgetUSD, oc.OverByUSD, oc.SpentUSD, oc.HeldUSD, oc.ChildrenUSD)
}

// OverCommitError is the refusal of a grant under an over-committed ancestor. It matches ErrLimit and carries the numbers.
type OverCommitError struct{ OverCommit }

func (e *OverCommitError) Error() string {
	return fmt.Sprintf("%v: session #%d: its ancestor #%d is over its budget of $%.2f (spent $%.4f, held $%.4f, children $%.4f)",
		ErrLimit, e.Session, e.Ancestor, e.BudgetUSD, e.SpentUSD, e.HeldUSD, e.ChildrenUSD)
}

func (e *OverCommitError) Unwrap() error { return ErrLimit }

// OverCommitOf reads, in one snapshot and without writing, whether the session or a budgeted ancestor is over-committed.
// It uses the same commitment walk the reservation and cap checks use (Overcommitted / ancestorGrant), so it answers the
// question those refuse on.
func (s *Store) OverCommitOf(ctx context.Context, profileID string, sessionID int64) (OverCommit, error) {
	var out OverCommit
	err := s.snapshot(ctx, func(x dbx) error {
		if _, err := s.profileOf(ctx, x, profileID); err != nil {
			return err
		}
		var err error
		out, err = s.overCommitWalk(ctx, x, profileID, sessionID)
		return err
	})
	return out, err
}
