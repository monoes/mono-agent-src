package machine

import (
	"context"
	"sync"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
)

// Step is one scripted answer of a Fake. Hang blocks until the context ends.
type Step struct {
	Sample governor.Sample
	Err    error
	Panic  bool
	Hang   bool
}

// Fake is a scripted sampler for tests of other packages: it answers the steps in order and repeats the last one.
type Fake struct {
	mu    sync.Mutex
	steps []Step
	calls int
}

// NewFake returns a Fake with the script; an empty script answers governor.ErrUnsupported.
func NewFake(steps ...Step) *Fake { return &Fake{steps: steps} }

// Calls is how many times Sample was called.
func (f *Fake) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// Sample implements governor.Sampler.
func (f *Fake) Sample(ctx context.Context) (governor.Sample, error) {
	f.mu.Lock()
	f.calls++
	if len(f.steps) == 0 {
		f.mu.Unlock()
		return governor.Sample{}, governor.ErrUnsupported
	}
	i := f.calls - 1
	if i >= len(f.steps) {
		i = len(f.steps) - 1
	}
	st := f.steps[i]
	f.mu.Unlock()
	if st.Panic {
		panic("fake sampler panic")
	}
	if st.Hang {
		<-ctx.Done()
		return governor.Sample{}, ctx.Err()
	}
	return st.Sample, st.Err
}

// Healthy is a plain good sample for tests.
func Healthy() governor.Sample {
	return governor.Sample{Valid: true, Load1: 0.1, Cores: 8, MemAvailFrac: 0.6, DiskFreeBytes: 200 << 30, DiskFreeFrac: 0.5}
}
