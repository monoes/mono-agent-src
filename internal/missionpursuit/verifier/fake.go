package verifier

import (
	"context"
	"errors"
	"os/exec"
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
	req.Criteria = append([]Criterion(nil), req.Criteria...)
	f.mu.Lock()
	f.calls = append(f.calls, req)
	f.mu.Unlock()
	base := Result{Containment: Containment, ReviewerPin: f.pin, GoalHash: req.GoalHash}
	if r := validate(req); r != ReasonNone {
		base.Outcome, base.Reason = OutcomeCannotCheck, r
		return base, nil
	}
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
	if st.Result.GoalHash == "" {
		st.Result.GoalHash = req.GoalHash
	}
	return st.Result, st.Err
}

// FakeConfiner is a Confiner for tests: it confines nothing, passes or fails its self test as told, and counts what it saw.
type FakeConfiner struct {
	Label   string
	TestErr error
	mu      sync.Mutex
	Tests   int
	Placed  int
	// NilRelease makes Place return a nil release; Clobber makes it drop the command's process attributes.
	NilRelease, Clobber bool
}

var _ Confiner = (*FakeConfiner)(nil)

func (f *FakeConfiner) Name() string {
	if f.Label == "" {
		return "fake"
	}
	return f.Label
}

func (f *FakeConfiner) SelfTest(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Tests++
	return f.TestErr
}

func (f *FakeConfiner) Place(cmd *exec.Cmd) (func(), error) {
	f.mu.Lock()
	f.Placed++
	f.mu.Unlock()
	if f.Clobber {
		cmd.SysProcAttr = nil
	}
	if f.NilRelease {
		return nil, nil
	}
	return func() {}, nil
}

// CanarySpecs are the criteria a Confiner's SelfTest runs through the sandbox: each must END WITH A NON-PASS under a working
// confinement. The first reads a file the caller made outside the work area, the second writes one there; the third reaches a
// unix socket path. A confinement that lets one pass does not hold.
func CanarySpecs(canaryFile, outsideFile, socketPath string) []Criterion {
	return []Criterion{
		{Kind: "command", Spec: "cat " + canaryFile, Expected: "exit:0"},
		{Kind: "command", Spec: "touch " + outsideFile, Expected: "exit:0"},
		{Kind: "command", Spec: "test -S " + socketPath, Expected: "exit:0"},
	}
}
