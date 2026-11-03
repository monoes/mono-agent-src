package stop

import (
	"context"
	"sort"
	"sync"
	"time"
)

type gateState int

const (
	gateOpen gateState = iota + 1
	gateClosing
	gateClosed
)

// GateConfig configures a Gate. Reg and Clock are required.
type GateConfig struct {
	Generation uint64
	Reg        Registry
	Clock      Clock
	Hook       Hook
	TClose     time.Duration // default DefaultTClose
	THung      time.Duration // default DefaultTHung
	TMerge     time.Duration // default DefaultTMerge
}

// Spec describes one effect to admit.
type Spec struct {
	Kind Kind
	// Op and Steps are set on the first step of a begun (multi-step) operation, Steps listing every step
	// including the first. Registration happens when that first ticket becomes STARTED.
	Op         string
	Steps      []string
	ApprovalID string // an approval consumed for this effect (note 13.4); audited when the ticket is aborted
}

// Ticket is one admitted effect. All state is guarded by the gate's mutex.
type Ticket struct {
	g        *Gate
	info     TicketInfo
	state    TicketState
	cont     bool
	op       *beginOp
	spec     Spec
	approval string
	ctx      context.Context
	cancel   context.CancelFunc
	since    time.Time // when it became STARTING
}

// ID returns the ticket id.
func (t *Ticket) ID() uint64 { return t.info.ID }

// Context is cancelled by Close for a STARTED E3 ticket.
func (t *Ticket) Context() context.Context { return t.ctx }

// Gate is the host-owned admission and dispatch barrier of one run generation (note 13.1 section 4).
//
// One plain mutex guards the gate state and the ticket table. It is held only for the few instructions of a
// state change; it is never held across a registry write, a start, a request or a hook, and no gate method
// calls another gate method while holding it.
type Gate struct {
	cfg  GateConfig
	ids  idSource
	mu   sync.Mutex
	st   gateState
	tix  map[uint64]*Ticket
	ops  map[string]*beginOp
	ch   chan struct{} // closed and replaced on every transition that can change the live set
	dead time.Time     // Close deadline, fixed by the first Seal
	flag bool          // deadline starters recorded unknown
}

// NewGate returns an open gate.
func NewGate(cfg GateConfig) *Gate {
	if cfg.TClose == 0 {
		cfg.TClose = DefaultTClose
	}
	if cfg.THung == 0 {
		cfg.THung = DefaultTHung
	}
	if cfg.TMerge == 0 {
		cfg.TMerge = DefaultTMerge
	}
	return &Gate{cfg: cfg, st: gateOpen, tix: map[uint64]*Ticket{}, ops: map[string]*beginOp{}, ch: make(chan struct{})}
}

func (g *Gate) notifyLocked() {
	close(g.ch)
	g.ch = make(chan struct{})
}

// Admit creates an ADMITTED ticket, or refuses at once (it never waits behind a closer).
func (g *Gate) Admit(spec Spec) (*Ticket, error) {
	g.cfg.Hook.at("admit.before")
	g.mu.Lock()
	g.expireLocked()
	if g.st != gateOpen {
		g.mu.Unlock()
		return nil, ErrGateClosed
	}
	t := g.newTicketLocked(spec, false, "")
	g.mu.Unlock()
	g.cfg.Hook.at("admit.after")
	return t, nil
}

func (g *Gate) newTicketLocked(spec Spec, cont bool, step string) *Ticket {
	ctx, cancel := context.WithCancel(context.Background())
	t := &Ticket{g: g, state: Admitted, cont: cont, spec: spec, approval: spec.ApprovalID, ctx: ctx, cancel: cancel}
	t.info = TicketInfo{ID: g.ids.next(), Kind: spec.Kind, Op: spec.Op, Step: step, Generation: g.cfg.Generation}
	if len(spec.Steps) > 0 && !cont {
		t.info.Step = spec.Steps[0]
	}
	g.tix[t.info.ID] = t
	return t
}

