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

// ErrMergeRepoBusy is the refusal of a merge into a repository that another merge holds, or that a failed merge
// left for the operator (note 13.3 section 6.3, T21).
var ErrMergeRepoBusy = errors.New("another merge holds this repository, or one left it for the operator")

// MergeMembers is the part of the store the merge uses: a durable live member for the whole operation, and the
// audit stream.
type MergeMembers interface {
	Register(ctx context.Context, m store.Member) error
	Release(ctx context.Context, profile string, generation int64, id string) error
	AppendAudit(ctx context.Context, profile string, generation int64, kind, payload string) (store.AuditRow, error)
}

// DoomsdayMerge connects a Conductor's merges to a run's gate, registry and journal. Journal is required: a
// merge with no journal cannot be recovered, so it is not begun.
type DoomsdayMerge struct {
	Gate       *stop.Gate
	Members    MergeMembers
	Journal    MergeJournal
	Profile    string
	Generation int64
	// Place starts every git command of the merge inside the run's containment unit. Required: a merge whose
	// commands could run outside the unit would be invisible to "the unit is empty", so it is not begun.
	Place GitPlacer
}

// mergeSeq makes operation ids unique within a process, whatever number of DoomsdayMerge values exist.
var mergeSeq atomic.Int64

// mergeOp is one begun merge. Every method is a no-op on a nil receiver (the mode is off).
type mergeOp struct {
	dd    *DoomsdayMerge
	id    string
	cur   *stop.Ticket
	first bool
	live  bool // the member row exists and has not been released

	reached     int    // the highest step whose work started
	merged      bool   // the merge step's work succeeded: the tip is in HEAD
	rolledBack  bool   // a failed merge was aborted
	abortFailed bool   // a failed merge could not be aborted
	reason      string // set when a step was refused rather than failed
}

func stepIndex(step string) int {
	for i, s := range mergeSteps {
		if s == step {
			return i + 1
		}
	}
	return 0
}

// resolveDoomsday is the run a merge belongs to: the fixed Config.Doomsday, else the resolver (nil when the mode
// is not built in). A resolver error refuses the merge: a run that cannot be read is not "no run".
func (c *Conductor) resolveDoomsday(ctx context.Context) (*DoomsdayMerge, error) {
	if c.cfg.Doomsday != nil {
		return c.cfg.Doomsday, nil
	}
	if c.cfg.DoomsdayFor == nil {
		return nil, nil
	}
	dd, err := c.cfg.DoomsdayFor(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMergeStopped, err)
	}
	return dd, nil
}

// beginMerge admits, fences, registers, claims the repository and starts the first step. A refusal happens before
// any git command, any write-lease claim and any commit on the worker's branch.
func (c *Conductor) beginMerge(ctx context.Context, repo, branch, worker string) (*mergeOp, error) {
	dd, err := c.resolveDoomsday(ctx)
	if err != nil || dd == nil {
		return nil, err
	}
	if dd.Journal == nil {
		return nil, fmt.Errorf("%w: the merge journal is not wired", ErrMergeStopped)
	}
	if dd.Place == nil {
		return nil, fmt.Errorf("%w: git commands cannot be placed in the containment unit", ErrMergeStopped)
	}
	id := fmt.Sprintf("merge-%d-%d", os.Getpid(), mergeSeq.Add(1))
	t, err := dd.Gate.Admit(stop.Spec{Kind: stop.KindE1, Op: id, Steps: mergeSteps})
	if err != nil {
		return nil, ErrMergeStopped
	}
	if err := dd.Gate.BeginStart(t); err != nil {
		return nil, ErrMergeStopped
	}
	abort := func(err error) (*mergeOp, error) { dd.Gate.Abort(t); return nil, err }
	// a cancelled turn (Stop, the moment Q11 is about) must not turn a release into a silent no-op
	release := func() { _ = dd.Members.Release(context.WithoutCancel(ctx), dd.Profile, dd.Generation, id) }
	if err := dd.repoFree(ctx, repo, ""); err != nil {
		return abort(err)
	}
	m := store.Member{Profile: dd.Profile, Generation: dd.Generation, ID: id, Kind: "merge", Pid: os.Getpid(), Stamp: "host"}
	if err := dd.Members.Register(ctx, m); err != nil {
		return abort(fmt.Errorf("%w: %v", ErrMergeStopped, err))
	}
	// The claim row is the cross-process lock: the chain gives every claim one place in line, and the earliest
	// open claim on a repository wins; a later one withdraws before it touches anything.
	if err := dd.appendRetry(ctx, kindBegin, mergeRec{Op: id, Repo: repo, Branch: branch, Worker: worker, Step: 1}); err != nil {
		release()
		return abort(fmt.Errorf("%w: the merge could not be recorded: %v", ErrMergeStopped, err))
	}
	if err := dd.repoFree(ctx, repo, id); err != nil {
		_ = dd.appendRetry(context.WithoutCancel(ctx), kindEnd, mergeRec{Op: id, State: StateIncomplete, Reason: ReasonClaimLost, Repair: RepairClean})
		release()
		return abort(err)
	}
	if err := dd.Gate.Started(t, id); err != nil {
		_ = dd.appendRetry(context.WithoutCancel(ctx), kindEnd, mergeRec{Op: id, State: StateIncomplete, Reason: ReasonStepFailed, Repair: RepairClean})
		release()
		return nil, ErrMergeStopped
	}
	return &mergeOp{dd: dd, id: id, cur: t, first: true, live: true, reached: 1}, nil
}

