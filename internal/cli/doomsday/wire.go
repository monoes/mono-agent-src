package doomsday

import (
	"context"
	"errors"
	"fmt"

	"github.com/monoes/mono-agent/internal/doomsday/stop"
	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// Wiring connects the store and the stop package to the CLI engine. Every seam is an interface so a scratch test
// can run a whole start and stop against a fake containment.
type Wiring struct {
	Store   *store.SQLStore
	Profile string
	RunDir  stop.RunDir
	// Probe is stop.Probe in production: nil means containment is available, anything else is the reason the
	// mode refuses to start. A nil Probe refuses (StoreEngine's errNoProbe).
	Probe func() error
	// Kernel is the platform's cgroup seam (stop.LinuxKernel); nil means no unit can be proven, so Stop reports
	// the unit as not proven rather than claiming it is gone.
	Kernel stop.Kernel
	Tokens interface {
		RevokeTokens(ctx context.Context, gen uint64) error
		CancelQueue(ctx context.Context, gen uint64) error
	}
	Clock stop.Clock
}

// notProvenEnder is the Ender of an install with no Kernel: it signals nothing and says so.
type notProvenEnder struct{}

func (notProvenEnder) End(context.Context, uint64, []uint64) stop.UnitOutcome {
	return stop.UnitOutcome{Kind: stop.UnitNotProven, Note: "no containment kernel is wired, so no unit can be proven; nothing was signalled"}
}

// Engine is the StoreEngine over the wiring.
func (w Wiring) Engine() *StoreEngine {
	return &StoreEngine{
		Store: w.Store, Profile: w.Profile,
		ContainmentProbe: w.Probe,
		StopFn:           w.stop,
		AfterEnter:       func(_ context.Context, gen int64) error { return w.RunDir.CreateSecret(uint64(gen)) },
	}
}

func (w Wiring) ender() stop.Ender {
	if w.Kernel == nil {
		return notProvenEnder{}
	}
	return stop.UnitEnder{K: w.Kernel, Dir: w.RunDir}
}

func (w Wiring) clock() stop.Clock {
	if w.Clock != nil {
		return w.Clock
	}
	return stop.SystemClock{}
}

// stop runs the Stop order for every open run of the profile and folds the reports into one outcome. A fact the
// order could not record is never rounded up: Incomplete carries the report lines of any run that is not fully
// stopped, and the command then does not print "stopped".
func (w Wiring) stop(ctx context.Context) (StopOutcome, error) {
	if w.Tokens == nil {
		return StopOutcome{}, errors.New("token revocation is not wired; nothing was stopped")
	}
	runs, err := w.Store.OpenRuns(ctx, w.Profile)
	if err != nil {
		return StopOutcome{}, fmt.Errorf("the open runs cannot be read, nothing was stopped: %w", err)
	}
	if len(runs) == 0 {
		return StopOutcome{}, errors.New("no open run to stop")
	}
	rs := stop.StoreRuns{Store: w.Store, Profile: w.Profile, By: "operator", Tokens: w.Tokens,
		Runs: func(ctx context.Context) ([]stop.RunInfo, error) { return w.openInfos(ctx) }}
	st := &stop.Stopper{Dir: w.RunDir, Store: rs, Ender: w.ender(), Clock: w.clock()}
	out := StopOutcome{MarkerRecorded: true, IntentRecorded: true}
	for _, r := range runs {
		rep := st.Stop(ctx, uint64(r.Generation), r.CharterID, "operator stop")
		out.MarkerRecorded = out.MarkerRecorded && stepDone(rep, stop.StepMarker)
		out.IntentRecorded = out.IntentRecorded && stepDone(rep, stop.StepIntent)
		for _, s := range append(append([]stop.Survivor{}, rep.Unit.Survivors...), rep.Unit.Unknown...) {
			out.Survivors = append(out.Survivors, fmt.Sprintf("pid %d (%s)", s.PID, s.Comm))
		}
		out.Survivors = append(out.Survivors, rep.Members...)
		if !rep.Stopped() {
			out.Incomplete = append(out.Incomplete, rep.Lines()...)
		}
	}
	return out, nil
}

func stepDone(rep stop.Report, name string) bool {
	for _, s := range rep.Steps {
		if s.Name == name {
			return s.Done
		}
	}
	return false
}

func (w Wiring) openInfos(ctx context.Context) ([]stop.RunInfo, error) {
	runs, err := w.Store.OpenRuns(ctx, w.Profile)
	var out []stop.RunInfo
	for _, r := range runs {
		out = append(out, stop.RunInfo{Generation: uint64(r.Generation), CharterID: r.CharterID, State: stop.RunState(r.State)})
	}
	return out, err
}
