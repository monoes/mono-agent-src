// Package store holds the durable state of the open-ended ("doomsday") mode: the insert-only charter, the run
// state machine, the membership registry, the MAC-chained audit stream and the cumulative counters. Other
// doomsday packages code against the Store interface below and never touch the tables.
//
// The mode ships off: nothing in this package starts a run by itself, Enter needs a charter whose hash the
// operator has confirmed, and every transition fails closed on a row it cannot verify.
package store

import (
	"context"
	"database/sql"
	"errors"
)

// RunState is the state of a run. It moves only forward, one step at a time (spec 5.1).
type RunState string

const (
	StateRunning          RunState = "running"
	StateStopRequested    RunState = "stop-requested"
	StateAuthorityRevoked RunState = "authority-revoked"
	StateCleanupPending   RunState = "cleanup-pending"
	StateQuiescent        RunState = "quiescent"
)

// Lifted authority line names. The platform-caps line is never part of "everything" (Q4).
const (
	LineSpending     = "spending"
	LineDestructive  = "destructive_local"
	LineVault        = "secrets_vault"
	LineThirdParty   = "outbound_third_party"
	LineInstall      = "install_software"
	LineHostSettings = "host_settings"
	LinePlatformCaps = "platform_caps"
)

// MaxTreeDepth is the charter maximum for tree_depth (Q1); DefaultTreeDepth is the default.
const (
	MaxTreeDepth     = 12
	DefaultTreeDepth = 4
)

// Criterion is one success criterion; Kind is "command", "test", "file", "output" or "rubric".
type Criterion struct {
	Kind     string `json:"kind"`
	Spec     string `json:"spec"`
	Expected string `json:"expected"`
}

// Limits are the named run limits (spec 3.1). BudgetMicro is micro-dollars; NoCeiling is the explicit "no ceiling".
type Limits struct {
	TreeDepth     int   `json:"tree_depth"`
	LivePerTree   int   `json:"live_per_tree"`
	SpawnRate     int   `json:"spawn_rate"`
	OpenSessions  int   `json:"open_sessions"`
	MaxConcurrent int   `json:"max_concurrent"`
	MaxTotal      int   `json:"max_total"`
	BudgetMicro   int64 `json:"budget_micro"`
	NoCeiling     bool  `json:"no_ceiling"`
	// The resource governor's numbers (canon version 3). Zero means the governor's compile-time default. They can only
	// bound the queue and name the floor of the governor; none of them can raise max_concurrent.
	QueueMaxRows      int   `json:"queue_max_rows,omitempty"`
	QueueMaxBytes     int64 `json:"queue_max_bytes,omitempty"`
	SafeMinConcurrent int   `json:"safe_min_concurrent,omitempty"`
}

// PerAsk are the limits inside which the boss may auto-approve (Q5). Declared false means nothing is auto-approved.
type PerAsk struct {
	Declared   bool   `json:"declared"`
	SpendMicro int64  `json:"ask_spend_micro"`
	Tasks      int    `json:"ask_tasks"`
	Access     string `json:"ask_access"`
	Depth      int    `json:"ask_depth"`
	// RunSpendMicro and RunTasks cap the run's cumulative auto-approved totals, so many small asks cannot add up to
	// what one large ask would have been (note 13.4). Required, and at least the per-ask value, once Declared.
	RunSpendMicro int64 `json:"run_spend_micro"`
	RunTasks      int64 `json:"run_tasks"`
	// AnswerClasses are the answer classes the boss may auto-answer (ask.answer). Empty means ask.answer is off and
	// every critical ask stays a person item.
	AnswerClasses []string `json:"answer_classes"`
}

// Pin fixes a model and policy for the whole run (Q6).
type Pin struct {
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	Version       string `json:"version"`
	PurposePolicy string `json:"purpose_policy"`
}

// Charter is the operator's declaration. Profile and Generation are part of the hash.
type Charter struct {
	Profile     string      `json:"profile"`
	Generation  int64       `json:"generation"`
	Goal        string      `json:"goal"`
	Criteria    []Criterion `json:"criteria"`
	Limits      Limits      `json:"limits"`
	Lines       []string    `json:"lines"`
	BossPin     Pin         `json:"boss_pin"`
	VerifierPin Pin         `json:"verifier_pin"`
	PerAsk      PerAsk      `json:"per_ask"`
	// Typed carries the constraints of the typed operations (note 13.4); empty enables none of them.
	Typed Typed `json:"typed"`

	// legacy marks a charter loaded from a row written under LegacyCanonVersion: it encodes without the governor's limits
	// so its stored hash, id and MAC still verify. Only the loader sets it.
	legacy bool
}

