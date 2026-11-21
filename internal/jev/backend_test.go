package jev

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// scripted is a Backend whose answer is a function of the request.
type scripted struct {
	caps   Capabilities
	answer func(req *Request) (*Response, error)
	calls  int
}

func (s *scripted) Answer(_ context.Context, req *Request) (*Response, error) {
	s.calls++
	return s.answer(req)
}

func (s *scripted) Capabilities() Capabilities { return s.caps }

type listing struct{ scripted }

func (l *listing) Models(context.Context) ([]Model, error) { return []Model{{ID: "m1"}}, nil }

type outcome struct {
	resp *Response
	err  error
	n    int
}

func recordInto(c *Client) *outcome {
	o := &outcome{}
	c.OnResult = func(_ *Request, resp *Response, _ time.Duration, err error) { o.resp, o.err, o.n = resp, err, o.n+1 }
	return o
}

func TestABackendAnswersInPlaceOfTypeSafe(t *testing.T) {
	b := &scripted{caps: Capabilities{Probability: ProbLogprob}, answer: func(req *Request) (*Response, error) {
		if req.Model != "m" {
			t.Errorf("model %q reached the backend", req.Model)
		}
		return &Response{Answers: map[string]Answer{
			"q": {Type: TypeChoice, Choice: "a", Probabilities: map[string]float64{"a": 0.8, "b": 0.2}, Confidence: 0.6},
		}, Usage: Usage{InputTokens: 7, OutputTokens: 1}}, nil
	}}
	c := NewBackendClient("local", "m", b)
	o := recordInto(c)
	resp, err := c.Ask(context.Background(), map[string]any{}, map[string]Question{"q": choiceQ("a", "b")})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "local" || resp.Probability != ProbLogprob || resp.Model != "m" || resp.Answers["q"].Choice != "a" {
		t.Fatalf("resp = %+v", resp)
	}
	if o.n != 1 || o.err != nil || o.resp == nil || o.resp.Usage.InputTokens != 7 {
		t.Fatalf("OnResult = %+v", o)
	}
}

// Ask repairs and validates a backend's answers as it does TypeSafe's, and
// OnResult still sees the rejected response.
func TestABackendsAnswersAreRepairedAndValidated(t *testing.T) {
	shared := map[string]Answer{
		"q": {Type: TypeChoice, Choice: "b", Probabilities: map[string]float64{"a": 0.7, "b": 0.3}},
	}
	b := &scripted{answer: func(*Request) (*Response, error) { return &Response{Answers: shared}, nil }}
	c := NewBackendClient("x", "", b)
	resp, err := c.Ask(context.Background(), nil, map[string]Question{"q": choiceQ("a", "b")})
	if err != nil || resp.Answers["q"].Choice != "a" {
		t.Fatalf("not repaired: %+v %v", resp, err)
	}
	if shared["q"].Choice != "b" {
		t.Fatal("Ask wrote into the backend's own answers map")
	}

	b.answer = func(*Request) (*Response, error) {
		return &Response{Answers: map[string]Answer{"q": {Type: TypeChoice, Choice: "z", Probabilities: map[string]float64{"z": 1}}}, Usage: Usage{InputTokens: 3}}, nil
	}
	o := recordInto(c)
	if _, err := c.Ask(context.Background(), nil, map[string]Question{"q": choiceQ("a", "b")}); !errors.Is(err, ErrInvalidAnswer) {
		t.Fatalf("an out-of-set answer passed: %v", err)
	}
	if o.n != 1 || o.err == nil || o.resp == nil || o.resp.Usage.InputTokens != 3 {
		t.Fatalf("OnResult after a rejected answer = %+v", o)
	}

	b.answer = func(*Request) (*Response, error) { return &Response{Answers: map[string]Answer{}}, nil }
	if _, err := c.Ask(context.Background(), nil, map[string]Question{"q": choiceQ("a", "b")}); !errors.Is(err, ErrInvalidAnswer) {
		t.Fatalf("a missing answer passed: %v", err)
	}
}

// A backend's failure reaches the caller and OnResult, with the usage of a
// partial response when the backend returned one.
func TestABackendFailureIsRecorded(t *testing.T) {
	boom := errors.New("boom")
	b := &scripted{answer: func(*Request) (*Response, error) {
		return &Response{Usage: Usage{InputTokens: 5}}, boom
	}}
	c := NewBackendClient("x", "", b)
	o := recordInto(c)
	if _, err := c.Ask(context.Background(), nil, map[string]Question{"q": {Type: TypeNoul}}); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if o.n != 1 || !errors.Is(o.err, boom) || o.resp == nil || o.resp.Usage.InputTokens != 5 {
		t.Fatalf("OnResult = %+v", o)
	}
	b.answer = func(*Request) (*Response, error) { return nil, nil }
	if _, err := c.Ask(context.Background(), nil, map[string]Question{"q": {Type: TypeNoul}}); !errors.Is(err, ErrInvalidAnswer) {
		t.Fatalf("no response passed: %v", err)
	}
}

// A question type the backend does not answer fails before any call.
func TestAnUnsupportedKindFailsBeforeTheCall(t *testing.T) {
	b := &scripted{caps: Capabilities{Kinds: []string{TypeChoice, TypeNoul}}}
	c := NewBackendClient("hf", "", b)
	o := recordInto(c)
	_, err := c.Ask(context.Background(), nil, map[string]Question{"s": {Type: TypeScore, Criteria: []string{"lo", "hi"}}})
	if !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), `"hf"`) || b.calls != 0 {
		t.Fatalf("err = %v, calls = %d", err, b.calls)
	}
	if o.n != 1 || !errors.Is(o.err, ErrUnsupported) {
		t.Fatalf("OnResult = %+v", o)
	}
}

func TestModelsDelegatesToABackendThatListsThem(t *testing.T) {
	ms, err := NewBackendClient("x", "", &listing{}).Models(context.Background())
	if err != nil || len(ms) != 1 || ms[0].ID != "m1" {
		t.Fatalf("models = %v %v", ms, err)
	}
	if _, err := NewBackendClient("x", "", &scripted{}).Models(context.Background()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("a backend that lists nothing: %v", err)
	}
}

// The TypeSafe wire names its provider and probability kind itself; a server
// field cannot set them.
func TestTheTypeSafeClientNamesItsProvider(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"model":"jev-x","provider":"evil","probability":"logprob","answers":{"q":{"type":"noul","noul":0.4}},"usage":{"input_tokens":2}}`))
	})
	if c.Provider != ProviderTypeSafe || c.Backend != nil {
		t.Fatalf("client = %+v", c)
	}
	resp, err := c.Ask(context.Background(), nil, map[string]Question{"q": {Type: TypeNoul}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != ProviderTypeSafe || resp.Probability != ProbNative || resp.Usage.CostUSD != nil {
		t.Fatalf("resp = %+v", resp)
	}
}
