// Package governor is the resource governor and scheduler of the open-ended ("doomsday") mode: width is unlimited as
// a QUEUE, not as simultaneous processes. This file holds the contract the sub-packages (machine, policy, slots, queue,
// sched) are built against and the integrator wires; it has no behaviour, no init() side effect and no registration.
//
// Authority rules the contract encodes:
//   - the governor only LOWERS concurrency below the charter cap; nothing here can raise it;
//   - a machine reading that fails, times out or panics lowers to the safe minimum, never to the cap;
//   - the governor is never on the Stop path and never kills or pauses a running session;
//   - there is no method that writes governor configuration: caps and bounds come from the verified charter row.
package governor

import (
	"context"
	"errors"
	"time"
)

// Compile-time defaults (decided 2026-11-03). They are not settings: changing one is a code change.
const (
	// DefaultSafeMin is the concurrency used when the machine cannot be read (charter safe_min_concurrent may name 1..cap).
	DefaultSafeMin = 2
	// DefaultQueueMaxRows and DefaultQueueMaxBytes bound the pending queue of one run when the charter names none.
	DefaultQueueMaxRows  = 500
	DefaultQueueMaxBytes = 8 << 20

	// LoadFactor: lower concurrency when the 1-minute load average exceeds LoadFactor x cores.
	LoadFactor = 0.8
	// MemLowFrac lowers concurrency below this available-memory fraction; MemStopFrac stops admitting.
	MemLowFrac  = 0.15
	MemStopFrac = 0.08
	// DiskLowBytes and DiskLowFrac lower concurrency when free space on the data or scratch directory is under
	// either bound.
	DiskLowBytes = 10 << 30
	DiskLowFrac  = 0.10
)

// Kind is the slot kind. A session launch takes one Session and one Process slot, a tool process one Process slot,
// the root one Process slot only (spec 3.1 item 5).
type Kind string

const (
	KindSession Kind = "session"
	KindProcess Kind = "process"
)

// Class is the queue priority class; Boss is served first, within a class leads are served round robin.
type Class string

const (
	ClassBoss   Class = "boss"
	ClassNormal Class = "normal"
)

// Limits are the charter's numbers for one run, read from the verified charter row and passed in; there is no setter.
// Zero QueueMaxRows/QueueMaxBytes/SafeMin mean the defaults above.
type Limits struct {
	Profile       string
	Generation    int64
	MaxConcurrent int // process slots, the ceiling of every decision
	LivePerTree   int // session slots per tree
	SafeMin       int
	QueueMaxRows  int
	QueueMaxBytes int64
}

// WithDefaults fills the zero queue bounds and the safe minimum, and clamps the safe minimum to 1..MaxConcurrent.
func (l Limits) WithDefaults() Limits {
	if l.QueueMaxRows == 0 {
		l.QueueMaxRows = DefaultQueueMaxRows
	}
	if l.QueueMaxBytes == 0 {
		l.QueueMaxBytes = DefaultQueueMaxBytes
	}
	if l.SafeMin == 0 {
		l.SafeMin = DefaultSafeMin
	}
	if l.SafeMin > l.MaxConcurrent {
		l.SafeMin = l.MaxConcurrent
	}
	if l.SafeMin < 1 {
		l.SafeMin = 1
	}
	return l
}

// Sample is one reading of the machine. A field the platform cannot give is left at its zero value together with
// Valid false; the policy treats an invalid sample like a failed one.
type Sample struct {
	Valid         bool
	Load1         float64 // 1-minute load average
	Cores         int
	MemAvailFrac  float64 // available / total, 0..1
	DiskFreeBytes uint64  // the smaller of the data and scratch directories
	DiskFreeFrac  float64
}

// Sampler reads the machine. An error, a timeout or a panic in an implementation is the safe minimum for the caller;
// an implementation never returns a made-up healthy value. Platforms without a sampler return ErrUnsupported.
type Sampler interface {
	Sample(ctx context.Context) (Sample, error)
}

// ErrUnsupported is a sampler's answer on a platform it cannot read.
var ErrUnsupported = errors.New("doomsday governor: no machine sampler on this platform")

// Clock is monotonic time: a wall-clock change never changes a cap or a backoff window.
type Clock interface {
	// Now is a monotonic reading; only differences between readings mean anything.
	Now() time.Duration
}

// Reason says why the effective value is below the cap.
type Reason string

const (
	ReasonNone       Reason = ""
	ReasonLoad       Reason = "load"
	ReasonMemory     Reason = "memory"
	ReasonMemoryStop Reason = "memory-stop-admitting"
	ReasonDisk       Reason = "disk"
	ReasonProvider   Reason = "provider-backoff"
	ReasonSampleFail Reason = "sample-failed"
	ReasonRecovering Reason = "recovering"
)

// Effective is the governor's decision. Value is in SafeMin..Cap; Admitting is false only when memory is under
// MemStopFrac (nothing new starts; nothing running is touched).
type Effective struct {
	Cap       int
	Value     int
	Admitting bool
	Reasons   []Reason
}

// ProviderEffective is Effective narrowed to one provider key: only that provider's launches use it.
type ProviderEffective struct {
	Provider string
	Value    int
	Until    time.Duration // monotonic reading when the backoff ends
}