// Run is the run row of one generation.
type Run struct {
	Profile    string
	Generation int64
	CharterID  string
	State      RunState
	IntentAt   int64
	IntentBy   string
	Cause      string
}

// Member is one registered live piece of the run (a session, a tool process, a unit).
type Member struct {
	Profile    string
	Generation int64
	ID         string
	Kind       string
	Pid        int
	Stamp      string
	Live       bool
}

// AuditRow is one row of the MAC-chained audit stream.
type AuditRow struct {
	Profile    string
	Generation int64
	Seq        int64
	Kind       string
	Payload    string
}

// Refusal is returned by Admit, Register and ConsumeCumulative when the run does not admit the work.
type Refusal struct {
	Reason string
}

func (r *Refusal) Error() string { return "doomsday: refused: " + r.Reason }

// Sentinel errors.
var (
	ErrUnverifiable  = errors.New("doomsday: stored authority cannot be verified")
	ErrBadTransition = errors.New("doomsday: run state moves forward one step only")
	ErrLiveMembers   = errors.New("doomsday: live members are still registered")
	ErrActiveRun     = errors.New("doomsday: the profile already has an active run")
	// ErrCommitUnknown: the entry's transaction was sent to commit and the commit reported an error, so the run row may or may not exist.
	ErrCommitUnknown   = errors.New("doomsday: the outcome of the entry's commit is unknown")
	ErrGenerationMoved = errors.New("doomsday: the generation is not the next one")
	ErrHashMismatch    = errors.New("doomsday: charter hash differs from the confirmed hash")
	ErrAlreadyConsumed = errors.New("doomsday: approval already consumed")
	ErrOverLimit       = errors.New("doomsday: cumulative limit would be exceeded")
	ErrInvalidCharter  = errors.New("doomsday: invalid charter")
	ErrUnknownSchema   = errors.New("doomsday: unknown schema version")
)

// DBTX is what Admit needs, so a caller can run it inside its own transaction.
type DBTX interface {
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

// Keyer derives MACs. Domain separates the uses ("charter", "run", "audit", "registry"). The real implementation
// derives its key from the profile's vault key; a failure to obtain the key makes every verify fail closed.
type Keyer interface {
	MAC(domain string, parts ...[]byte) ([]byte, error)
}

// Store is the narrow surface the other doomsday packages use.
type Store interface {
	// NextGeneration is max(recorded generations of the profile)+1. The hash shown to the operator is of a charter carrying it.
	NextGeneration(ctx context.Context, profile string) (int64, error)
	// Enter inserts the charter and its running run in one transaction, re-hashing it there and comparing with confirmed.
	Enter(ctx context.Context, c Charter, confirmed [32]byte, by string) (Run, error)
	LoadCharter(ctx context.Context, profile string, generation int64) (Charter, error)
	// GetRun returns the verified run row.
	GetRun(ctx context.Context, profile string, generation int64) (Run, error)
	// Advance moves the run to the next state; to must be the single next state.
	Advance(ctx context.Context, profile string, generation int64, to RunState, by, cause string) (Run, error)
	// Admit refuses (with *Refusal) unless the run is verified, running, and of this generation.
	Admit(ctx context.Context, q DBTX, profile string, generation int64) error
	// Register adds a live member; the same statement checks the run is running.
	Register(ctx context.Context, m Member) error
	Release(ctx context.Context, profile string, generation int64, id string) error
	LiveMembers(ctx context.Context, profile string, generation int64) ([]Member, error)
	AppendAudit(ctx context.Context, profile string, generation int64, kind, payload string) (AuditRow, error)
	VerifyAudit(ctx context.Context, profile string, generation int64) error
	// ConsumeCumulative atomically adds n to the counter kind if the total stays within limit; approvalID is unique.
	ConsumeCumulative(ctx context.Context, profile string, generation int64, kind, approvalID string, n, limit int64) error
}
