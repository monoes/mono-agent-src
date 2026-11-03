package dynorg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"

	"github.com/monoes/mono-agent/internal/doomsday/stop"
	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// The merge as a begun operation of the open-ended mode (note 13.3): four steps, each with its own gate ticket,
// so that a merge which has begun finishes after Stop and one which has not begun does not start (Q11). With
// Config.Doomsday nil nothing here runs and Merge behaves exactly as before.

// The registered steps of a merge, in order (note 13.3 section 3).
const (
	MergeStepCheckpoint = "checkpoint"
	MergeStepPin        = "pin"
	MergeStepMerge      = "merge"
	MergeStepRecord     = "record"
)

var mergeSteps = []string{MergeStepCheckpoint, MergeStepPin, MergeStepMerge, MergeStepRecord}

// ErrMergeStopped is the refusal of a merge that has not begun while Stop is in progress or the run does not
// admit work.
var ErrMergeStopped = errors.New("Stop is in progress; the merge was not started")

// MergeMembers is the part of the store the merge uses: a durable live member for the whole operation, and the
// audit stream.
type MergeMembers interface {
	Register(ctx context.Context, m store.Member) error
	Release(ctx context.Context, profile string, generation int64, id string) error
	AppendAudit(ctx context.Context, profile string, generation int64, kind, payload string) (store.AuditRow, error)
}

// DoomsdayMerge connects a Conductor's merges to a run's gate and registry.
type DoomsdayMerge struct {
	Gate       *stop.Gate
	Members    MergeMembers
	Profile    string
	Generation int64
	seq        atomic.Int64
}

// mergeOp is one begun merge. Every method is a no-op on a nil receiver (the mode is off).
type mergeOp struct {
	dd    *DoomsdayMerge
	id    string
	cur   *stop.Ticket
	first bool
	live  bool // the member row exists and has not been released
}

// beginMerge admits, fences, registers and starts the first step. A refusal happens before any git command, any
// write-lease claim and any commit on the worker's branch.
func (c *Conductor) beginMerge(ctx context.Context) (*mergeOp, error) {
	dd := c.cfg.Doomsday
	if dd == nil {
		return nil, nil
	}
	id := fmt.Sprintf("merge-%d-%d", os.Getpid(), dd.seq.Add(1))
	t, err := dd.Gate.Admit(stop.Spec{Kind: stop.KindE1, Op: id, Steps: mergeSteps})
	if err != nil {
		return nil, ErrMergeStopped
	}
	if err := dd.Gate.BeginStart(t); err != nil {
		return nil, ErrMergeStopped
	}
	m := store.Member{Profile: dd.Profile, Generation: dd.Generation, ID: id, Kind: "merge", Pid: os.Getpid(), Stamp: "host"}
	if err := dd.Members.Register(ctx, m); err != nil {
		dd.Gate.Abort(t)
		return nil, fmt.Errorf("%w: %v", ErrMergeStopped, err)
	}
	if err := dd.Gate.Started(t, id); err != nil {
		_ = dd.Members.Release(ctx, dd.Profile, dd.Generation, id)
		return nil, ErrMergeStopped
	}
	return &mergeOp{dd: dd, id: id, cur: t, first: true, live: true}, nil
}

// run executes one registered step under its ticket. The first step uses the ticket begun by beginMerge; each
// later step is admitted as a continuation, which works while the gate is closing, and is refused after T_merge.
func (m *mergeOp) run(step string, fn func() error) error {
	if m == nil {
		return fn()
	}
	if m.first {
		m.first = false
	} else {
		t, err := m.dd.Gate.AdmitContinuation(m.id, step)
		if err != nil {
			return fmt.Errorf("the merge did not finish (step %s refused: %v)", step, err)
		}
		if err := m.dd.Gate.BeginStart(t); err != nil {
			return fmt.Errorf("the merge did not finish (step %s could not start: %v)", step, err)
		}
		if err := m.dd.Gate.Started(t, m.id+"/"+step); err != nil {
			return fmt.Errorf("the merge did not finish (step %s could not be recorded: %v)", step, err)
		}
		m.cur = t
	}
	err := fn()
	m.dd.Gate.EndWork(m.cur, err == nil)
	return err
}

// pin records the head and the tip the merge will use, durably, before the merge step may start.
func (m *mergeOp) pin(ctx context.Context, before, tip, branch string) error {
	if m == nil {
		return nil
	}
	payload := fmt.Sprintf("%s branch=%s before=%s tip=%s", m.id, branch, before, tip)
	if _, err := m.dd.Members.AppendAudit(ctx, m.dd.Profile, m.dd.Generation, "merge.pin", payload); err != nil {
		return fmt.Errorf("the merge pin could not be recorded, so nothing was merged: %v", err)
	}
	return nil
}

// end records the outcome and releases the member. All git commands of the operation have ended when Merge
// returns (each is bounded and waited for), so the process question of note 13.3 section 5 is answered here;
// after a host death only RevisitMergeMembers can answer it.
func (m *mergeOp) end(ctx context.Context, err *error) {
	if m == nil || !m.live {
		return
	}
	ctx = context.WithoutCancel(ctx)
	state := "finished"
	if *err != nil {
		state = "incomplete: " + strings.ReplaceAll(strings.TrimSpace(boundText(fmt.Sprint(*err), 200)), "\n", " ")
	}
	if _, aerr := m.dd.Members.AppendAudit(ctx, m.dd.Profile, m.dd.Generation, "merge.end", m.id+" "+state); aerr != nil {
		return // the member stays live: an operation with no recorded end is not released
	}
	if rerr := m.dd.Members.Release(ctx, m.dd.Profile, m.dd.Generation, m.id); rerr == nil {
		m.live = false
	}
}