// BeginStart is the irrevocable fence on future creation: the OS start call, or the hand-over to the
// transport, is allowed only after it returned nil. Exactly one of three rules applies, by the ticket's state.
func (g *Gate) BeginStart(t *Ticket) error {
	g.cfg.Hook.at("beginstart.before")
	g.mu.Lock()
	g.expireLocked()
	var err error
	switch {
	case t.state != Admitted: // rule (c): any other state, including a repeated call, refuses and changes nothing
		err = ErrNotAdmitted
	case t.cont && t.op != nil && t.op.state == opContinuing && g.cfg.Clock.Now().Before(t.op.deadline):
		t.state, t.since = Starting, g.cfg.Clock.Now()
	case !t.cont && g.st == gateOpen:
		t.state, t.since = Starting, g.cfg.Clock.Now()
	default: // rule (b)
		t.state = Fenced
		g.notifyLocked()
		err = ErrGateClosed
	}
	g.mu.Unlock()
	g.cfg.Hook.at("beginstart.after")
	return err
}

// Started writes the registry row (outside the mutex), then makes the ticket STARTED only if the gate is still
// open (or the ticket is a continuation within its bound). Otherwise the ticket is late: it is aborted, the
// caller must kill the held process / not enter the transport, and ErrLate is returned.
func (g *Gate) Started(t *Ticket, handle string) error {
	g.cfg.Hook.at("started.before-registry")
	werr := g.cfg.Reg.RecordStarted(t.info, handle)
	g.cfg.Hook.at("started.after-registry")
	g.mu.Lock()
	g.expireLocked()
	if t.state != Starting {
		g.mu.Unlock()
		return ErrNotAdmitted
	}
	if werr != nil {
		t.state = Unknown
		g.stepFailedLocked(t)
		g.notifyLocked()
		g.mu.Unlock()
		return ErrRegistryWrite
	}
	now := g.cfg.Clock.Now()
	open := g.st == gateOpen || (t.cont && t.op != nil && t.op.state == opContinuing && now.Before(t.op.deadline))
	if open {
		t.state = Started
		g.registerOpLocked(t)
		g.mu.Unlock()
		return nil
	}
	t.state = Aborting
	g.mu.Unlock()
	g.finishAbort(t)
	return ErrLate
}

// finishAbort runs the durable abort transaction outside the mutex and moves ABORTING to ABORTED or UNKNOWN,
// signalling the drain in the same critical section.
func (g *Gate) finishAbort(t *Ticket) {
	g.cfg.Hook.at("abort.before-write")
	err := g.cfg.Reg.RecordAborted(t.info, t.approval)
	g.cfg.Hook.at("abort.after-write")
	g.mu.Lock()
	if err != nil {
		t.state = Unknown
	} else {
		t.state = Aborted
	}
	g.stepFailedLocked(t)
	g.notifyLocked()
	g.mu.Unlock()
}

// MarkUnknown moves a STARTING or ABORTING ticket to UNKNOWN (a starter that could not kill what it created).
func (g *Gate) MarkUnknown(t *Ticket) {
	g.mu.Lock()
	if t.state == Starting || t.state == Aborting || t.state == Started {
		t.state = Unknown
		g.notifyLocked()
	}
	g.mu.Unlock()
}

// Abort is idempotent and acts by the ticket's state: ADMITTED goes straight to ABORTED; STARTING goes through
// ABORTING and the durable abort transaction (never a direct flip); every other state is left unchanged.
func (g *Gate) Abort(t *Ticket) {
	g.mu.Lock()
	switch t.state {
	case Admitted:
		t.state = Aborted
		g.notifyLocked()
		g.mu.Unlock()
	case Starting:
		t.state = Aborting
		g.mu.Unlock()
		g.finishAbort(t)
	default:
		g.mu.Unlock()
	}
}

// RetryUnknown retries the abort write of every UNKNOWN ticket (the schedule is the caller's); a success
// resolves the ticket to ABORTED and so can improve the published outcome.
func (g *Gate) RetryUnknown() {
	g.mu.Lock()
	var us []*Ticket
	for _, t := range g.tix {
		if t.state == Unknown {
			us = append(us, t)
		}
	}
	g.mu.Unlock()
	for _, t := range us {
		if g.cfg.Reg.RecordAborted(t.info, t.approval) == nil {
			g.mu.Lock()
			if t.state == Unknown {
				t.state = Aborted
				g.notifyLocked()
			}
			g.mu.Unlock()
		}
	}
}

