package stop

import (
	"context"
	"errors"
)

// OutsideRegistry is the pre-registration of a dispatcher that runs outside the host (note 13.1 section 6).
// RegisterAdmitted is an E4 write conditional on the run being running (exact against Stop's transaction).
type OutsideRegistry interface {
	RegisterAdmitted(ctx context.Context, gen uint64, id string) error
	// Ended resolves the row; a refused dispatcher must write it itself, otherwise it blocks quiescence.
	Ended(ctx context.Context, gen uint64, id string) error
}

// ErrOutsideRefused: the run is stopped (or the registration was refused).
var ErrOutsideRefused = errors.New("stop: the run does not admit this dispatcher")

// OutsideDispatch is the E3 path for the CLI, the daemon and workflow runners: register durably first, then
// read the marker, then act. A dispatcher that registered and is then refused writes ended itself. A request
// that was registered may still go out: it is admitted-before-Stop and, being known, quiescence waits for it.
func OutsideDispatch(ctx context.Context, reg OutsideRegistry, dir RunDir, gen uint64, charterID, id string, act func() error) error {
	if err := reg.RegisterAdmitted(ctx, gen, id); err != nil {
		return errors.Join(ErrOutsideRefused, err)
	}
	dir.Hook.at("outside.registered")
	defer func() { _ = reg.Ended(context.WithoutCancel(ctx), gen, id) }()
	if dir.ReadMarker(gen, charterID).Stops() {
		return ErrOutsideRefused
	}
	dir.Hook.at("outside.marker-read")
	dir.Hook.at("outside.act")
	return act()
}
