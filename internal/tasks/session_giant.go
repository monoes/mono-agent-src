package tasks

import (
	"context"
	"fmt"
)

// GiantFacts is what the person is shown about a draft session that waits for their approval: why
// it is giant work, and the numbers the reasons are made of.
type GiantFacts struct {
	Reasons     []string // giantReasons now; empty when the session is no longer giant
	TaskCount   int
	BudgetUSD   float64
	BudgetShare float64 // of what the mission's root has left; 0 when the root has no budget
}

// GiantFactsOf reads the facts of a session for the operator's view of a person item. It writes
// nothing and is the operator's: an agent is told nothing about why work is giant.
func (s *Store) GiantFactsOf(ctx context.Context, profileID string, id int64, actor Actor) (GiantFacts, error) {
	if actor.Kind != Human {
		return GiantFacts{}, operatorOnly("read why a session is giant work")
	}
	var out GiantFacts
	err := s.snapshot(ctx, func(x dbx) error {
		se, err := s.sessionTx(ctx, x, profileID, id)
		if err != nil {
			return err
		}
		out.TaskCount, out.BudgetUSD = se.Counts.Stored(), se.BudgetUSD
		if se.ParentSessionID == 0 {
			return nil // a root is the operator's own approval: it is never giant
		}
		parent, root, err := s.approvalChain(ctx, x, profileID, se)
		if err != nil {
			return err
		}
		var pol ApprovalPolicy
		if s.approval != nil && root.ConversationID != "" {
			if pol, err = s.approval.ApprovalPolicy(ctx, profileID, root.ConversationID); err != nil {
				return fmt.Errorf("tasks: reading the mission's approval settings: %w", err)
			}
		}
		out.Reasons = giantReasons(se, parent, root, pol)
		if left := root.BudgetUSD - root.SpentUSD; root.BudgetUSD > 0 && left > 0 {
			out.BudgetShare = se.BudgetUSD / left
		}
		return nil
	})
	return out, err
}
