package coderhost

import (
	"context"
	"errors"
	"fmt"
)

// Dispatch-barrier tickets at the host's call sites (note 13.1, section 8). With an open-ended run active the
// host gives every dispatch a ticket before it happens: a session spawn, a turn start and a token mint. The
// mode is off by default: with no gate installed (SetDispatchGate, WrapLauncher) every function here is a plain
// call and nothing about the host changes.
//
// Token REVOCATION is deliberately not gated: it is a step of Stop itself and must work after the gate has
// closed. Enqueueing a command is not gated either: a stop, pause or cancel must still be queued; what a queued
// send does is gated where the host starts its turn.

// DispatchPoint names a call site that takes a ticket.
type DispatchPoint string

const (
	PointSessionSpawn DispatchPoint = "session.spawn"
	PointTurnStart    DispatchPoint = "turn.start"
	PointTokenMint    DispatchPoint = "token.mint"
)

// ErrDispatchRefused: the dispatch barrier refused the dispatch (the run is stopping, or the ticket was late).
// Nothing was dispatched.
var ErrDispatchRefused = errors.New("coder host: the run is stopping; nothing new is dispatched")

// StartedProc is a process a Start made. Kill ends it (a late ticket kills what the gate refused).
type StartedProc interface {
	Handle() string
	Kill() error
}

// DispatchGate is the narrow view of the dispatch barrier the host's call sites need. The stop package's
// Gate satisfies it through StopGate.
type DispatchGate interface {
	// Hand runs fn only after the ticket for p was admitted, fenced in and registered; a closed gate returns an
	// error wrapping ErrDispatchRefused and fn is not called. fn's own error is returned unchanged.
	Hand(p DispatchPoint, fn func(ctx context.Context) error) error
	// Start is Hand for a process start: create makes the process and it is registered; a late ticket kills it
	// (Kill) and returns an error wrapping ErrDispatchRefused.
	Start(p DispatchPoint, create func() (StartedProc, error)) error
}

// SetDispatchGate installs (or with nil removes) the gate of the store's own dispatches (spawn, token mint).
func (s *Store) SetDispatchGate(g DispatchGate) { s.dispatch = g }

// gated runs fn under a ticket of p when a gate is installed, and plainly otherwise.
func (s *Store) gated(p DispatchPoint, fn func() error) error {
	g := s.dispatch
	if g == nil {
		return fn()
	}
	return g.Hand(p, func(context.Context) error { return fn() })
}

// gatedLauncher gives every Start a ticket. Adopt and Finish end or find what exists and are never gated.
type gatedLauncher struct {
	Launcher
	gate DispatchGate
}

type gatedBudgetLauncher struct{ gatedLauncher }

// WrapLauncher returns l with its turn starts under tickets of g. With a nil gate it returns l itself, so a
// host with no open-ended run is what it was. A BudgetLauncher stays one.
func WrapLauncher(l Launcher, g DispatchGate) Launcher {
	if g == nil || l == nil {
		return l
	}
	w := gatedLauncher{Launcher: l, gate: g}
	if _, ok := l.(BudgetLauncher); ok {
		return gatedBudgetLauncher{w}
	}
	return w
}

type turnProc struct{ t Turn }

func (p turnProc) Handle() string { return fmt.Sprintf("%d:%s", p.t.PID(), p.t.Stamp()) }
func (p turnProc) Kill() error    { p.t.Stop(); return nil }

func (w gatedLauncher) start(mk func() (Turn, error)) (Turn, error) {
	var t Turn
	err := w.gate.Start(PointTurnStart, func() (StartedProc, error) {
		var e error
		if t, e = mk(); e != nil {
			return nil, e
		}
		return turnProc{t}, nil
	})
	if err != nil {
		return nil, err
	}
	return t, nil
}

func (w gatedLauncher) Start(profileID, conversationID, turnID, message string, attachments []string, missionToken string) (Turn, error) {
	return w.start(func() (Turn, error) {
		return w.Launcher.Start(profileID, conversationID, turnID, message, attachments, missionToken)
	})
}

func (w gatedBudgetLauncher) StartCapped(profileID, conversationID, turnID, message string, attachments []string, missionToken string, capUSD float64) (Turn, error) {
	return w.start(func() (Turn, error) {
		return w.Launcher.(BudgetLauncher).StartCapped(profileID, conversationID, turnID, message, attachments, missionToken, capUSD)
	})
}
