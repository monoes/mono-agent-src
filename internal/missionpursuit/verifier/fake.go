package verifier

import (
	"context"
	"errors"
	"sync"
)

// FakeStep is one scripted answer of a Fake.
type FakeStep struct {
	Result Result
	Err    error
	// UntilCancelled makes the call wait for its context and then answer abandoned (a check in progress that is cancelled).
	UntilCancelled bool
}

// Fake is a scripted Verifier for the host's tests: it answers its steps in order and records every request. Like the real
// one it refuses a pin that is not independent of the judge and the lead, and abandons when its context is already done.
type Fake struct {
	pin   string
	mu    sync.Mutex
	steps []FakeStep
	calls []Request
}

var _ Verifier = (*Fake)(nil)

// NewFake builds a fake whose checker pin is pin.
func NewFake(pin string, steps ...FakeStep) *Fake { return &Fake{pin: pin, steps: steps} }

func (f *Fake) Pin() string { return f.pin }

// Calls are the requests seen, in order.
func (f *Fake) Calls() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request(nil), f.calls...)
}

func (f *Fake) Run(ctx context.Context, req Request) (Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	f.mu.Unlock()
	base := Result{Containment: Containment, ReviewerPin: f.pin}
	if ok, _ := Independent(req.JudgePin, f.pin, req.LeadPin); !ok {
		base.Outcome, base.Reason = OutcomeCannotCheck, ReasonPinNotIndependent
		return base, nil
	}
	if ctx.Err() != nil {
		base.Outcome, base.Reason = OutcomeAbandoned, ReasonCancelled
		return base, nil
	}
	f.mu.Lock()
	if len(f.steps) == 0 {
		f.mu.Unlock()
		return Result{}, errors.New("verifier: the fake's script is exhausted")
	}
	st := f.steps[0]
	f.steps = f.steps[1:]
	f.mu.Unlock()
	if st.UntilCancelled {
		<-ctx.Done()
		base.Outcome, base.Reason = OutcomeAbandoned, ReasonCancelled
		return base, nil
	}
	return st.Result, st.Err
}
