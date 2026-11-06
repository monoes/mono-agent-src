package doomsday

import (
	"context"
	"database/sql"
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
	// Host is the service manager that starts and stops the run unit (stop.SystemHost in production). Without
	// it, or without a Kernel, no run can start and Stop cannot end a unit's service.
	Host   stop.UnitHost
	Tokens interface {
		RevokeTokens(ctx context.Context, gen uint64) error
		CancelQueue(ctx context.Context, gen uint64) error
	}
	Clock stop.Clock
	// Binding says whether security.peercred_binding is on; nil refuses every start ("no run without binding").
	Binding func(ctx context.Context) (bool, error)
	// DB is the profile's database: the merge journal and the task store's merge records are read from it. Nil
	// leaves merge recovery out (only a test that does not exercise merges leaves it nil).
	DB *sql.DB
	// Witness is the note 13.5 witness setup (database path for the pinned connection, MAC keys, secret store).
	// Without it no run can be entered and nothing chained is written.
	Witness *WitnessSetup
	// Progress receives the Stop order's early line, once per open run (the marker is written, the database step follows). Nil prints nothing: only
	// the CLI sets it, a host that stops a run does not talk to a terminal.
	Progress func(string)
	// ChatNote writes one system message into a conversation's transcript (ai.AIStore.RecordSystemNotice in the CLI).
	// Nil: a stop ends the sessions but leaves no message, and says so.
	ChatNote func(ctx context.Context, conversationID, message string) error
}

// notProvenEnder is the Ender of an install with no Kernel: it signals nothing and says so.
type notProvenEnder struct{}

func (notProvenEnder) End(context.Context, uint64, []uint64) stop.UnitOutcome {
	return stop.UnitOutcome{Kind: stop.UnitNotProven, Note: "no containment kernel is wired, so no unit can be proven; nothing was signalled"}
}

// Engine is the StoreEngine over the wiring. The witness (and with it the pinned connection) is built when something needs it
// (a charter is about to be entered, or a run's chained write): constructing the engine and every verb that reads or refuses
// with no run pins nothing, reads no key and watches no file.
func (w Wiring) Engine() *StoreEngine {
	chained := w.Chained()
	witness := func(ctx context.Context) (*stop.Sequencer, error) { return chained.seq(ctx) }
	e := &StoreEngine{
		Store: chained, Profile: w.Profile,
		// No witness, no mode: asked after the containment probe, so a refusing probe never builds the witness.
		ContainmentProbe: func() error {
			if w.Probe == nil {
				return errNoProbe
			}
			if err := w.Probe(); err != nil {
				return err
			}
			_, err := witness(context.Background())
			return err
		},
		Binding:     w.Binding,
		StopFn:      w.stop,
		BeforeStart: w.recoverMerges,
		CreateUnit:  w.createUnit,
		Boss:        w.designateBoss,
		AfterStopFn: w.afterStop,
		AfterEnter:  func(_ context.Context, gen int64) error { return w.RunDir.CreateSecret(uint64(gen)) },
		HeadAnchor:  w.headAnchor(),
		BeginEntry: func(ctx context.Context, gen int64) error {
			seq, err := witness(ctx)
			if err != nil {
				return err
			}
			return seq.BeginEntry(ctx, gen)
		},
		Anchored: func(ctx context.Context, gen int64) (bool, string, error) {
			seq, err := witness(ctx)
			if err != nil {
				return false, "", err
			}
			return seq.Entered(ctx, gen)
		},
	}
	return e
}

func (w Wiring) ender() stop.Ender {
	if w.Kernel == nil {
		return notProvenEnder{}
	}
	u := stop.UnitEnder{K: w.Kernel, Dir: w.RunDir}
	if w.Host != nil {
		u.StopUnit = w.Host.StopUnit
	}
	seq, err := w.Sequencer(context.Background())
	if err != nil {
		return u // stopping stays possible; the end line cannot be written, so nothing claims it
	}
	u.Witness = seq.BoundUnits
	return stop.NewWitnessEnder(u, seq)
}

// createUnit is StoreEngine.CreateUnit: the unit is created, proven and recorded before the run exists.
func (w Wiring) createUnit(ctx context.Context, gen int64) (func(context.Context) error, error) {
	c := stop.UnitCreator{K: w.Kernel, Dir: w.RunDir, Host: w.Host}
	seq, err := w.Sequencer(ctx)
	if err != nil {
		return nil, err
	}
	c.Journal = seq // unit-intent before the service, unit-bound before the unit file
	rec, err := c.Create(ctx, uint64(gen))
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context) error { return c.Discard(ctx, rec) }, nil
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
	_ = w.recoverMerges(ctx) // a merge member a dead host left is released only on proof; stopping goes on either way
	runs, err := w.Store.OpenRuns(ctx, w.Profile)
	if err != nil {
		return StopOutcome{}, fmt.Errorf("the open runs cannot be read, nothing was stopped: %w", err)
	}
	if len(runs) == 0 {
		return StopOutcome{}, errors.New("no open run to stop")
	}
	st := w.Stopper("operator", w.ender())
	out := StopOutcome{MarkerRecorded: true, IntentRecorded: true}
	for _, r := range runs {
		rep := st.Stop(ctx, uint64(r.Generation), r.CharterID, "operator stop")
		if rep.Stopped() {
			out.Runs = append(out.Runs, RunRef{CharterID: r.CharterID, Generation: r.Generation})
		}
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

// Stopper is the production Stop order for this profile. It runs in the CLI, a different process from the host that holds
// the gate, so it carries the host link (the marker and emergency message in, the host's acknowledgement out) instead of a
// Gate: a Stopper with neither cannot reach quiescent.
func (w Wiring) Stopper(by string, e stop.Ender) *stop.Stopper {
	rs := stop.StoreRuns{Store: w.Chained(), Profile: w.Profile, By: by, Tokens: w.Tokens,
		Runs: func(ctx context.Context) ([]stop.RunInfo, error) { return w.openInfos(ctx) }}
	return &stop.Stopper{Dir: w.RunDir, Store: rs, Ender: e, Host: stop.ProcessHostLink{Dir: w.RunDir, Clock: w.clock()}, Clock: w.clock(), Progress: w.Progress}
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
