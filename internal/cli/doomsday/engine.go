package doomsday

import (
	"context"
	"errors"
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
	Kind  string // "terminal": the typed phrase; "script": --yes-i-lift with the full hash; "gui": the phrase typed in the app form, re-derived by the CLI
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
	// BestEffort: every run ended, but a unit's mechanism cannot prove that no process left its group (Windows, macOS):
	// the command prints the limit with the word and never a bare "stopped".
	BestEffort bool
	// Incomplete holds the report lines of any run that is not fully stopped (a step not done, the run not
	// quiescent). Non-empty means "stopped" is never printed.
	Incomplete []string
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

// BossBinder is an Engine that can record which session is the run's boss (`start --boss-session`).
type BossBinder interface {
	BindBoss(ctx context.Context, session int64, entry Entry) error
}

// BossChecker is an Engine that can say, BEFORE a run starts, whether a session can be the run's boss. `start` asks it so
// that a stale or ineligible --boss-session leaves no started full-access run behind (BindBoss runs after Enter).
type BossChecker interface {
	CheckBoss(ctx context.Context, session int64) error
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
