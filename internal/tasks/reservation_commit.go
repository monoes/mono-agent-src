package tasks

import (
	"context"
	"fmt"
)

// budgetEps is the tolerance of every budget comparison (the same 1e-9 the coder host's missionBudgetRefusal uses).
const budgetEps = 1e-9

// Commitment is what a session's budget is already spoken for by: its own spend, the unspent part of the
// reservations held on it, and what its child sessions hold. It is derived from the rows on every call, never
// stored, so it cannot drift from them.
type Commitment struct {
	Budget   float64 // the session's budget (0: none set)
	Spent    float64
	Held     float64 // the unspent part of its live and expired reservations
	Children float64 // what its child sessions hold, at any depth
	// ChildCommitted is what the child sessions have really spoken for, at any depth: their spend and reservations,
	// without the unspent part of their budgets. It is what lowering this session's budget cannot go below (a
	// lowered budget clamps the children's allowances, it cannot take back money they spent or reserved).
	ChildCommitted float64
}

// Total is the money committed: spent + held + children.
func (c Commitment) Total() float64 { return c.Spent + c.Held + c.Children }

// Floor is the least a budget can be set to: spent + held + what the children have really spoken for.
func (c Commitment) Floor() float64 { return c.Spent + c.Held + c.ChildCommitted }

// Left is what is still free under the budget, never below 0. A budget above MaxBudgetUSD (a legacy row; none is
// accepted now) counts as MaxBudgetUSD, so that reserve-all grants all that remains of the clamped budget. bounded is false when the session has no
// budget, in which case there is no limit and left is 0.
func (c Commitment) Left() (left float64, bounded bool) {
	if c.Budget <= 0 {
		return 0, false
	}
	if l := min(c.Budget, MaxBudgetUSD) - c.Total(); l > 0 {
		return l, true
	}
	return 0, true
}

// leftMicros is Left in whole micro-dollars for a grant: the budget (clamped to MaxBudgetUSD) rounded DOWN, less
// the committed money rounded to the grid. Done in integers so that float noise in the difference of two large
// numbers (the spacing near 1e9 is 1.2e-7) cannot shave a micro-dollar off a remainder that is exact in decimal,
// and a remainder is never rounded up past what is really left.
func (c Commitment) leftMicros() (left int64, bounded bool) {
	if c.Budget <= 0 {
		return 0, false
	}
	return max(floorMicros(min(c.Budget, MaxBudgetUSD))-toMicros(c.Total()), 0), true
}

// Commitment reads the commitment of a session in one snapshot.
func (s *Store) Commitment(ctx context.Context, profileID string, sessionID int64) (Commitment, error) {
	var c Commitment
	err := s.snapshot(ctx, func(x dbx) error {
		if _, err := s.profileOf(ctx, x, profileID); err != nil {
			return err
		}
		var err error
		c, err = s.commitmentTx(ctx, x, profileID, sessionID)
		return err
	})
	return c, err
}

// commitmentTx is the one formula: spent + held reservations + the children's hold. A child holds the larger
// of its budget and its own commitment while it is open (it may still spend up to its budget), and only its
// commitment once closed or archived. The walk is profile-scoped, visits each session once (a revisit is a
// cycle of parents) and fails closed on a tree deeper than HardMaxSessionDepth.
func (s *Store) commitmentTx(ctx context.Context, x dbx, profileID string, id int64) (Commitment, error) {
	return s.commitWalkWith(ctx, x, profileID, id, 1, map[int64]bool{id: true}, hypo{})
}

// commitWalkWith is the walk with extra.held counted as held on extra.session, as if a reservation of that size
// were there: the way to ask what an ancestor would commit if a grant were made below it. The zero hypo adds nothing.
func (s *Store) commitWalkWith(ctx context.Context, x dbx, profileID string, id int64, depth int, seen map[int64]bool, extra hypo) (Commitment, error) {
	var c Commitment
	if err := x.QueryRowContext(ctx, `SELECT budget_usd, spent_usd FROM task_sessions WHERE profile_id = ? AND id = ?`, profileID, id).Scan(&c.Budget, &c.Spent); err != nil {
		return c, sessionLookup(err, id)
	}
	if err := x.QueryRowContext(ctx, `SELECT COALESCE(SUM(MAX(amount_usd - charged_usd, 0)), 0) FROM task_session_reservations
		WHERE profile_id = ? AND session_id = ? AND state IN ('live', 'expired')`, profileID, id).Scan(&c.Held); err != nil {
		return c, fmt.Errorf("tasks: reading the reservations of session #%d: %w", id, err)
	}
	if extra.session == id {
		c.Held += extra.held
	}
	type kid struct {
		id     int64
		budget float64
		open   bool
	}
	rows, err := x.QueryContext(ctx, `SELECT id, budget_usd, state, archived_at FROM task_sessions WHERE profile_id = ? AND parent_session_id = ? ORDER BY id`, profileID, id)
	if err != nil {
		return c, fmt.Errorf("tasks: reading the children of session #%d: %w", id, err)
	}
	var kids []kid
	for rows.Next() {
		var k kid
		var st, archived string
		if err := rows.Scan(&k.id, &k.budget, &st, &archived); err != nil {
			rows.Close()
			return c, fmt.Errorf("tasks: reading the children of session #%d: %w", id, err)
		}
		k.open = archived == "" && isOpen(SessionState(st))
		kids = append(kids, k)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return c, fmt.Errorf("tasks: reading the children of session #%d: %w", id, err)
	}
	if depth >= HardMaxSessionDepth && len(kids) > 0 {
		return c, limit("session #%d has sessions deeper than %d levels below it", id, HardMaxSessionDepth)
	}
	for _, k := range kids {
		if seen[k.id] {
			return c, invalid("session #%d is in a cycle of parents", k.id)
		}
		seen[k.id] = true
		kc, err := s.commitWalkWith(ctx, x, profileID, k.id, depth+1, seen, extra)
		if err != nil {
			return c, err
		}
		hold := kc.Total()
		if k.open && min(k.budget, MaxBudgetUSD) > hold {
			hold = min(k.budget, MaxBudgetUSD)
		}
		c.Children += hold
		c.ChildCommitted += kc.Floor()
	}
	return c, nil
}