// State returns the ticket's current state.
func (g *Gate) State(t *Ticket) TicketState {
	g.mu.Lock()
	defer g.mu.Unlock()
	return t.state
}

// Seal closes the gate to new admissions and cancels the contexts of STARTED E3 tickets; it does not wait.
// It is what the emergency message and the marker observation call: no Admit or BeginStart succeeds after it.
func (g *Gate) Seal() {
	g.cfg.Hook.at("close.set-closing")
	g.mu.Lock()
	if g.st == gateOpen {
		g.st = gateClosing
		g.dead = g.cfg.Clock.Now().Add(g.cfg.TClose)
		g.notifyLocked()
	}
	for _, t := range g.tix {
		if t.state == Started && t.info.Kind == KindE3 {
			t.cancel()
		}
	}
	g.mu.Unlock()
}

// Sealed reports whether the gate has been sealed.
func (g *Gate) Sealed() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.st != gateOpen
}

// computeLocked evaluates the outcome over the live set, never over a frozen snapshot.
func (g *Gate) computeLocked() (o Outcome, waiting bool) {
	for _, t := range g.tix {
		switch t.state {
		case Starting:
			o.Starters = append(o.Starters, t.info.ID)
		case Aborting:
			o.Aborting = append(o.Aborting, t.info.ID)
		case Unknown:
			o.Unknown = append(o.Unknown, t.info.ID)
		}
	}
	for id, op := range g.ops {
		if op.state == opContinuing {
			o.Operations = append(o.Operations, id)
		}
	}
	sortIDs(o.Starters)
	sortIDs(o.Aborting)
	sortIDs(o.Unknown)
	sort.Strings(o.Operations)
	waiting = len(o.Starters)+len(o.Aborting)+len(o.Operations) > 0
	switch {
	case len(o.Starters) > 0:
		o.Kind = ClosedWithStarters
	case len(o.Unknown)+len(o.Aborting) > 0:
		o.Kind = ClosedWithUnknown
	case len(o.Operations) > 0:
		o.Kind = ClosedWithOperation
	default:
		o.Kind = Barrier
	}
	return o, waiting
}

func sortIDs(s []uint64) { sort.Slice(s, func(i, j int) bool { return s[i] < s[j] }) }

// Outcome returns the outcome as of now (the strongest statement, as if the deadline had passed). The gate
// keeps publishing it after Close returned: a later resolution can improve it.
func (g *Gate) Outcome() Outcome {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.expireLocked()
	o, _ := g.computeLocked()
	return o
}

// Close seals the gate and waits for the live set to drain, or for the deadline from the injected clock. It
// returns the outcome computed at that instant. Concurrent and repeated calls share one deadline.
func (g *Gate) Close() Outcome {
	g.Seal()
	for {
		g.mu.Lock()
		g.expireLocked()
		now := g.cfg.Clock.Now()
		o, waiting := g.computeLocked()
		expired := !now.Before(g.dead)
		if !waiting || expired {
			if !waiting && g.st == gateClosing {
				g.st = gateClosed
			}
			var rec []*Ticket
			if expired && !g.flag {
				g.flag = true
				for _, id := range o.Starters {
					rec = append(rec, g.tix[id])
				}
			}
			g.mu.Unlock()
			for _, t := range rec { // each starter is recorded durably as unknown (best effort)
				_ = g.cfg.Reg.RecordUnknown(t.info)
			}
			g.cfg.Hook.at("close.drained")
			return o
		}
		wait, ch := g.dead.Sub(now), g.ch
		if d, ok := g.nextOpDeadlineLocked(now); ok && d < wait {
			wait = d
		}
		g.mu.Unlock()
		select {
		case <-ch:
		case <-g.cfg.Clock.After(wait):
		}
	}
}

// HungStarters lists the tickets that have been STARTING for longer than THung; the host terminates itself
// (crash-only, note 13.1 section 5) while the list is not empty.
func (g *Gate) HungStarters() []uint64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []uint64
	now := g.cfg.Clock.Now()
	for _, t := range g.tix {
		if t.state == Starting && now.Sub(t.since) >= g.cfg.THung {
			out = append(out, t.info.ID)
		}
	}
	sortIDs(out)
	return out
}
