package doomsday

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
	"github.com/monoes/mono-agent/internal/doomsday/stop"
)

// The verification unit (note 13.6 section 5, note 13.2): the run's own unit is ended by the Stop that began the finish,
// so every round of the finish gets a unit of its own. It is created and recorded durably before any criterion starts
// (stop.UnitCreator: a transient service with a delegated cgroup, bound by cgroup identity and an anchor process, never
// by a name pattern), every criterion is BORN in a ticket sub-cgroup of it (stop.Placer, clone3 into the cgroup: no hold
// stub), and at the end of the round the unit is ended through the same identity proof Stop uses.
//
// Not done here, and said so: the unit's intent and binding are not written to the witness (the witness is a chain of
// the run's own generation, which ended at quiescent), and the unit is not a registry member kind `verify` (note 13.6
// G3): a dead host leaves a unit that `doomsday stop` ends by its record, and nothing else tracks it.

func (e *FinishEngine) unitDir(gen, round int64) stop.RunDir {
	return stop.RunDir{Dir: filepath.Join(e.area(), "units", fmt.Sprintf("g%d-r%d", gen, round))}
}

func (e *FinishEngine) sandbox(gen int64) func(ctx context.Context, round int64) (finish.Sandbox, func(), error) {
	return func(ctx context.Context, round int64) (finish.Sandbox, func(), error) {
		w := e.W
		if w.Kernel == nil || w.Host == nil {
			return finish.Sandbox{}, nil, errors.New("no containment kernel or service manager is wired, so no verification unit can be made")
		}
		if w.Probe == nil {
			return finish.Sandbox{}, nil, errors.New("no containment probe is wired, so containment cannot be shown to be available")
		}
		if err := w.Probe(); err != nil {
			return finish.Sandbox{}, nil, fmt.Errorf("containment is not available: %w", err)
		}
		dir := e.unitDir(gen, round)
		if _, err := (stop.UnitCreator{K: w.Kernel, Dir: dir, Host: w.Host}).Create(ctx, uint64(gen)); err != nil {
			return finish.Sandbox{}, nil, err
		}
		end := func() {
			for _, n := range e.endUnit(dir, gen) {
				e.notes = append(e.notes, n)
			}
			finish.RemoveTree(filepath.Join(dir.Dir, "scratch"))
		}
		placer, err := stop.NewPlacer(w.Kernel, dir, uint64(gen))
		if err != nil {
			end()
			return finish.Sandbox{}, nil, err
		}
		scratch := filepath.Join(dir.Dir, "scratch")
		if err := os.Mkdir(scratch, 0o700); err != nil {
			end()
			return finish.Sandbox{}, nil, err
		}
		return finish.Sandbox{Place: placer.Place, Scratch: scratch}, end, nil
	}
}

// endUnit ends the unit recorded in dir and returns what could not be proven (empty when it is proven gone).
func (e *FinishEngine) endUnit(dir stop.RunDir, gen int64) []string {
	w := e.W
	if w.Kernel == nil {
		return []string{"no containment kernel is wired, so the verification unit could not be proven ended"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	u := stop.UnitEnder{K: w.Kernel, Dir: dir, Cfg: stop.EndConfig{Clock: stop.SystemClock{}}}
	if w.Host != nil {
		u.StopUnit = w.Host.StopUnit
	}
	out := u.End(ctx, uint64(gen), nil)
	switch out.Kind {
	case stop.UnitEnded, stop.UnitNone:
		return nil
	}
	var why []string
	for _, s := range append(append([]stop.Survivor{}, out.Survivors...), out.Unknown...) {
		why = append(why, fmt.Sprintf("pid %d (%s)", s.PID, s.Comm))
	}
	return []string{fmt.Sprintf("the verification unit of %s is not proven gone (%s%s)", filepath.Base(dir.Dir), out.Note, strings.Join(why, ", "))}
}

// endAllUnits ends every verification unit this generation made (a stop of the finish, or a crashed round).
func (e *FinishEngine) endAllUnits(gen int64) []string {
	ents, _ := os.ReadDir(filepath.Join(e.area(), "units"))
	var left []string
	for _, d := range ents {
		if d.IsDir() && strings.HasPrefix(d.Name(), fmt.Sprintf("g%d-r", gen)) {
			left = append(left, e.endUnit(stop.RunDir{Dir: filepath.Join(e.area(), "units", d.Name())}, gen)...)
		}
	}
	return left
}
