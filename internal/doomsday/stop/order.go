package stop

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"
)

// RunState is the forward-only state of a run generation (spec 5.1).
type RunState string

const (
	StateRunning          RunState = "running"
	StateStopRequested    RunState = "stop-requested"
	StateAuthorityRevoked RunState = "authority-revoked"
	StateCleanupPending   RunState = "cleanup-pending"
	StateQuiescent        RunState = "quiescent"
)

// RunInfo is one open (not quiescent) run generation.
type RunInfo struct {
	Generation uint64
	CharterID  string
	State      RunState
}

// RunStore is the part of the store package Stop needs. Advance is forward-only and idempotent: moving to the
// current or an earlier state is a no-op that returns nil.
type RunStore interface {
	State(ctx context.Context, gen uint64) (RunState, error)
	Advance(ctx context.Context, gen uint64, to RunState, cause string) error
	// RevokeTokens revokes every token of the run; CancelQueue cancels queued commands. The store may do both
	// in the one transaction that also moves the state to authority-revoked.
	RevokeTokens(ctx context.Context, gen uint64) error
	CancelQueue(ctx context.Context, gen uint64) error
	// LiveMembers lists registry members that have not acknowledged cancellation (the registry, not the tree).
	LiveMembers(ctx context.Context, gen uint64) ([]string, error)
	OpenRuns(ctx context.Context) ([]RunInfo, error)
}

// Survivor is a process or unit that Stop could not prove gone.
type Survivor struct {
	PID   int
	Stamp string
	Comm  string
	Sub   string
	Why   string
}

// UnitOutcomeKind is the result of ending a containment unit.
type UnitOutcomeKind int

const (
	// UnitEnded: every member signalled, escalated and proven gone (rmdir succeeded).
	UnitEnded UnitOutcomeKind = iota + 1
	// UnitEndedWithUnknown: ended, but a member had no registry row or an unreadable identity.
	UnitEndedWithUnknown
	// UnitStragglers: a member survived the kill window.
	UnitStragglers
	// UnitNotProven: the identity of the unit could not be proven, so nothing was signalled.
	UnitNotProven
	// UnitNone: the run never had a unit (nothing to end).
	UnitNone
	// UnitBestEffort: the unit is gone as far as its group can tell, but the mechanism cannot prove that no process
	// left the group (a Windows Job Object, a macOS process group). It is never reported as a bare "stopped".
	UnitBestEffort
)

// UnitOutcome is what Ender.End returns.
type UnitOutcome struct {
	Kind      UnitOutcomeKind
	Survivors []Survivor
	Unknown   []Survivor
	Note      string
}

// Ender ends the containment unit(s) of a run generation, excluding the ticket sub-units of begun operations.
type Ender interface {
	End(ctx context.Context, gen uint64, exclude []uint64) UnitOutcome
}

// Step is one recorded step of a Stop.
type Step struct {
	Name string
	Done bool
	Err  error
}

// Report is everything a Stop did and did not do. Only Stopped() allows the word "stopped".
type Report struct {
	Generation uint64
	Steps      []Step
	Marker     MarkerStatus
	Final      RunState
	Gate       *Outcome
	Host       *HostOutcome // the host-side gate as a Stop from another process learnt it (Gate is nil then)
	Unit       UnitOutcome
	Members    []string
	Pending    []uint64 // tickets of begun operations still excluded from the kill
}

func (r *Report) step(name string, err error) bool {
	r.Steps = append(r.Steps, Step{Name: name, Done: err == nil, Err: err})
	return err == nil
}

func (r Report) done(name string) bool {
	for _, s := range r.Steps {
		if s.Name == name {
			return s.Done
		}
	}
	return false
}

// Stopped is true only when every step is recorded and the run is quiescent: no survivor, no unknown process,
// no live member, the barrier reached, and the unit's end PROVEN. A best-effort unit is never Stopped.
func (r Report) Stopped() bool { return r.Unit.Kind != UnitBestEffort && r.settled() }

// StoppedBestEffort is true when everything Stopped needs holds except that the unit's mechanism cannot prove that no
// process left the group (UnitBestEffort). It is the only way such a platform is reported as ended.
func (r Report) StoppedBestEffort() bool { return r.Unit.Kind == UnitBestEffort && r.settled() }

