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
	var held float64
	err := x.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN state IN ('running','paused') THEN budget_usd ELSE spent_usd END), 0)
		FROM task_sessions WHERE parent_session_id = ? AND id <> ? AND state <> 'draft'`, se.ParentSessionID, se.ID).Scan(&held)
	if err != nil {
		return Session{}, fmt.Errorf("tasks: reading the budgets of session #%d's siblings: %w", se.ID, err)
	}
	root.SpentUSD += held
	return root, nil
}
