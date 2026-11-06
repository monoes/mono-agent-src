package doomsday

import (
	"context"
	"errors"
	"time"
)

// ErrOff is why the mode refuses on an install that was not built with it.
var ErrOff = errors.New("open-ended (doomsday) mode is not available on this install")

// Canon is the canonical encoding of a charter and its hash, as the store
// computes them (bound to the profile and the next run generation).
type Canon struct {
	Bytes []byte
	Hash  string // full lowercase hex SHA-256
	// Handle is the engine's own form of the charter, handed back to Enter.
	Handle any
}

// Ack is who acknowledged the lift, recorded with the run (Q9).
type Ack struct {
	Kind  string // "terminal": the typed phrase; "script": --yes-i-lift with the full hash
	Lines int    // lifted lines the acknowledgement named
}

// Entry is a started run.
type Entry struct {
	RunID      string `json:"run_id"`
	CharterID  string `json:"charter_id"`
	Hash       string `json:"hash"`
	Generation int64  `json:"generation"`
	// AnchorBound: the OS secret store holds the run's anchor. When false, AnchorNote says why rollback detection is weaker.
	AnchorBound bool   `json:"anchor_bound"`
	AnchorNote  string `json:"anchor_note,omitempty"`
}

// StopOutcome is what Stop did, in the order it did it.
type StopOutcome struct {
	MarkerRecorded bool     // the Stop marker was durably written first
	IntentRecorded bool     // database intent
	Survivors      []string // processes still alive after the kill step
	// Incomplete holds the report lines of any run that is not fully stopped (a step not done, the run not
	// quiescent). Non-empty means "stopped" is never printed.
	Incomplete []string
	// Line, when the engine sets it, is the first line of a stop that is not a bare "stopped" (a best-effort stop says so in
	// it). It must start with "stopped"; the command prints it, cleaned, and passes it on as the verdict.
	Line string
	// Runs are the runs this Stop proved stopped (every step done): the only ones whose sessions AfterStop may end.
	Runs []RunRef
}

// RunRef names one run of the profile: its charter and generation, as stamped on its boss session.
type RunRef struct {
	CharterID  string
	Generation int64
}

// Status is the read-only view of the profile's run.
type Status struct {
	Active     bool     `json:"active"`
	RunID      string   `json:"run_id,omitempty"`
	CharterID  string   `json:"charter_id,omitempty"`
	Hash       string   `json:"hash,omitempty"`
	State      string   `json:"state,omitempty"`
	Generation int64    `json:"generation,omitempty"`
	Lines      []string `json:"lines,omitempty"`
	Members    int      `json:"members,omitempty"`
	AuditSeq   int64    `json:"audit_seq,omitempty"`
	AuditHead  string   `json:"audit_head,omitempty"` // first 16 hex of the chain head MAC
	// Finish is the state of a finish (note 13.6): quiescent while the freeze is awaited, verifying, finish-parked or
	// finished; empty for a run that is not finishing. FinishReason is the closed park reason of a parked finish.
	Finish       string `json:"finish,omitempty"`
	FinishReason string `json:"finish_reason,omitempty"`
	FinishRound  int64  `json:"finish_round,omitempty"`
}

// Engine is what the store and stop packages give the CLI. Enter must
// re-compute the hash over Canon.Bytes inside the entry transaction and refuse
// when it differs from ExpectedHash.
type Engine interface {
	// Containment is nil only where the mode can contain its processes (cgroup
	// v2 delegation probed); anywhere else it is the reason the mode refuses.
	Containment() error
	Canonical(ctx context.Context, c Charter) (Canon, error)
	Enter(ctx context.Context, canon Canon, expectedHash string, ack Ack) (Entry, error)
	Stop(ctx context.Context) (StopOutcome, error)
	Status(ctx context.Context) (Status, error)
}

// AfterStopper is an Engine that moves what hangs on a stopped run to a defined state. The stop verb calls it only
// after it has printed nothing but a proven (or best-effort) stop; verdict is the exact stop line it prints. It returns
// the lines to print after the verdict; what it could not do is a line that says so, and Failed is true then (the stop
// verb exits ExitCleanupFailed: the run IS stopped, its cleanup is not complete).
type AfterStopper interface {
	AfterStop(ctx context.Context, o StopOutcome, verdict string, at time.Time) AfterReport
}

// AfterReport is what the step after a stop printed and whether any part of it failed.
type AfterReport struct {
	Lines  []string
	Failed bool
}

// ExitCleanupFailed is the exit code of a stop that stopped the run but could not end every session or write the chat
// message (AGENTS.md exit-code table). The first line printed is still the verdict; a later stop finishes the cleanup.
const ExitCleanupFailed = 8

// StopRepairer is an Engine that can finish what the last stop left: asked only when a stop found no open run (ErrNoOpenRun).
// RepairNothing keeps the "no open run" failure; RepairDone prints its lines and exits 0; RepairFailed prints them and exits 1.
type StopRepairer interface {
	RepairStop(ctx context.Context, at time.Time) ([]string, RepairStatus)
}

// BossBinder is an Engine that can record which session is the run's boss (`start --boss-session`).
type BossBinder interface {
	BindBoss(ctx context.Context, session int64, entry Entry) error
}

// BindingRequirer is an Engine that also knows whether peer-credential binding is on: a run needs bound turns, so
// `start` asks it before showing the charter (the StoreEngine asks again in Enter).
type BindingRequirer interface {
	RequireBinding(ctx context.Context) error
}

// Off is the Engine of an install that does not carry the mode: it can do
// nothing, so no run can start.
type Off struct{}

func (Off) Containment() error                                       { return ErrOff }
func (Off) Canonical(context.Context, Charter) (Canon, error)        { return Canon{}, ErrOff }
func (Off) Enter(context.Context, Canon, string, Ack) (Entry, error) { return Entry{}, ErrOff }
func (Off) Stop(context.Context) (StopOutcome, error)                { return StopOutcome{}, ErrOff }
func (Off) Status(context.Context) (Status, error)                   { return Status{}, nil }
