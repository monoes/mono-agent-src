package epoch

import (
	"context"
	"errors"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/judge"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/sstore"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

// Outcome says how a tick ended.
type Outcome string

const (
	OutcomeIdle        Outcome = "idle"         // nothing to do (or held; see Held)
	OutcomeInert       Outcome = "inert"        // the run is not running (or strategy is parked): nothing new is written
	OutcomeApplied     Outcome = "applied"      // judged, verdicts applied
	OutcomeBadJudgment Outcome = "bad-judgment" // the judge failed or its output was refused; nothing else changed
	OutcomeAbandoned   Outcome = "abandoned"    // the epoch was closed unfinished
)

// Held names why a candidate trigger did not open an epoch.
type Held string

const (
	HeldFloor      Held = "floor"
	HeldHourlyCap  Held = "hourly-cap"
	HeldBackoff    Held = "backoff"
	HeldJudgePin   Held = "judge-pin"
	HeldNotRunning Held = "not-running"
	HeldRunUnknown Held = "run-state-unknown"
	HeldParked     Held = "parked"
)

// Move is one direction state change an applied verdict made.
type Move struct {
	DirectionID string
	Verdict     types.Verdict
	To          types.DirectionState
}

// TickResult is what the audit and notification layer reads. It carries ids, enums and the validated judgment, never
// raw model output. AllPlausiblyMet is the judge's opinion only: the engine never requests or decides a finish.
type TickResult struct {
	Outcome         Outcome
	Held            Held
	EpochSeq        int64
	Trigger         types.Trigger
	Resumed         bool
	Abandoned       bool
	BadReason       string // "invalid", "model-failed" or "input"; empty otherwise
	Moves           []Move
	Skipped         []string // direction ids whose verdict could not be applied (already moved or gone)
	Observed        []Move   // finished and stalled directions the host reported, moved when the epoch opened
	AllPlausiblyMet bool
	Judgment        *types.Judgment
}

// judgeEpoch runs the one judge call for an open epoch and records the outcome.
func (e *Engine) judgeEpoch(ctx context.Context, ep types.Epoch, snap Snapshot, resumed bool) (TickResult, error) {
	res := TickResult{EpochSeq: ep.Seq, Trigger: ep.Trigger, Resumed: resumed}
	open, err := e.d.Store.OpenDirections(ctx, e.cfg.Profile, e.cfg.Generation)
	if err != nil {
		return e.fail(err)
	}
	if res.Observed, err = e.observe(ctx, open, snap); err != nil {
		return e.failOrStop(ctx, ep, res, err)
	}
	if open, err = e.d.Store.OpenDirections(ctx, e.cfg.Profile, e.cfg.Generation); err != nil {
		return e.fail(err)
	}
	out, err := e.d.Judge.Run(ctx, buildInput(snap, open))
	if err != nil {
		if ctx.Err() != nil {
			return res, ctx.Err() // leave the epoch open: the next tick resumes it
		}
		return e.badJudgment(ctx, ep, res, err)
	}
	if !e.stillRunning(ctx) {
		return e.abandonNow(ctx, ep, res)
	}
	if _, err := e.d.Store.RecordJudgment(ctx, e.cfg.Profile, e.cfg.Generation, ep.Seq, out.Body); err != nil {
		return e.failOrStop(ctx, ep, res, err)
	}
	j := out.Judgment
	return e.applyWith(ctx, ep, j, res)
}

func buildInput(snap Snapshot, open []types.Direction) judge.Input {
	in := judge.Input{Criteria: snap.Criteria}
	for _, d := range open {
		in.Directions = append(in.Directions, judge.DirectionInput{ID: d.ID, Title: d.Title, State: d.State, Summary: snap.Summaries[d.ID]})
	}
	return in
}

// observe moves directions the host reports as finished (lead done) or stalled (idle a full window). These are facts
// the host reported, not judge effects; each is a forward move and repeating it is harmless.
func (e *Engine) observe(ctx context.Context, open []types.Direction, snap Snapshot) ([]Move, error) {
	now := e.d.Clock.Now()
	var moved []Move
	state := map[string]types.DirectionState{}
	for _, d := range open {
		state[d.ID] = d.State
	}
	step := func(id string, to types.DirectionState) error {
		if !types.CanMoveDirection(state[id], to) {
			return nil
		}
		if err := e.d.Store.MoveDirection(ctx, e.cfg.Profile, e.cfg.Generation, id, to, ""); err != nil {
			if errors.Is(err, sstore.ErrBackwardMove) || errors.Is(err, sstore.ErrNotFound) {
				return nil
			}
			return err
		}
		state[id] = to
		moved = append(moved, Move{DirectionID: id, To: to})
		return nil
	}
	for _, id := range finishedIDs(open, snap.Activity) {
		if err := step(id, types.DirFinished); err != nil {
			return moved, err
		}
	}
	for _, id := range stalledIDs(now, e.cfg.StallWindow, open, snap.Activity) {
		if err := step(id, types.DirStalled); err != nil {
			return moved, err
		}
	}
	return moved, nil
}

// failOrStop handles a store error mid-epoch: a stopped run abandons the epoch, anything else is returned.
func (e *Engine) failOrStop(ctx context.Context, ep types.Epoch, res TickResult, err error) (TickResult, error) {
	if errors.Is(err, sstore.ErrRunNotRunning) {
		return e.abandonNow(ctx, ep, res)
	}
	out, err := e.fail(err)
	out.EpochSeq, out.Trigger = ep.Seq, ep.Trigger
	return out, err
}

// abandonNow closes an unfinished epoch because the run stopped. It writes no judgment and applies nothing.
func (e *Engine) abandonNow(ctx context.Context, ep types.Epoch, res TickResult) (TickResult, error) {
	if err := e.d.Store.MoveEpoch(ctx, e.cfg.Profile, e.cfg.Generation, ep.Seq, types.EpochAbandoned); err != nil {
		return e.fail(err)
	}
	res.Outcome, res.Held, res.Abandoned = OutcomeInert, HeldNotRunning, true
	return res, nil
}

// badJudgment records a bad-judgment epoch. It changes nothing else: no direction moves, no verdicts.
func (e *Engine) badJudgment(ctx context.Context, ep types.Epoch, res TickResult, cause error) (TickResult, error) {
	if !e.stillRunning(ctx) {
		return e.abandonNow(ctx, ep, res)
	}
	if err := e.d.Store.MoveEpoch(ctx, e.cfg.Profile, e.cfg.Generation, ep.Seq, types.EpochBadJudgment); err != nil {
		return e.failOrStop(ctx, ep, res, err)
	}
	res.Outcome = OutcomeBadJudgment
	switch {
	case errors.Is(cause, types.ErrBadJudgment):
		res.BadReason = "invalid"
	case errors.Is(cause, judge.ErrModelFailed):
		res.BadReason = "model-failed"
	default:
		res.BadReason = "input"
	}
	return res, nil
}

func (e *Engine) apply(ctx context.Context, ep types.Epoch, j types.Judgment, resumed bool) (TickResult, error) {
	return e.applyWith(ctx, ep, j, TickResult{EpochSeq: ep.Seq, Trigger: ep.Trigger, Resumed: resumed})
}

// applyWith applies keep, prune and boost verdicts of a recorded judgment, then marks the epoch applied. Every move is
// a forward move the store guards; a verdict that can no longer apply is skipped, so a repeat after a crash is safe.
func (e *Engine) applyWith(ctx context.Context, ep types.Epoch, j types.Judgment, res TickResult) (TickResult, error) {
	state := map[string]types.DirectionState{}
	open, err := e.d.Store.OpenDirections(ctx, e.cfg.Profile, e.cfg.Generation)
	if err != nil {
		return e.fail(err)
	}
	for _, d := range open {
		state[d.ID] = d.State
	}
	for _, a := range j.Directions {
		to, ok := target(a.Verdict, state[a.ID])
		if !ok {
			if _, open := state[a.ID]; !open {
				res.Skipped = append(res.Skipped, a.ID)
			}
			continue
		}
		err := e.d.Store.MoveDirection(ctx, e.cfg.Profile, e.cfg.Generation, a.ID, to, "")
		switch {
		case err == nil:
			res.Moves = append(res.Moves, Move{DirectionID: a.ID, Verdict: a.Verdict, To: to})
		case errors.Is(err, sstore.ErrBackwardMove) || errors.Is(err, sstore.ErrNotFound):
			res.Skipped = append(res.Skipped, a.ID)
		default:
			return e.failOrStop(ctx, ep, res, err)
		}
	}
	if err := e.d.Store.MoveEpoch(ctx, e.cfg.Profile, e.cfg.Generation, ep.Seq, types.EpochApplied); err != nil {
		return e.failOrStop(ctx, ep, res, err)
	}
	res.Outcome, res.Judgment = OutcomeApplied, &j
	ids := make([]string, 0, len(j.Criteria))
	for _, c := range j.Criteria {
		ids = append(ids, c.ID)
	}
	res.AllPlausiblyMet = j.AllPlausiblyMet(ids)
	return res, nil
}

// target maps a verdict to the direction state it moves to. Keep never moves. Prune moves any open direction to
// pruned. Boost activates a proposed direction; boosting a direction that is already active or stalled changes nothing.
func target(v types.Verdict, from types.DirectionState) (types.DirectionState, bool) {
	switch v {
	case types.VerdictPrune:
		if types.CanMoveDirection(from, types.DirPruned) {
			return types.DirPruned, true
		}
	case types.VerdictBoost:
		if types.CanMoveDirection(from, types.DirActive) {
			return types.DirActive, true
		}
	}
	return "", false
}
