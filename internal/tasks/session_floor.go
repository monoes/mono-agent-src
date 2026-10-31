package tasks

import (
	"context"
	"fmt"
	"strings"
)

// budgetFloor holds a new budget to what is already spoken for, in the write that sets it (the check and the
// write are never apart, so a reservation cannot slip in between). A budget above 0 is refused when:
//   - a worker holds a reservation with no cap (bounded 0, live or expired) on the session or any session below
//     it: its cost is unknown, so no budget can be said to cover it. The operator stops the workers or resolves
//     the lapsed row first;
//   - it is below spent + reservations + what the children have spent or reserved (Commitment.Floor; the unspent
//     part of a child's budget is not counted: lowering a budget clamps the children's allowances). Pause the session to
//     halt it instead of lowering a budget under what was spent.
//
// 0 (remove the budget) and a budget restated unchanged are not floor questions.
func (s *Store) budgetFloor(ctx context.Context, x dbx, se Session, budget float64) error {
	if budget <= 0 || budget == se.BudgetUSD {
		return nil
	}
	if err := s.refuseUnboundedBelow(ctx, x, se); err != nil {
		return err
	}
	c, err := s.commitmentTx(ctx, x, se.ProfileID, se.ID)
	if err != nil {
		return err
	}
	if budget*1e6+1e-6 < float64(toMicros(c.Floor())) {
		return limit("budget $%.2f is below the $%.4f already committed on session #%d (spent $%.4f, reservations $%.4f, children $%.4f): "+
			"raise it to at least that, or pause the session to halt it", budget, c.Floor(), se.ID, c.Spent, c.Held, c.ChildCommitted)
	}
	return nil
}

// refuseUnboundedBelow refuses while a live or expired reservation with no cap sits on the session or below it,
// naming up to five of them.
func (s *Store) refuseUnboundedBelow(ctx context.Context, x dbx, se Session) error {
	rows, err := x.QueryContext(ctx, `WITH RECURSIVE sub(id, depth) AS (
			SELECT ?, 1
			UNION
			SELECT t.id, sub.depth + 1 FROM task_sessions t JOIN sub ON t.parent_session_id = sub.id
			WHERE t.profile_id = ? AND sub.depth < ?)
		SELECT session_id, worker_ref, state FROM task_session_reservations
		WHERE profile_id = ? AND bounded = 0 AND state IN ('live', 'expired') AND session_id IN (SELECT id FROM sub)
		ORDER BY id`, se.ID, se.ProfileID, HardMaxSessionDepth, se.ProfileID)
	if err != nil {
		return fmt.Errorf("tasks: reading the uncapped reservations of session #%d: %w", se.ID, err)
	}
	defer rows.Close()
	var named []string
	n := 0
	for rows.Next() {
		var sid int64
		var ref, state string
		if err := rows.Scan(&sid, &ref, &state); err != nil {
			return fmt.Errorf("tasks: reading the uncapped reservations of session #%d: %w", se.ID, err)
		}
		if n++; n <= 5 {
			named = append(named, fmt.Sprintf("worker %q on session #%d (%s)", ref, sid, state))
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("tasks: reading the uncapped reservations of session #%d: %w", se.ID, err)
	}
	if n == 0 {
		return nil
	}
	more := ""
	if n > len(named) {
		more = fmt.Sprintf(" and %d more", n-len(named))
	}
	return limit("session #%d has %d worker reservation(s) with no cap, whose cost is unknown: %s%s. Stop the workers (pause the session), or resolve a lapsed one, then set the budget",
		se.ID, n, strings.Join(named, ", "), more)
}
