package stop

import (
	"context"
	"errors"
	"fmt"
)

// Held is a child created inside the containment unit that blocks on a release pipe before running any user
// code (note 13.2 section 6). Killing a held child kills a process that never ran.
type Held interface {
	Handle() string
	Release() error
	Kill() error
}

// ErrPanic wraps a panic recovered by a dispatcher wrapper after BeginStart.
var ErrPanic = errors.New("stop: panic in the dispatcher")

// StartHeld is the E1 dispatcher wrapper: Admit, BeginStart, create the child held, register it with Started,
// and only then release it. A late ticket, a failed registry write, a failed create or a panic after BeginStart
// kills the held child (which never ran) and resolves the ticket through the durable abort path.
func (g *Gate) StartHeld(spec Spec, create func() (Held, error)) (t *Ticket, err error) {
	spec.Kind = KindE1
	t, err = g.Admit(spec)
	if err != nil {
		return nil, err
	}
	if err = g.BeginStart(t); err != nil {
		return t, err
	}
	var held Held
	done := false
	defer func() {
		r := recover()
		if done {
			if r != nil {
				panic(r)
			}
			return
		}
		if held != nil {
			if kerr := held.Kill(); kerr != nil {
				g.MarkUnknown(t)
			} else {
				g.abortKilled(t) // STARTED, but the child was killed (Release failed): not a live started ticket
			}
		}
		g.Abort(t) // STARTING -> ABORTING -> durable abort; a no-op for UNKNOWN or ABORTED
		if r != nil {
			err = fmt.Errorf("%w: %v", ErrPanic, r)
		}
	}()
	g.cfg.Hook.at("start.inside")
	if held, err = create(); err != nil {
		return t, err
	}
	if err = g.Started(t, held.Handle()); err != nil {
		return t, err // deferred: kill the held child, which never ran
	}
	g.cfg.Hook.at("release.before")
	if err = held.Release(); err != nil {
		return t, err
	}
	done = true
	return t, nil
}

// CallRemote is the E3 dispatcher wrapper. The transport is entered only after Started returned nil, and the
// context is checked immediately before the entry; that check is best effort, not atomic with the entry (a
// wrapper that read an uncancelled context and paused is admitted-before-Stop).
func (g *Gate) CallRemote(spec Spec, transport func(ctx context.Context) error) (t *Ticket, err error) {
	spec.Kind = KindE3
	t, err = g.Admit(spec)
	if err != nil {
		return nil, err
	}
	if err = g.BeginStart(t); err != nil {
		return t, err
	}
	defer func() {
		if r := recover(); r != nil {
			g.Abort(t)
			err = fmt.Errorf("%w: %v", ErrPanic, r)
		}
	}()
	if err = g.Started(t, ""); err != nil {
		return t, err // late or unknown: the transport is never entered
	}
	g.cfg.Hook.at("transport.before-ctx-check")
	if cerr := t.ctx.Err(); cerr != nil {
		return t, cerr
	}
	g.cfg.Hook.at("transport.after-ctx-check")
	g.cfg.Hook.at("transport.before-enter")
	return t, transport(t.ctx)
}
