package tasks

import (
	"context"
	"math"
)

// ApprovalBoss is the approval basis of a plan the boss of the open-ended mode approved inside the charter's per-ask
// limits (note 13.4 section 4B). Only ApprovePlanAsBoss writes it.
const ApprovalBoss = "boss"

// BossPlanFacts are the numbers of a draft's plan that the per-ask limits are checked against, read from the store at
// the effect and never from the approval or the agent. Budget and task count are read pessimistically: a plan with no
// budget, or one that may grow past what it holds, is counted at its ceiling, so it can only park, never slip under.
type BossPlanFacts struct {
	Open        bool
	Rev         int64
	Digest      string
	Tasks       int
	SpendMicro  int64
	AccessLevel string
	Depth       int // levels the plan's tree may reach below the asker
}

// PlanFactsForBoss reads the facts of a draft's plan for the verified lead of its parent session (the boss). The
// actor is checked as for any agent approval (leadOfParent): the operator's settings are not read, the charter's
// limits replace them, and giantReasons is not applied because the per-ask limits are the boss's rule.
func (s *Store) PlanFactsForBoss(ctx context.Context, profileID string, id int64, actor Actor) (BossPlanFacts, error) {
	var out BossPlanFacts
	err := s.snapshot(ctx, func(x dbx) error {
		se, err := s.sessionTx(ctx, x, profileID, id)
		if err != nil {
			return notYourApproval(id)
		}
		parent, _, err := s.approvalChain(ctx, x, profileID, se)
		if err != nil {
			return err
		}
		if err := leadOfParent(se, actor, parent); err != nil {
			return err
		}
		v, err := s.planViewTx(ctx, x, se)
		if err != nil {
			return err
		}
		out = bossFacts(se, parent, v)
		return nil
	})
	return out, err
}

func bossFacts(se, parent Session, v PlanView) BossPlanFacts {
	n := max(se.Counts.Stored(), se.MaxTasks, v.TaskCount)
	spend := int64(math.MaxInt64) // no budget of its own: unbounded, so it exceeds every limit
	if se.BudgetUSD > 0 {
		spend = int64(math.Round(se.BudgetUSD * 1e6))
	}
	return BossPlanFacts{
		Open:        se.State == SessionDraft && !se.Archived,
		Rev:         v.Rev,
		Digest:      v.Digest,
		Tasks:       n,
		SpendMicro:  spend,
		AccessLevel: se.AccessLevel,
		Depth:       max(1, se.Depth-parent.Depth, se.MaxDepth),
	}
}

// ApprovePlanAsBoss approves a draft's plan for the boss, only if the plan is still the one the guard names (both the
// revision and the digest are required). The caller must already hold a spent approval of the enforcement function
// of the open-ended mode (ops.Enforcer.Decide): this method does not know the charter, and an inventory test lists
// its only caller. The actor must be the verified lead of the session's parent and nothing else.
func (s *Store) ApprovePlanAsBoss(ctx context.Context, profileID string, id int64, actor Actor, g PlanGuard) (Session, error) {
	if g.Rev == 0 || g.Digest == "" {
		return Session{}, planChanged(id, "the boss approves a plan only as read: revision and digest are both needed")
	}
	return s.sessionWrite(ctx, profileID, id, func(x dbx, se Session) error {
		if err := s.checkPlanGuard(ctx, x, se, g); err != nil {
			return err
		}
		parent, _, err := s.approvalChain(ctx, x, profileID, se)
		if err != nil {
			return err
		}
		if err := leadOfParent(se, actor, parent); err != nil {
			return err
		}
		return s.approveDraft(ctx, x, profileID, se, actor, ApprovalBoss)
	})
}
