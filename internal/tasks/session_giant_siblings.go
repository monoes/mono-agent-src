package tasks

import (
	"context"
	"fmt"
)

// withSiblingsCommitted returns the root as the share rule must see it: what the parent's other
// children already hold is taken off what the root has left, so siblings that each take 40% of
// what is left cannot together exceed the root. A sibling that is not a draft holds its budget
// while it runs or is paused, and what it spent once it is over. Drafts hold nothing. The amount
// is added to the copy's SpentUSD, the field the rule subtracts; nothing is written.
func (s *Store) withSiblingsCommitted(ctx context.Context, x dbx, se, root Session) (Session, error) {
	if se.ParentSessionID == 0 {
		return root, nil
	}
	held, err := heldByChildren(ctx, x, se.ParentSessionID, se.ID, `state <> 'draft'`)
	if err != nil {
		return Session{}, err
	}
	root.SpentUSD += held
	return root, nil
}

// heldByChildren is what the children of parent hold of its budget: the whole budget of a child
// that can still spend (draft, running, paused), what it spent once it is over. A child's spend
// never reaches its parent's spent_usd, so counting only the open children would return a closed
// child's budget to the pool though it was spent. except is a child to leave out (0: none); where
// narrows the children (a SQL condition on task_sessions; archived children are included unless it
// says otherwise).
func heldByChildren(ctx context.Context, x dbx, parent, except int64, where string) (float64, error) {
	var held float64
	err := x.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN state IN `+openChildStates+` THEN budget_usd ELSE spent_usd END), 0)
		FROM task_sessions WHERE parent_session_id = ? AND id <> ? AND `+where, parent, except).Scan(&held)
	if err != nil {
		return 0, fmt.Errorf("tasks: reading the budgets of session #%d's children: %w", parent, err)
	}
	return held, nil
}
