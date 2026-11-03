package stop

import (
	"fmt"
	"time"
)

type opState int

const (
	opContinuing opState = iota + 1
	opFinished
	opIncomplete
)

// beginOp is a begun multi-step operation (a merge, Q11): once its first step is STARTED it may finish its
// remaining steps after Stop, within T_merge, and only through continuation tickets.
type beginOp struct {
	id       string
	state    opState
	steps    []string
	deadline time.Time
	tickets  []*Ticket // tickets[i] is the ticket of steps[i]
	progress int       // number of steps whose work ended successfully
}

// registerOpLocked registers the begun operation when the ticket of its first step becomes STARTED, in the
// same critical section, so registration happens before any Close can observe the gate closing.
func (g *Gate) registerOpLocked(t *Ticket) {
	if t.cont || t.spec.Op == "" || len(t.spec.Steps) == 0 {
		return
	}
	if _, dup := g.ops[t.spec.Op]; dup {
		return
	}
	op := &beginOp{id: t.spec.Op, state: opContinuing, steps: append([]string(nil), t.spec.Steps...),
		deadline: g.cfg.Clock.Now().Add(g.cfg.TMerge), tickets: []*Ticket{t}}
	t.op = op
	g.ops[op.id] = op
}

// stepRunningLocked reports whether the operation's latest step has been started and its work has not ended: a
// git command is running right now (note 13.3 R2).
func stepRunningLocked(op *beginOp) bool {
	if len(op.tickets) == 0 || op.progress >= len(op.tickets) {
		return false
	}
	last := op.tickets[len(op.tickets)-1]
	return last.state == Starting || last.state == Started
}

// effectiveDeadlineLocked is T_merge, extended to the hard bound T_merge+TMergeHard while a step is running.
func (g *Gate) effectiveDeadlineLocked(op *beginOp, now time.Time) time.Time {
	if !now.Before(op.deadline) && stepRunningLocked(op) {
		return op.deadline.Add(g.cfg.TMergeHard)
	}
	return op.deadline
}

// expireLocked applies T_merge: an operation past its bound becomes INCOMPLETE and its remaining steps are
// withdrawn. A step that is running at the bound keeps its operation CONTINUING (so Stop's deferral of the
// containment kill still covers the command) until the step ends or the hard bound passes; no new step is
// admitted in that time (AdmitContinuation, BeginStart). It is called under the mutex by every method, so no
// read of the bound happens outside it.
func (g *Gate) expireLocked() {
	now := g.cfg.Clock.Now()
	for _, op := range g.ops {
		if op.state == opContinuing && !now.Before(g.effectiveDeadlineLocked(op, now)) {
			g.terminalLocked(op, opIncomplete)
		}
	}
}

// terminalLocked leaves CONTINUING for any reason. Every continuation ticket still ADMITTED is moved to
// FENCED (withdrawn) in the same critical section; tickets already STARTING or later resolve by ordinary rules.
func (g *Gate) terminalLocked(op *beginOp, to opState) {
	op.state = to
	for _, t := range op.tickets {
		if t.cont && t.state == Admitted {
			t.state = Fenced
		}
	}
	g.notifyLocked()
}

// stepFailedLocked: a step whose ticket was ABORTED or UNKNOWN leaves its operation INCOMPLETE.
func (g *Gate) stepFailedLocked(t *Ticket) {
	if t.op != nil && t.op.state == opContinuing {
		g.terminalLocked(t.op, opIncomplete)
	}
}

func (g *Gate) nextOpDeadlineLocked(now time.Time) (time.Duration, bool) {
	var best time.Duration
	found := false
	for _, op := range g.ops {
		if op.state != opContinuing {
			continue
		}
		if d := g.effectiveDeadlineLocked(op, now).Sub(now); !found || d < best {
			best, found = d, true
		}
	}
	return best, found
}

// AdmitContinuation admits the next registered step of a CONTINUING operation; it works while the gate is
// CLOSING or CLOSED. It refuses a new operation, a step out of order, outside the list, or after T_merge, and a
// step whose predecessor's work has not ended successfully. A repeated call returns the same ticket.
func (g *Gate) AdmitContinuation(opID, step string) (*Ticket, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.expireLocked()
	op := g.ops[opID]
	if op == nil || op.state != opContinuing {
		return nil, fmt.Errorf("%w: operation %q is not continuing", ErrContinuation, opID)
	}
	n := len(op.tickets)
	if n >= 2 && op.steps[n-1] == step {
		return op.tickets[n-1], nil
	}
	if !g.cfg.Clock.Now().Before(op.deadline) {
		return nil, fmt.Errorf("%w: T_merge has passed, no new step is admitted", ErrContinuation)
	}
	if n >= len(op.steps) || op.steps[n] != step {
		return nil, fmt.Errorf("%w: %q is not the next registered step", ErrContinuation, step)
	}
	if op.progress != n {
		return nil, fmt.Errorf("%w: the previous step has not ended", ErrContinuation)
	}
	t := g.newTicketLocked(Spec{Kind: KindE1, Op: opID, ApprovalID: ""}, true, step)
	t.op = op
	op.tickets = append(op.tickets, t)
	return t, nil
}

// EndWork reports that the work of the ticket's step ended. A failing step makes the operation INCOMPLETE; the
// last step ending successfully makes it FINISHED. It is a no-op for a ticket that belongs to no operation.
func (g *Gate) EndWork(t *Ticket, ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	op := t.op
	if op == nil || op.state != opContinuing {
		return
	}
	switch {
	case !ok:
		g.terminalLocked(op, opIncomplete)
	default:
		op.progress++
		switch {
		case op.progress >= len(op.steps):
			g.terminalLocked(op, opFinished)
		case !g.cfg.Clock.Now().Before(op.deadline): // the running step ended past T_merge: nothing further runs
			g.terminalLocked(op, opIncomplete)
		}
	}
}

// ContinuingTickets lists the tickets of CONTINUING operations; Stop excludes their containment sub-units
// from the kill (Q11: a begun merge finishes, an unbegun one does not start).
func (g *Gate) ContinuingTickets() []uint64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.expireLocked()
	var out []uint64
	for _, op := range g.ops {
		if op.state != opContinuing {
			continue
		}
		for _, t := range op.tickets {
			out = append(out, t.info.ID)
		}
	}
	sortIDs(out)
	return out
}
