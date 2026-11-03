// Package stop implements the Stop side of the open-ended ("doomsday") mode: the dispatch barrier (gate), the
// stop marker, the emergency channel, recovery, the Stop order and the containment-unit end procedure.
//
// The package is self-contained: it codes against the narrow interfaces below (Registry, RunStore, Units, ...)
// and the store package satisfies them. Nothing here makes the mode reachable; the mode ships off, and Probe
// refuses to start it wherever containment is unavailable.
package stop

import (
	"errors"
	"sync"
	"time"
)

// Clock is the injected time source for every deadline in this package.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

// SystemClock is the production clock.
type SystemClock struct{}

func (SystemClock) Now() time.Time                         { return time.Now() }
func (SystemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Hook is a named pause point a test can block on; nil means no-op (production).
type Hook func(point string)

func (h Hook) at(point string) {
	if h != nil {
		h(point)
	}
}

// Defaults of note 13.1; starting values to be measured, not charter fields.
const (
	DefaultTClose = 2 * time.Second
	DefaultTHung  = 30 * time.Second
	DefaultTMerge = 10 * time.Minute
	// DefaultTMergeHard bounds how long past T_merge a step that is still running keeps its operation open
	// (note 13.3 R2); after it the operation is INCOMPLETE and the kill is no longer deferred.
	DefaultTMergeHard = 10 * time.Minute
)

// Kind is the effect kind of a ticket.
type Kind int

const (
	// KindE1 is a process start (created held, registered, released).
	KindE1 Kind = iota + 1
	// KindE3 is a remote request.
	KindE3
)

// TicketState is the state of one admitted effect.
type TicketState int

const (
	Admitted TicketState = iota + 1
	Starting
	Started
	Aborting
	Aborted
	Unknown
	Fenced
)

func (s TicketState) String() string {
	switch s {
	case Admitted:
		return "ADMITTED"
	case Starting:
		return "STARTING"
	case Started:
		return "STARTED"
	case Aborting:
		return "ABORTING"
	case Aborted:
		return "ABORTED"
	case Unknown:
		return "UNKNOWN"
	case Fenced:
		return "FENCED"
	}
	return "?"
}

// OutcomeKind is the result of Close (note 13.1 section 4.2).
type OutcomeKind int

const (
	Barrier OutcomeKind = iota + 1
	ClosedWithUnknown
	ClosedWithOperation
	ClosedWithStarters
)

func (k OutcomeKind) String() string {
	switch k {
	case Barrier:
		return "BARRIER"
	case ClosedWithUnknown:
		return "CLOSED-WITH-UNKNOWN"
	case ClosedWithOperation:
		return "CLOSED-WITH-OPERATION"
	case ClosedWithStarters:
		return "CLOSED-WITH-STARTERS"
	}
	return "?"
}

// Outcome names every live starter at the instant it was computed; it is recomputed on every transition.
type Outcome struct {
	Kind       OutcomeKind
	Starters   []uint64 // tickets STARTING (of any origin, continuation tickets included)
	Aborting   []uint64 // tickets whose abort write is not durable yet
	Unknown    []uint64 // tickets UNKNOWN
	Operations []string // begun operations still CONTINUING
}

// Sentinel errors.
var (
	ErrGateClosed    = errors.New("stop: the admission gate is closed")
	ErrNotAdmitted   = errors.New("stop: the ticket is not in a state that allows this")
	ErrLate          = errors.New("stop: the gate closed before the start completed; the effect was aborted")
	ErrRegistryWrite = errors.New("stop: the registry write failed; the ticket is unknown")
	ErrContinuation  = errors.New("stop: continuation refused")
)

// TicketInfo is what the registry is told about a ticket.
type TicketInfo struct {
	ID         uint64
	Kind       Kind
	Op         string
	Step       string
	Generation uint64
}

// Registry is the durable run-membership registry as the gate needs it (the store package satisfies it).
type Registry interface {
	RecordStarted(t TicketInfo, handle string) error
	// RecordAborted marks the row aborted; when approvalID is non-empty it also appends the audit row
	// "aborted-after-consume" naming the approval, in the same transaction.
	RecordAborted(t TicketInfo, approvalID string) error
	RecordUnknown(t TicketInfo) error
}

type idSource struct {
	mu sync.Mutex
	n  uint64
}

func (s *idSource) next() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	return s.n
}
