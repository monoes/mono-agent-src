package doomsday

import (
	"context"

	"github.com/monoes/mono-agent/internal/doomsday/ops"
)

// finish.begin as the boss's typed operation (note 13.6 A3). The body is FinishEngine.FinishBegin; what makes it the boss's
// is the enforcement function of the ops package (BeginFinish): the live charter, the verified caller. The MCP tool
// session_boss_finish_begin calls BossFinishBegin through an installer set only by a build with the mode.

// finishBeginner adapts the engine's report to the ops package's result.
type finishBeginner struct{ e *FinishEngine }

func (f finishBeginner) FinishBegin(ctx context.Context) (ops.FinishBeginResult, error) {
	b, err := f.e.FinishBegin(ctx)
	if err != nil {
		return ops.FinishBeginResult{}, err
	}
	return ops.FinishBeginResult{Generation: b.Generation, Accepted: b.Accepted, Refused: b.Refused, State: b.State, Lines: b.Lines}, nil
}

// BossFinishBegin runs finish.begin for the verified lead session. With no live charter (no running run) it is refused as
// ops.ErrNoCharter and nothing is written.
func (w Wiring) BossFinishBegin(ctx context.Context, session int64) (ops.FinishBeginResult, error) {
	o := w.Ops(nil)
	act, ok, err := o.Adapter.Active(ctx, w.Profile)
	if err != nil {
		return ops.FinishBeginResult{}, err
	}
	if !ok {
		return ops.FinishBeginResult{}, ops.ErrNoCharter
	}
	req := ops.Request{Profile: w.Profile, CharterID: act.ID, Hash: act.Hash, Generation: act.Generation, Op: ops.OpFinishBegin,
		Caller: ops.Caller{Session: session}, Addressed: session}
	return o.Enforcer.BeginFinish(ctx, req, finishBeginner{w.FinishEngine()})
}
