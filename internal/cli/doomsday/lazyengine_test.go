package doomsday

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/stop"
)

// The words of a database that is locked or unreadable right now (not "not available on this install", not "no run").
const (
	unreadableText = "the open-ended mode's database is busy or cannot be read right now; nothing was changed. Retry in a moment; if it persists, run `monoagentcli doctor`."
	stopUnreadable = "stop could not run, so nothing was stopped and an open run stays open. "
)

// countingPins makes every pin attempt fail and counts it, so a test needs no inotify watch.
func countingPins(t *testing.T) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	real := PinFunc
	PinFunc = func(context.Context, stop.PinOptions) (*stop.Pinned, error) {
		n.Add(1)
		return nil, errors.New("counted")
	}
	t.Cleanup(func() { PinFunc = real; ClosePins() })
	return &n
}

// With no charter and no run, building the engine and every verb that reads or refuses (stop, status, finish, finish --begin,
// rubric) makes no pinned connection, reads no key and writes nothing: the witness is built when a run or a charter needs it.
func TestEngineWithNoRunPinsNothingForStopStatusFinishAndRubric(t *testing.T) {
	r, db := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	keys := &macCounter{in: r.w.Witness.Keys}
	w := r.w
	w.RunDir = stop.RunDir{Dir: t.TempDir() + "/fresh-run"} // a pin cache key no earlier test used
	ws := *w.Witness
	ws.Keys = keys
	w.Witness = &ws
	pins := countingPins(t)
	keys.n.Store(0)
	ctx := context.Background()

	fe := w.FinishEngine()
	if st, err := fe.Status(ctx); err != nil || st.Active || st.RunID != "" {
		t.Fatalf("status with no run: %+v %v", st, err)
	}
	if _, err := fe.Stop(ctx); err == nil || !strings.Contains(err.Error(), "no open run to stop") {
		t.Fatalf("stop with no run: %v", err)
	}
	if done, err := fe.AbortFinish(ctx); done || err != nil {
		t.Fatalf("abort finish with no run: %v %v", done, err)
	}
	if rep, err := fe.FinishBegin(ctx); err != nil || rep.Accepted {
		t.Fatalf("finish --begin with no run: %+v %v", rep, err)
	}
	if _, err := fe.FinishAdvance(ctx, FinishOptions{}); !errors.Is(err, ErrNotFinishing) {
		t.Fatalf("finish with no run: %v", err)
	}
	if _, err := fe.RubricList(ctx); !errors.Is(err, ErrNotFinishing) {
		t.Fatalf("rubric with no run: %v", err)
	}
	if pins.Load() != 0 || keys.n.Load() != 0 {
		t.Fatalf("with no charter and no run: %d pin(s), %d key read(s), want none", pins.Load(), keys.n.Load())
	}
	_ = db
}

// start and render ask the containment probe first: where it refuses, the witness is never built (no pin), and the refusal is the
// probe's. Where it passes, the witness is built then, because a charter is about to be entered.
func TestEngineContainmentProbesBeforeBuildingTheWitness(t *testing.T) {
	probeErr := errors.New("probe says no")
	r, _ := newWire(t, func() error { return probeErr }, fakeKernel{}, nil)
	w := r.w
	w.RunDir = stop.RunDir{Dir: t.TempDir() + "/fresh-run-2"}
	pins := countingPins(t)
	if err := w.Engine().Containment(); !errors.Is(err, probeErr) || pins.Load() != 0 {
		t.Fatalf("a refusing probe: %v, %d pin(s)", err, pins.Load())
	}
	w.Probe = func() error { return nil }
	err := w.Engine().Containment()
	if err == nil || !strings.Contains(err.Error(), "pinned database connection cannot be made") || pins.Load() != 1 {
		t.Fatalf("a passing probe over a witness that cannot be built must refuse with the witness's reason: %v, %d pin(s)", err, pins.Load())
	}
}

// The database that cannot be opened answers in its own words for every verb, and says what it does not know.
func TestUnreadableEngineSaysTheDatabaseIsBusyForEveryVerb(t *testing.T) {
	cause := errors.New("database is locked")
	for _, v := range []struct {
		name string
		args []string
		want string
	}{
		{"stop", []string{"stop"}, stopUnreadable + unreadableText},
		{"status", []string{"status"}, unreadableText},
		{"finish", []string{"finish"}, unreadableText},
		{"finish --begin", []string{"finish", "--begin"}, unreadableText},
		{"rubric", []string{"rubric"}, unreadableText},
		{"start", []string{"start", "--charter", "x"}, unreadableText},
	} {
		t.Run(v.name, func(t *testing.T) {
			rg := newRig()
			rg.deps.Engine = func() Engine { return Unreadable{Err: cause} }
			out, err := run(t, rg.deps, "", v.args...)
			if err == nil || !strings.Contains(err.Error(), v.want) || !strings.Contains(err.Error(), cause.Error()) {
				t.Fatalf("%s: err %v, out %q: want %q and the cause", v.name, err, out, v.want)
			}
			if strings.Contains(err.Error()+out, "not available on this install") || strings.Contains(out, "no run") || strings.Contains(err.Error(), "no open run") {
				t.Fatalf("%s: a wrong statement: %v %q", v.name, err, out)
			}
			if !errors.Is(err, ErrUnreadable) {
				t.Fatalf("%s: the error is not ErrUnreadable: %v", v.name, err)
			}
		})
	}
}

// The production Stopper carries the wiring's progress sink to the Stop order.
func TestWiringStopperCarriesTheProgressSink(t *testing.T) {
	r, _ := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	var got []string
	w := r.w
	w.Progress = func(m string) { got = append(got, m) }
	st := w.Stopper("operator", notProvenEnder{})
	if st.Progress == nil {
		t.Fatal("the Stopper has no progress sink")
	}
	st.Progress(stop.ProgressIntent)
	if len(got) != 1 || got[0] != stop.ProgressIntent {
		t.Fatalf("progress: %v", got)
	}
}
