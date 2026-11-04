//go:build !linux

package machine

import (
	"context"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
)

type unsupported struct{}

func (unsupported) Sample(context.Context) (governor.Sample, error) {
	return governor.Sample{}, governor.ErrUnsupported
}

// New returns a sampler that always answers governor.ErrUnsupported (the caller lowers to the safe minimum).
func New(dirs ...string) governor.Sampler { return unsupported{} }
