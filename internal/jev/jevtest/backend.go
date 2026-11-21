package jevtest

import (
	"context"
	"fmt"
	"sync"

	"github.com/monoes/mono-agent/internal/jev"
)

// FakeBackend is an in-process jev.Backend for tests of code that runs on a
// non-TypeSafe provider. It records every request and answers through the
// same Handler values as Server (see the package doc), so every answer is
// valid unless Respond deliberately breaks it.
//
//	fb := jevtest.NewFakeBackend(jevtest.Fixed(map[string]string{"route": "billing"}))
//	t.Cleanup(jevconf.RegisterKind("fake", func(jevconf.Provider, string) (jev.Backend, error) { return fb, nil }))
//	jevconf.SaveProvider(ctx, db, jevconf.Provider{Name: "local", Kind: "fake", BaseURL: "http://localhost"})
//	jevconf.SetDefaultProvider(ctx, db, "local") // or SetSurfaceProvider, or jevconf.NewClientFor(…, "local", …)
//
// A kind name is [a-z0-9-]{1,32} and registered once at a time, so tests of
// one package that register the same kind must not run in parallel.
type FakeBackend struct {
	// Caps is what Capabilities reports. NewFakeBackend sets every question
	// type and jev.ProbLogprob.
	Caps jev.Capabilities
	// ModelList is what Models returns; nil makes Models fail with jev.ErrUnsupported.
	ModelList []jev.Model

	mu       sync.Mutex
	handler  Handler
	respond  func(req *jev.Request) (*jev.Response, error)
	err      error
	requests []jev.Request
}

// NewFakeBackend returns a fake answering through h (nil = defaults).
func NewFakeBackend(h Handler) *FakeBackend {
	return &FakeBackend{
		Caps:    jev.Capabilities{Kinds: []string{jev.TypeChoice, jev.TypeNoul, jev.TypeScore}, Probability: jev.ProbLogprob},
		handler: h,
	}
}

// SetHandler swaps the handler for following calls.
func (f *FakeBackend) SetHandler(h Handler) {
	f.mu.Lock()
	f.handler = h
	f.mu.Unlock()
}

// SetRespond makes following calls return whatever respond returns, in place
// of the handler's valid answers (nil restores the handler).
func (f *FakeBackend) SetRespond(respond func(req *jev.Request) (*jev.Response, error)) {
	f.mu.Lock()
	f.respond = respond
	f.mu.Unlock()
}

// SetErr makes following calls fail with err (nil restores answers).
func (f *FakeBackend) SetErr(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

// Capabilities implements jev.Backend.
func (f *FakeBackend) Capabilities() jev.Capabilities { return f.Caps }

// Answer implements jev.Backend: it records req, then answers each question
// as the handler says, with 100 input and 1 output token of usage.
func (f *FakeBackend) Answer(ctx context.Context, req *jev.Request) (*jev.Response, error) {
	f.mu.Lock()
	f.requests = append(f.requests, *req)
	h, respond, err := f.handler, f.respond, f.err
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if respond != nil {
		return respond(req)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var want map[string]string
	if h != nil {
		want = h(*req)
	}
	resp := &jev.Response{Model: req.Model, Answers: map[string]jev.Answer{}, Usage: jev.Usage{InputTokens: 100, OutputTokens: 1}}
	for id, q := range req.Questions {
		a, err := Answer(q, want[id])
		if err != nil {
			return resp, fmt.Errorf("jevtest: question %q: %w", id, err)
		}
		resp.Answers[id] = a
	}
	return resp, nil
}

// Models implements jev.ModelLister.
func (f *FakeBackend) Models(context.Context) ([]jev.Model, error) {
	if f.ModelList == nil {
		return nil, fmt.Errorf("%w: the fake lists no models", jev.ErrUnsupported)
	}
	return append([]jev.Model(nil), f.ModelList...), nil
}

// Requests returns a copy of every request received so far.
func (f *FakeBackend) Requests() []jev.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]jev.Request(nil), f.requests...)
}

// Calls is len(Requests()).
func (f *FakeBackend) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}