func (r Report) settled() bool {
	if r.Final != StateQuiescent {
		return false
	}
	for _, s := range r.Steps {
		if !s.Done {
			return false
		}
	}
	return true
}

// BestEffortNote is the limit a best-effort stop states wherever it says "stopped".
const BestEffortNote = "best effort: processes that left the group are not covered"

// ExitCode is non-zero unless Stopped or StoppedBestEffort.
func (r Report) ExitCode() int {
	if r.Stopped() || r.StoppedBestEffort() {
		return 0
	}
	return 1
}

// Lines renders the report. "stopped" never appears while a survivor, an unknown process or an unrecorded
// step exists; "NOT RECORDED" is the marker step's failure, the others print their own state.
func (r Report) Lines() []string {
	var out []string
	for _, s := range r.Steps {
		switch {
		case s.Done:
			out = append(out, fmt.Sprintf("%s: recorded", s.Name))
		case s.Name == StepMarker:
			out = append(out, fmt.Sprintf("NOT RECORDED: %s (%v)", s.Name, s.Err))
		default:
			out = append(out, fmt.Sprintf("%s: not done (%v)", s.Name, s.Err))
		}
	}
	if !r.done(StepRevoke) && r.done(StepIntent) {
		out = append(out, "stop requested, authority not yet revoked")
	}
	if r.Marker == MarkerUnsigned || r.Marker == MarkerCorrupt {
		out = append(out, fmt.Sprintf("the stop marker could not be authenticated (%s); the generation is stopped anyway", r.Marker))
	}
	for _, s := range r.Unit.Survivors {
		out = append(out, fmt.Sprintf("survivor: pid %d (%s) in %s %s", s.PID, s.Comm, s.Sub, s.Why))
	}
	for _, s := range r.Unit.Unknown {
		out = append(out, fmt.Sprintf("unknown process: pid %d (%s) %s", s.PID, s.Comm, s.Why))
	}
	if r.Unit.Kind == UnitNotProven {
		out = append(out, "identity not proven, nothing was signalled: "+r.Unit.Note)
	}
	if r.Host != nil && !r.hostClean() {
		out = append(out, "the host's gate is not proven closed: "+r.Host.Note)
	}
	for _, m := range r.Members {
		out = append(out, "live member: "+m)
	}
	if len(r.Pending) > 0 {
		out = append(out, fmt.Sprintf("a begun operation is finishing (tickets %v)", r.Pending))
	}
	if r.Stopped() {
		out = append(out, fmt.Sprintf("generation %d stopped (quiescent)", r.Generation))
	} else if r.StoppedBestEffort() {
		out = append(out, fmt.Sprintf("generation %d stopped (%s)", r.Generation, BestEffortNote))
	} else {
		out = append(out, fmt.Sprintf("generation %d NOT stopped: state %s", r.Generation, r.Final))
	}
	return out
}

// Step names.
const (
	StepMarker = "stop marker"
	StepIntent = "database intent"
	StepRevoke = "token revocation and queue cancel"
	StepEnd    = "containment end"
)

// ProgressIntent is the line Stopper.Progress receives after the marker is written.
const ProgressIntent = "marker written; recording intent..."

// Stopper runs the Stop order: marker first, then the database intent, then token revocation and queue cancel,
// then process kill. Gate is the host's own gate when Stop runs inside the host. A Stop in another process (the CLI)
// has no Gate and must have a Host link instead: with neither, the host side is not proven and the Stop never reaches
// quiescent (fail closed: it does not skip what it cannot see).
type Stopper struct {
	Dir     RunDir
	Store   RunStore
	Gate    *Gate
	Host    HostLink
	Ender   Ender
	Clock   Clock
	Retries int           // database attempts, default 5
	Backoff time.Duration // base back-off, default 50ms, jittered
	Hook    Hook
	// Progress, when set, receives one line per Stop call (Stop runs once per open run) once the marker is on disk and before the database step starts (the step that may
	// wait on a busy database for tens of seconds). Never called when the marker was not written; no retry runs behind it.
	Progress func(string)
	// Unrecorded is set when a stop was requested but the marker or the intent is not yet durable.
	Unrecorded *UnrecordedFlag
}

