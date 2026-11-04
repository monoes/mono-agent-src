package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// The coder host carves a child session's budget out of a mission's session. A carve is a hold on that session, so
// it is admitted by the rules a worker reservation is: the whole commitment of the session (its spend, worker
// reservations, task-session children at any depth, the host's own child holds) and that of every budgeted
// ancestor must carry it. These two functions run inside the caller's transaction, on the connection that holds
// the write lock, and use the same walk and the same ancestor logic as Reserve.

// AdmitHold says why a hold of carve dollars may not be added to the session (a carve, or a raise of one), or "" when
// the session and every budgeted ancestor have the room. Sessions with no budget on the way are not limited. The
// $0.01 minimum of a worker reservation does not apply: a carve is checked against the room, whatever its size.
func AdmitHold(ctx context.Context, x Queryer, profileID string, sessionID int64, carve float64) (string, error) {
	c, err := ReadCommitment(ctx, x, profileID, sessionID)
	if err != nil {
		return "", err
	}
	return AdmitHoldWith(ctx, x, profileID, sessionID, carve, c)
}

// ReadCommitment is Store.Commitment on the caller's own transaction: the way for another store to read the
// commitment of a session in the same snapshot as its other budget reads, and hand it to AdmitHoldWith or
// AdmitRaiseWith, so one decision walks the tree once.
func ReadCommitment(ctx context.Context, x Queryer, profileID string, sessionID int64) (Commitment, error) {
	return NewStore(nil).commitmentTx(ctx, x, profileID, sessionID)
}

// AdmitHoldWith is AdmitHold for a caller that has read the commitment c of the session already, in the same
// transaction: it does not read it again.
func AdmitHoldWith(ctx context.Context, x Queryer, profileID string, sessionID int64, carve float64, c Commitment) (string, error) {
	return NewStore(nil).fits(ctx, x, profileID, sessionID, toMicros(carve), c)
}

// AdmitRaise is AdmitHold for a raise of the cap of the coder child with the conversation conv. A child that is also
// a task session below this one contributes the larger of its cap and its task hold, so the hold to admit is the
// change of that contribution (nothing when the task hold covers the raise); a child that is not linked is a new hold.
func AdmitRaise(ctx context.Context, x Queryer, profileID string, sessionID int64, conv string, raise float64) (string, error) {
	c0, err := ReadCommitment(ctx, x, profileID, sessionID)
	if err != nil {
		return "", err
	}
	return AdmitRaiseWith(ctx, x, profileID, sessionID, conv, raise, c0)
}

// AdmitRaiseWith is AdmitRaise for a caller that has read the commitment c0 of the session already, in the same
// transaction.
func AdmitRaiseWith(ctx context.Context, x Queryer, profileID string, sessionID int64, conv string, raise float64, c0 Commitment) (string, error) {
	var linked int64
	err := x.QueryRowContext(ctx, `SELECT child_session_id FROM v_mission_child_linked WHERE profile_id = ? AND conversation_id = ? AND session_id = ? LIMIT 1`, profileID, conv, sessionID).Scan(&linked)
	if errors.Is(err, sql.ErrNoRows) {
		return AdmitHoldWith(ctx, x, profileID, sessionID, raise, c0)
	}
	if err != nil {
		return "", fmt.Errorf("tasks: reading the link of conversation %s: %w", echo(conv), err)
	}
	s := NewStore(nil)
	c1, err := s.commitWalkWith(ctx, x, profileID, sessionID, 1, map[int64]bool{sessionID: true}, hypo{capConv: conv, capDelta: raise})
	if err != nil {
		return "", err
	}
	delta := toMicros(c1.Total()) - toMicros(c0.Total())
	if delta <= 0 {
		return "", nil
	}
	return s.fits(ctx, x, profileID, sessionID, delta, c0)
}

// fits says why a hold of g micro-dollars does not fit the session (whose commitment is c) and its budgeted ancestors.
func (s *Store) fits(ctx context.Context, x dbx, profileID string, sessionID int64, g int64, c Commitment) (string, error) {
	if own, budgeted := c.leftMicros(); budgeted && own < g {
		return fmt.Sprintf("session #%d has $%.4f left of $%.2f (spent $%.4f, held $%.4f, children $%.4f)", sessionID, fromMicros(own), c.Budget, c.Spent, c.Held, c.Children), nil
	}
	grant, _, err := s.ancestorGrant(ctx, x, profileID, sessionID, g)
	if err != nil {
		if errors.Is(err, ErrLimit) {
			return err.Error(), nil
		}
		return "", err
	}
	if grant < g {
		return fmt.Sprintf("session #%d: a budgeted ancestor leaves $%.4f, less than the $%.4f asked", sessionID, fromMicros(grant), fromMicros(g)), nil
	}
	return "", nil
}

// Overcommitted says why the session or a budgeted ancestor is committed past its budget, or "". It is the check
// after a hold was written in the same transaction: a statement that took a carve calls it, and rolls back on a reason.
func Overcommitted(ctx context.Context, x Queryer, profileID string, sessionID int64) (string, error) {
	return NewStore(nil).Overcommitted(ctx, x, profileID, sessionID)
}

// Overcommitted is the package function with the store's depth policy, so a charter tree is walked to its depth.
func (s *Store) Overcommitted(ctx context.Context, x Queryer, profileID string, sessionID int64) (string, error) {
	seen := map[int64]bool{}
	maxDepth := s.walkLimit(ctx, sessionID)
	for cur, step := sessionID, 0; ; step++ {
		if seen[cur] || step > maxDepth {
			return "", invalid("session #%d is in a cycle of parents or deeper than %d levels", sessionID, maxDepth)
		}
		seen[cur] = true
		c, err := s.commitmentTx(ctx, x, profileID, cur)
		if err != nil {
			return "", err
		}
		if c.Budget > 0 && toMicros(c.Total()) > floorMicros(min(c.Budget, MaxBudgetUSD)) {
			return fmt.Sprintf("session #%d would be committed $%.4f of its budget of $%.2f (spent $%.4f, held $%.4f, children $%.4f)", cur, c.Total(), c.Budget, c.Spent, c.Held, c.Children), nil
		}
		var parent sql.NullInt64
		if err := x.QueryRowContext(ctx, `SELECT parent_session_id FROM task_sessions WHERE profile_id = ? AND id = ?`, profileID, cur).Scan(&parent); err != nil {
			return "", sessionLookup(err, cur)
		}
		if !parent.Valid {
			return "", nil
		}
		cur = parent.Int64
	}
}
