package ops

import (
	"context"
	"fmt"
)

// finish.begin (note 13.6 sections 7.1 and 7.2, requirement A3) is a typed operation of the boss that has no approval and no
// per-ask limit: it spends no counter and a charter does not "lift" it. Its enforcement function is this check (the live
// charter, the verified caller) followed by the finish's own preconditions, which the Finisher evaluates and records
// (a refused call writes a `denied-attempt` row and changes nothing).

// FinishBeginResult is what the finish's begin reports back to the boss.
type FinishBeginResult struct {
	Generation int64
	Accepted   bool
	Refused    string // why the preconditions were not met (Accepted is false)
	State      string // the run's state after the stop that begins the finish
	Lines      []string
}

// Finisher is the body of finish.begin (internal/cli/doomsday FinishEngine.FinishBegin, behind an adapter).
type Finisher interface {
	FinishBegin(ctx context.Context) (FinishBeginResult, error)
}

// BeginFinish runs finish.begin for the verified caller. Like Decide it refuses before it reads anything else when there is
// no live charter, or the request names another charter, generation or profile than the live one, or the caller is not the
// session the call is addressed to. An operation other than finish.begin is not decided here.
func (e *Enforcer) BeginFinish(ctx context.Context, r Request, f Finisher) (FinishBeginResult, error) {
	if r.Op != OpFinishBegin {
		return FinishBeginResult{}, fmt.Errorf("%w: BeginFinish only runs %s", ErrNotAllowlisted, OpFinishBegin)
	}
	act, ok, err := e.Charters.Active(ctx, r.Profile)
	if err != nil {
		return FinishBeginResult{}, err
	}
	if !ok {
		return FinishBeginResult{}, ErrNoCharter
	}
	if act.Hash != r.Hash || act.ID != r.CharterID || act.Generation != r.Generation || act.Profile != r.Profile || act.Hash == "" {
		return FinishBeginResult{}, ErrCharterStale
	}
	if r.Caller.Session == 0 || r.Caller.Session != r.Addressed {
		return FinishBeginResult{}, ErrCallerMismatch
	}
	if err := e.requireBoss(ctx, act, r.Caller.Session, true); err != nil {
		return FinishBeginResult{}, err
	}
	if f == nil {
		return FinishBeginResult{}, fmt.Errorf("%w: no finish is wired", ErrNotEnabled)
	}
	return f.FinishBegin(ctx)
}