// repoFree refuses when another operation holds the repository. With self empty it asks "is anyone holding it";
// with self set (after the claim row) it asks "is anyone ahead of me": an open operation with an earlier claim.
// A journal that cannot be read refuses (fail closed).
func (dd *DoomsdayMerge) repoFree(ctx context.Context, repo, self string) error {
	rows, err := dd.Journal.MergeRows(ctx, dd.Profile)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrMergeStopped, err)
	}
	ops, order := foldJournal(rows)
	me := ops[self]
	for _, id := range order {
		o := ops[id]
		if id == self || o.rec.Repo != repo || !o.blocks() {
			continue
		}
		if me != nil && (o.NeedsOperator() || o.Gen > me.Gen || (o.Gen == me.Gen && o.BeginSeq > me.BeginSeq)) {
			continue // after the claim only an earlier open claim matters; a needs-operator one was refused before it
		}
		if o.NeedsOperator() {
			return fmt.Errorf("%w: %s was left by %s (run `git status` in it, resolve it, then clear it)", ErrMergeRepoBusy, o.rec.Repo, id)
		}
		return fmt.Errorf("%w: %s holds %s", ErrMergeRepoBusy, id, o.rec.Repo)
	}
	return nil
}

// appendRetry writes one journal row; two writers can race for the same seq and the loser retries.
func (dd *DoomsdayMerge) appendRetry(ctx context.Context, kind string, r mergeRec) error {
	var err error
	for i := 0; i < 8; i++ {
		if _, err = dd.Members.AppendAudit(ctx, dd.Profile, dd.Generation, kind, r.payload()); err == nil {
			return nil
		}
	}
	return err
}

// run executes one registered step under its ticket. The first step uses the ticket begun by beginMerge; each
// later step is admitted as a continuation, which works while the gate is closing, and is refused after T_merge.
// A later step's row is durable before its work starts (R1).
func (m *mergeOp) run(step string, fn func() error) error {
	if m == nil {
		return fn()
	}
	idx := stepIndex(step)
	if m.first {
		m.first = false
	} else {
		t, err := m.dd.Gate.AdmitContinuation(m.id, step)
		if err != nil {
			m.reason = ReasonBoundExpired
			return fmt.Errorf("the merge did not finish (step %s refused: %v)", step, err)
		}
		if err := m.dd.Gate.BeginStart(t); err != nil {
			m.reason = ReasonBoundExpired
			return fmt.Errorf("the merge did not finish (step %s could not start: %v)", step, err)
		}
		if err := m.dd.Gate.Started(t, m.id+"/"+step); err != nil {
			return fmt.Errorf("the merge did not finish (step %s could not be recorded: %v)", step, err)
		}
		m.cur = t
		if err := m.dd.appendRetry(context.Background(), kindStep, mergeRec{Op: m.id, Step: idx}); err != nil {
			m.dd.Gate.EndWork(m.cur, false)
			return fmt.Errorf("the merge did not finish (step %s could not be journaled: %v)", step, err)
		}
	}
	m.reached = idx
	err := fn()
	if idx == 3 && err == nil {
		m.merged = true
	}
	m.dd.Gate.EndWork(m.cur, err == nil)
	return err
}

// aborted records the outcome of the merge step's own undo (nil-safe).
func (m *mergeOp) aborted(ok bool) {
	if m == nil {
		return
	}
	m.rolledBack, m.abortFailed = ok, !ok
}

// pin records the head, the tip and the fingerprint of the chat folder the merge will use, durably, before the
// merge step may start.
func (m *mergeOp) pin(ctx context.Context, before, tip, branch, fp string) error {
	if m == nil {
		return nil
	}
	rec := mergeRec{Op: m.id, Branch: branch, Before: before, Tip: tip, Ref: "refs/heads/" + branch, FP: fp}
	if err := m.dd.appendRetry(ctx, kindPin, rec); err != nil {
		return fmt.Errorf("the merge pin could not be recorded, so nothing was merged: %v", err)
	}
	return nil
}

// outcome is the journal state of the operation as the live code sees it (note 13.3 section 6.2 applied live).
func (m *mergeOp) outcome(err error) mergeRec {
	r := mergeRec{Op: m.id, State: StateFinished}
	if err == nil {
		return r
	}
	r.State, r.Reason, r.Repair = StateIncomplete, ReasonStepFailed, RepairClean
	if m.reason != "" {
		r.Reason = m.reason
	}
	switch {
	case m.abortFailed:
		r.Repair = RepairNeedsOperator
	case m.merged:
		r.Repair = RepairAppliedUnrecorded
	case m.rolledBack:
		r.Repair = RepairRolledBack
	}
	r.Note = strings.ReplaceAll(strings.TrimSpace(boundText(err.Error(), 200)), "\n", " ")
	return r
}

// end records the outcome and releases the member. All git commands of the operation have ended when Merge
// returns (each is bounded and waited for), so the process question of note 13.3 section 5 is answered here;
// after a host death only recovery can answer it.
func (m *mergeOp) end(ctx context.Context, err *error) {
	if m == nil || !m.live {
		return
	}
	ctx = context.WithoutCancel(ctx)
	if aerr := m.dd.appendRetry(ctx, kindEnd, m.outcome(*err)); aerr != nil {
		return // the member stays live: an operation with no recorded end is not released
	}
	if rerr := m.dd.Members.Release(ctx, m.dd.Profile, m.dd.Generation, m.id); rerr == nil {
		m.live = false
	}
}