// Policy is the pure core: effective = min(cap, cpu, mem, disk, provider) with hysteresis and a floor. It has no I/O.
// Implementations must satisfy SafeMin <= Value <= Cap for every input, including NaN, negative and missing values.
type Policy interface {
	// Decide takes the latest sample (or the error of reading it) and returns the decision. It keeps the hysteresis
	// state (recovery needs several consecutive good samples) inside the implementation.
	Decide(l Limits, s Sample, sampleErr error) Effective
	// RateLimited records a provider 429; retryAfter 0 means "no header". Only the provider name and a number cross.
	RateLimited(provider string, retryAfter time.Duration)
	// Provider is the narrowed limit for one provider at this time (Value == Cap when it is not backing off).
	Provider(l Limits, provider string) ProviderEffective
}

// Request asks for slots for one member.
type Request struct {
	Kind     Kind
	Tree     string // the session tree; required for KindSession
	MemberID string // the registry member the slots belong to
}

// Refusal reasons of slot reservation and queue admission.
var (
	ErrNoSlot      = errors.New("doomsday governor: no free slot")
	ErrNotRunning  = errors.New("doomsday governor: the run is not running")
	ErrQueueFull   = errors.New("doomsday governor: the queue is full")
	ErrNotQueued   = errors.New("doomsday governor: the item is not queued")
	ErrAlreadyHeld = errors.New("doomsday governor: the member already holds that slot")
)

// Slots reserves and releases process and session slots. Every call runs in one immediate database transaction: the
// live count is read and the slot inserted atomically, against min(charter cap, effective) passed in as limit, so the
// last slot is admitted exactly once even across two processes on one database.
type Slots interface {
	// Reserve takes the slots a Request needs (a session: both kinds, all or none). limit is the governor's effective
	// concurrency for process slots; it never exceeds the charter cap (the store refuses above it regardless).
	Reserve(ctx context.Context, l Limits, limit int, r Request) error
	// Release frees every live slot of the member. Releasing a member without slots is not an error.
	Release(ctx context.Context, profile string, generation int64, memberID string) error
	// Live counts live slots (tree "" counts every tree).
	Live(ctx context.Context, profile string, generation int64, k Kind, tree string) (int, error)
	// Reconcile releases slots whose member is no longer live in the registry (orphans of a crash); it reports how many.
	Reconcile(ctx context.Context, profile string, generation int64) (int, error)
}

// Item is one pending launch. Payload carries references only: no token, key, credential or vault value.
type Item struct {
	QID     int64 // set by the queue
	Lead    string
	Class   Class
	Payload []byte
	EnqAt   int64 // unix seconds, set by the queue
}

// Queue is the bounded durable queue of pending launches. The row and byte bounds are enforced in the insert itself
// (and by the migration's triggers), never only in Go.
type Queue interface {
	// Enqueue adds an item or refuses with ErrQueueFull (rows or bytes) or ErrNotRunning; a refusal writes nothing.
	Enqueue(ctx context.Context, l Limits, it Item) (int64, error)
	// Peek lists pending items in service order for the scheduler: boss class first, then leads round robin, FIFO
	// within a lead. It does not change state. max bounds the answer.
	Peek(ctx context.Context, profile string, generation int64, max int) ([]Item, error)
	// MarkAdmitted moves one item queued -> admitted (only while the run is running); ErrNotQueued otherwise.
	MarkAdmitted(ctx context.Context, profile string, generation int64, qid int64) error
	// CancelAll moves every queued item to cancelled and returns how many; used by Stop and on quiescence. A stopped
	// run's queue never resumes.
	CancelAll(ctx context.Context, profile string, generation int64) (int, error)
	// Depth is the pending count and pending bytes.
	Depth(ctx context.Context, profile string, generation int64) (rows int, bytes int64, err error)
}

// ProviderSnapshot is one provider's backoff state for the read model.
type ProviderSnapshot struct {
	Provider string
	Value    int
	Until    time.Duration
}

// Snapshot is the read-only view B4 and B5 consume (`doomsday governor status --json`). It carries numbers and
// reason names only; B4 must not call Slots or Queue directly.
type Snapshot struct {
	Cap           int
	Effective     int
	Admitting     bool
	Reasons       []Reason
	LiveProcess   int
	LiveSession   int
	QueueRows     int
	QueueBytes    int64
	QueueMaxRows  int
	QueueMaxBytes int64
	Providers     []ProviderSnapshot
	LastSample    time.Time
}

// Governor is what the integrator builds for a run and the coder-host seams call. It has no method that raises a
// cap, writes a limit or touches a running member; it is not consulted by Stop.
type Governor interface {
	// Effective is the current decision for the run.
	Effective(ctx context.Context) Effective
	// TryAdmit reserves slots for r if the effective limit (and the provider's, when provider is not empty) allows,
	// else returns ErrNoSlot, ErrNotRunning or the run's refusal. It never blocks on the machine sampler.
	TryAdmit(ctx context.Context, provider string, r Request) error
	// Enqueue parks a launch that could not be admitted; a full queue is a visible ErrQueueFull.
	Enqueue(ctx context.Context, it Item) (int64, error)
	// ReportRateLimit feeds a provider 429/Retry-After; only the provider name and a duration cross the seam.
	ReportRateLimit(provider string, retryAfter time.Duration)
	// Release frees a member's slots and wakes the scheduler.
	Release(ctx context.Context, memberID string) error
	// Snapshot is the read model; it is safe to call at any time and never blocks Stop.
	Snapshot(ctx context.Context) (Snapshot, error)
}
