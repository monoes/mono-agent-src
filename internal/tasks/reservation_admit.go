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
// the session and every budgeted ancestor have the room. Sessions with no budget on the way are not limited.
func AdmitHold(ctx context.Context, x Queryer, profileID string, sessionID int64, carve float64) (string, error) {
	s := NewStore(nil)
	c, err := s.commitmentTx(ctx, x, profileID, sessionID)
	if err != nil {
		return "", err
	}
	amount, _, err := s.grantFor(ctx, x, profileID, sessionID, carve, c)
	if err != nil {
		if errors.Is(err, ErrLimit) {
			return err.Error(), nil
		}
		return "", err
	}
	if toMicros(amount) < toMicros(carve) {
		return fmt.Sprintf("session #%d and the sessions above it leave $%.4f, less than the $%.4f asked", sessionID, amount, carve), nil
	}
	return "", nil
}

// Overcommitted says why the session or a budgeted ancestor is committed past its budget, or "". It is the check
// after a hold was written in the same transaction: a statement that took a carve calls it, and rolls back on a reason.
func Overcommitted(ctx context.Context, x Queryer, profileID string, sessionID int64) (string, error) {
	s := NewStore(nil)
	seen := map[int64]bool{}
	for cur, step := sessionID, 0; ; step++ {
		if seen[cur] || step > HardMaxSessionDepth {
			return "", invalid("session #%d is in a cycle of parents or deeper than %d levels", sessionID, HardMaxSessionDepth)
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
