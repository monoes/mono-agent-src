package judge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
	"unicode/utf8"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

// Typed refusals. ErrBadJudgment is the types package's: the caller records a bad-judgment epoch and changes nothing
// else. ErrModelFailed (timeout, transport, cancelled) carries no model text; the caller may record it the same way.
var (
	ErrBadJudgment    = types.ErrBadJudgment
	ErrModelFailed    = errors.New("judge: model call failed")
	ErrModelPanicked  = errors.New("judge: the model adapter panicked")
	ErrBadInput       = errors.New("judge: bad input")
	ErrNoPin          = errors.New("judge: model pin not recorded")
	ErrNotIndependent = errors.New("judge: model pin refused by the independence rule")
	ErrBadConfig      = errors.New("judge: bad configuration")
	maxPinLen         = types.MaxIDLen
	defaultTimeout    = 2 * time.Minute
	maxTimeout        = 10 * time.Minute
)

// Model is supplied by the caller, already resolved for the judging purpose. This package never builds one.
// Pin names the exact provider and model version in use; it is recorded with the result. Complete must honour ctx and
// should return no more than req.MaxBytes: it must bound what it reads (ReadReply, or a cancel once a stream passes the
// cap), because Run can only check the size of a reply that is already in memory.
type Model interface {
	Pin() string
	Complete(ctx context.Context, req Request) ([]byte, error)
}

// ErrReplyTooLong: the model's reply went past the cap before it was read to its end.
var ErrReplyTooLong = fmt.Errorf("%w: the reply is over the size cap", types.ErrBadJudgment)

// ReadReply reads at most the cap from a model's reply and refuses a longer one, WITHOUT reading the rest: the bound is
// on the read, not on a copy that already exists. A Model that has its reply as a stream returns ReadReply(stream).
func ReadReply(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, types.MaxJudgmentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > types.MaxJudgmentBytes {
		return nil, ErrReplyTooLong
	}
	return b, nil
}

// Config builds a Judge. Refuse is required: it reports whether a pin may not judge (for example because it equals the
// finish verifier's pin, or the author of the evidence). A caller that wants no restriction passes a func returning
// false, which makes that choice visible.
type Config struct {
	Model   Model
	Refuse  func(pin string) bool
	Timeout time.Duration // 0 means two minutes; above ten minutes is refused
}

// Judge runs judging rounds. It holds no state between runs.
type Judge struct {
	model   Model
	refuse  func(string) bool
	timeout time.Duration
}

// Result is a fully validated judgment with the raw body the model returned (what the store keeps) and its hash.
type Result struct {
	Judgment Judgment
	Body     []byte
	BodyHash [32]byte
	ModelPin string
}

// Judgment is re-exported for callers of this package.
type Judgment = types.Judgment

// New validates the configuration.
func New(c Config) (*Judge, error) {
	if c.Model == nil || c.Refuse == nil {
		return nil, fmt.Errorf("%w: model and independence predicate are required", ErrBadConfig)
	}
	t := c.Timeout
	if t == 0 {
		t = defaultTimeout
	}
	if t < 0 || t > maxTimeout {
		return nil, fmt.Errorf("%w: timeout", ErrBadConfig)
	}
	return &Judge{model: c.Model, refuse: c.Refuse, timeout: t}, nil
}

// CheckPin applies the pin and independence rules without calling the model.
func (j *Judge) CheckPin() (string, error) {
	pin := j.model.Pin()
	if pin == "" || len(pin) > maxPinLen || !utf8.ValidString(pin) {
		return "", ErrNoPin
	}
	if j.refuse(pin) {
		return "", ErrNotIndependent
	}
	return pin, nil
}

type reply struct {
	body []byte
	err  error
}

// Run scores one round. Order: pin and independence, prompt, one bounded model call, size cap, strict decode, check
// against the ids the host expects. Nothing is trusted until every step passed; any failure returns a typed error and
// no partial result.
func (j *Judge) Run(ctx context.Context, in Input) (Result, error) {
	pin, err := j.CheckPin()
	if err != nil {
		return Result{}, err
	}
	req, err := BuildPrompt(in)
	if err != nil {
		return Result{}, err
	}
	body, err := j.call(ctx, req)
	if err != nil {
		return Result{}, err
	}
	if len(body) > types.MaxJudgmentBytes {
		return Result{}, fmt.Errorf("%w: body size %d", ErrBadJudgment, len(body))
	}
	jd, err := types.DecodeJudgment(body)
	if err != nil {
		return Result{}, err
	}
	if err := jd.Validate(in.Expect()); err != nil {
		return Result{}, err
	}
	kept := append([]byte(nil), body...)
	return Result{Judgment: jd, Body: kept, BodyHash: types.BodyHash(kept), ModelPin: pin}, nil
}

// call runs the model under the timeout. The goroutine ends when the Model returns; the buffered channel means it
// never blocks on a caller that already gave up. A Model that ignores ctx is the caller's bug, but Run still returns.
func (j *Judge) call(ctx context.Context, req Request) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, j.timeout)
	defer cancel()
	ch := make(chan reply, 1)
	go func() {
		// A panic in the model adapter must not take the process down: it is a failed call, with no model text.
		defer func() {
			if recover() != nil {
				ch <- reply{nil, ErrModelPanicked}
			}
		}()
		b, err := j.model.Complete(cctx, req)
		ch <- reply{b, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, fmt.Errorf("%w: %v", ErrModelFailed, cctx.Err())
		}
		if cctx.Err() != nil {
			return nil, fmt.Errorf("%w: %v", ErrModelFailed, cctx.Err())
		}
		return r.body, nil
	case <-cctx.Done():
		return nil, fmt.Errorf("%w: %v", ErrModelFailed, cctx.Err())
	}
}
