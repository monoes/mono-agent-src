package coderhost

import (
	"context"
	"fmt"

	"github.com/monoes/mono-agent/internal/doomsday/ops"
	"github.com/monoes/mono-agent/internal/tasks"
)

type bossActorKey struct{}

// BossApprovePlan is the boss's approval of a child session's draft plan (plan.approve) inside the charter's per-ask
// limits. sc must be the LeadScope of the plan's parent session, from its verified token; OperatorScope is not
// accepted. The plan is approved only as read (rev and digest): a plan that changed since is refused, and a plan
// over a limit returns ErrParked and stays a draft for the person. The approval is spent before the effect (the safe
// direction), as for the other typed operations.
func (s *Store) BossApprovePlan(ctx context.Context, sc CallerScope, profileID string, session, rev int64, digest string) (tasks.Session, error) {
	r := s.doomsdayFor(ctx, profileID)
	if r == nil {
		return tasks.Session{}, ErrDoomsdayOff
	}
	sid, err := s.LeadMission(sc, profileID)
	if err != nil {
		return tasks.Session{}, err
	}
	conv := sc.ConversationID()
	actor := leadActor(conv, sid, conv)
	req, err := r.request(ctx, profileID, ops.OpPlanApprove, sid)
	if err != nil {
		return tasks.Session{}, err
	}
	req.Plan = &ops.PlanArgs{Session: session, Rev: rev, Digest: digest}
	d, err := r.Enforcer.Decide(context.WithValue(ctx, bossActorKey{}, actor), req)
	if err != nil {
		return tasks.Session{}, err
	}
	if d.Verdict != ops.Approved {
		return tasks.Session{}, fmt.Errorf("%w (%v)", ErrParked, d.Exceeded)
	}
	return tasks.NewStore(s.db).ApprovePlanAsBoss(ctx, profileID, session, actor, tasks.PlanGuard{Rev: rev, Digest: digest})
}