func (s *Stopper) retry(ctx context.Context, f func() error) error {
	n, base := s.Retries, s.Backoff
	if n == 0 {
		n = 5
	}
	if base == 0 {
		base = 50 * time.Millisecond
	}
	var err error
	for i := 0; i < n; i++ {
		if err = f(); err == nil {
			return nil
		}
		d := base<<i/2 + time.Duration(rand.Int64N(int64(base)))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.Clock.After(d):
		}
	}
	return err
}

// Stop performs the whole order for one generation. Emergency actions act first and record best effort: a
// failed step never stops the later ones, it is recorded and printed.
func (s *Stopper) Stop(ctx context.Context, gen uint64, charterID, cause string) Report {
	rep := Report{Generation: gen}
	if s.Unrecorded != nil {
		s.Unrecorded.Set()
	}
	res, merr := s.Dir.WriteMarker(gen, charterID, cause)
	rep.step(StepMarker, merr)
	rep.Marker = res.Status
	s.Hook.at("marker.recorded")
	if merr == nil && s.Progress != nil {
		s.Progress(ProgressIntent)
	}
	if s.Gate != nil {
		s.Gate.Seal()
		s.Hook.at("gate.closed")
	}
	ierr := s.retry(ctx, func() error { return s.Store.Advance(ctx, gen, StateStopRequested, cause) })
	rep.step(StepIntent, ierr)
	s.Hook.at("db.intent.committed")
	rerr := s.Store.RevokeTokens(ctx, gen)
	if rerr == nil {
		rerr = s.Store.CancelQueue(ctx, gen)
	}
	if rerr == nil {
		rerr = s.Store.Advance(ctx, gen, StateAuthorityRevoked, cause)
	}
	rep.step(StepRevoke, rerr)
	if merr == nil && ierr == nil && s.Unrecorded != nil {
		s.Unrecorded.Clear()
	}
	if s.Gate != nil {
		o := s.Gate.Close()
		rep.Gate = &o
		rep.Pending = s.Gate.ContinuingTickets()
	} else {
		ho := HostOutcome{Kind: HostUnproven, Note: "no host link is wired, so the host's gate cannot be shown to be closed"}
		if s.Host != nil {
			ho = s.Host.CloseHost(ctx, gen)
		}
		rep.Host = &ho
		rep.Pending = ho.Pending
	}
	rep.Unit = s.Ender.End(ctx, gen, rep.Pending)
	rep.step(StepEnd, unitErr(rep.Unit))
	members, lerr := s.Store.LiveMembers(ctx, gen)
	rep.Members = members
	if lerr != nil {
		rep.step("registry read", lerr)
	}
	rep.Final = s.settle(ctx, gen, &rep, cause)
	return rep
}

// hostClean says the host side holds nothing: its own gate is a BARRIER, or a linked host closed with nothing live, or none holds
// the generation. No gate and no link is not clean.
func (r Report) hostClean() bool {
	switch {
	case r.Gate != nil:
		return r.Gate.Kind == Barrier
	case r.Host != nil:
		return r.Host.Kind == HostNone || r.Host.Kind == HostClosed
	}
	return false
}

func unitErr(u UnitOutcome) error {
	switch u.Kind {
	case UnitEnded, UnitNone, UnitBestEffort:
		return nil
	}
	return fmt.Errorf("unit not proven gone (%d)", u.Kind)
}

// settle moves the state to quiescent only when every condition holds, otherwise to cleanup-pending.
func (s *Stopper) settle(ctx context.Context, gen uint64, rep *Report, cause string) RunState {
	cur, err := s.Store.State(ctx, gen)
	if err != nil {
		cur = StateStopRequested
	}
	if !rep.done(StepIntent) || !rep.done(StepRevoke) {
		return cur
	}
	clean := rep.done(StepEnd) && len(rep.Members) == 0 && len(rep.Pending) == 0 && len(rep.Unit.Unknown) == 0 &&
		rep.hostClean()
	for _, st := range rep.Steps {
		clean = clean && st.Done
	}
	to := StateCleanupPending
	if clean {
		to = StateQuiescent
	}
	if err := s.Store.Advance(ctx, gen, to, cause); err != nil {
		return cur
	}
	return to
}
