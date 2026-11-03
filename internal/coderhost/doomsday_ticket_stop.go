package coderhost

import (
	"context"
	"errors"
	"fmt"

	"github.com/monoes/mono-agent/internal/doomsday/stop"
)

// StopGate adapts the stop package's dispatch barrier to DispatchGate. A turn start is an E1 ticket; a spawn or
// a token mint is a database effect handed over like an E3 ticket (admitted, fenced in, registered, then done:
// the write itself carries its own state check, note 13.1 section 7). The ticket's Op carries the call site.
type StopGate struct{ G *stop.Gate }

var barrierErrs = []error{stop.ErrGateClosed, stop.ErrNotAdmitted, stop.ErrLate, stop.ErrRegistryWrite, stop.ErrPanic}

func refusal(err error) error {
	for _, b := range barrierErrs {
		if errors.Is(err, b) {
			return fmt.Errorf("%w: %v", ErrDispatchRefused, err)
		}
	}
	return err
}

// Hand implements DispatchGate.
func (s StopGate) Hand(p DispatchPoint, fn func(ctx context.Context) error) error {
	_, err := s.G.CallRemote(stop.Spec{Op: string(p)}, fn)
	return refusal(err)
}

type heldProc struct{ p StartedProc }

func (h heldProc) Handle() string { return h.p.Handle() }
func (h heldProc) Release() error { return nil } // the launcher starts the process running: held creation is the launcher's (contract 13.2)
func (h heldProc) Kill() error    { return h.p.Kill() }

// Start implements DispatchGate.
func (s StopGate) Start(p DispatchPoint, create func() (StartedProc, error)) error {
	_, err := s.G.StartHeld(stop.Spec{Op: string(p)}, func() (stop.Held, error) {
		pr, err := create()
		if err != nil {
			return nil, err
		}
		return heldProc{pr}, nil
	})
	return refusal(err)
}
