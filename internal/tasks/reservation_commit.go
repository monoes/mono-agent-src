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
}

// Total is the money committed: spent + held + children.
func (c Commitment) Total() float64 { return c.Spent + c.Held + c.Children }

// Left is what is still free under the budget, never below 0. bounded is false when the session has no
// budget, in which case there is no limit and left is 0.
func (c Commitment) Left() (left float64, bounded bool) {
	if c.Budget <= 0 {
		return 0, false
	}
	if l := c.Budget - c.Total(); l > 0 {
		return l, true
	}
	return 0, true
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
	return s.commitWalk(ctx, x, profileID, id, 1, map[int64]bool{id: true})
}

func (s *Store) commitWalk(ctx context.Context, x dbx, profileID string, id int64, depth int, seen map[int64]bool) (Commitment, error) {
	var c Commitment
	if err := x.QueryRowContext(ctx, `SELECT budget_usd, spent_usd FROM task_sessions WHERE profile_id = ? AND id = ?`, profileID, id).Scan(&c.Budget, &c.Spent); err != nil {
		return c, sessionLookup(err, id)
	}
	if err := x.QueryRowContext(ctx, `SELECT COALESCE(SUM(MAX(amount_usd - charged_usd, 0)), 0) FROM task_session_reservations
		WHERE profile_id = ? AND session_id = ? AND state IN ('live', 'expired')`, profileID, id).Scan(&c.Held); err != nil {
		return c, fmt.Errorf("tasks: reading the reservations of session #%d: %w", id, err)
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
		kc, err := s.commitWalk(ctx, x, profileID, k.id, depth+1, seen)
		if err != nil {
			return c, err
		}
		hold := kc.Total()
		if k.open && k.budget > hold {
			hold = k.budget
		}
		c.Children += hold
	}
	return c, nil
}
