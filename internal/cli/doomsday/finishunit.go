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
	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// The verification unit (note 13.6 section 5, note 13.2): the run's own unit is ended by the Stop that began the finish,
// so every round of the finish gets a unit of its own. It is created and recorded durably before any criterion starts
// (stop.UnitCreator: a transient service with a delegated cgroup, bound by cgroup identity and an anchor process, never
// by a name pattern), every criterion is BORN in a ticket sub-cgroup of it (stop.Placer, clone3 into the cgroup: no hold
// stub), and at the end of the round the unit is ended through the same identity proof Stop uses.
//
// The unit is a witnessed unit of the run's generation like the run's own: unit-intent before the service is asked for,
// unit-bound before the record file (stop.UnitCreator with the sequencer as its journal), unit-ended after the identity
// proof of its end (stop.NewWitnessEnder). It is also a registry member of kind `verify` (note 13.6 G3), registered after the
// unit is witnessed and before any criterion starts (a host that dies in between leaves a witnessed unit with no member
// row: it is found by its unit-bound line without an end line and ended by its record, see
// TestAVerificationUnitWithoutItsMemberRowIsFoundAndEndedByItsRecord) and released only on that proof: a host that dies mid-round leaves a live member that blocks a new
// run of the profile, and `doomsday stop` ends the unit by its record. With no witness (no sequencer) no unit is made.

// RegisterVerify registers a verification unit as a member, after the witness's freshness check (store.RegisterVerify).
func (s *SeqStore) RegisterVerify(ctx context.Context, m store.Member) error {
	if err := s.check(ctx, m.Generation); err != nil {
		return &store.Refusal{Reason: err.Error()}
	}
	return s.SQLStore.RegisterVerify(ctx, m)
}

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
		seq, err := w.Sequencer(ctx)
		if err != nil {
			return finish.Sandbox{}, nil, fmt.Errorf("no witness, so no verification unit can be recorded: %w", err)
		}
		dir := e.unitDir(gen, round)
		rec, err := stop.CreateUnit(ctx, w.Kernel, stop.CreateArgs{Dir: dir, Host: w.Host, Journal: seq}, uint64(gen))
		if err != nil {
			return finish.Sandbox{}, nil, err
		}
		end := func() {
			e.notes = append(e.notes, e.endUnit(dir, gen)...)
			finish.RemoveTree(filepath.Join(dir.Dir, "scratch"))
		}
		m := store.Member{Profile: w.Profile, Generation: gen, ID: rec.Name, Kind: store.KindVerify, Pid: rec.AnchorPID, Stamp: rec.AnchorStamp}
		if err := w.Chained().RegisterVerify(ctx, m); err != nil {
			end()
			return finish.Sandbox{}, nil, fmt.Errorf("the verification unit could not be registered as a member: %w", err)
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
		return finish.Sandbox{Place: placer.Place, Scratch: scratch, Limits: e.Limits, RequireNetDeny: e.RequireNetDeny}, end, nil
	}
}

// endUnit ends the unit recorded in dir through the witnessing ender (unit-ended is written when its end is proven) and
// releases its member on that proof. It returns what could not be proven (empty when the unit is proven gone and
// released); an unproven unit stays a live member.
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
	var ender stop.Ender = u
	if seq, err := w.Sequencer(ctx); err == nil {
		u.Witness = seq.BoundUnits
		ender = stop.NewWitnessEnder(u, seq)
	}
	rec, hadRec, _ := dir.ReadUnitFile(uint64(gen))
	out := ender.End(ctx, uint64(gen), nil)
	switch out.Kind {
	case stop.UnitEnded, stop.UnitNone, stop.UnitBestEffort:
		if hadRec {
			if err := w.Store.Release(ctx, w.Profile, gen, rec.Name); err != nil {
				return []string{fmt.Sprintf("the verification unit of %s is gone but its member could not be released: %v", filepath.Base(dir.Dir), err)}
			}
		}
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
