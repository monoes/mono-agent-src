package jev

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/monoes/mono-agent/internal/credhttp"
)

// ProviderTypeSafe names the built-in provider: TypeSafe's /v1/systemone,
// what a Client with no Backend talks to.
const ProviderTypeSafe = "typesafe"

// ProbabilityKind says how an answer's probabilities were obtained (plan D7).
type ProbabilityKind string

const (
	// ProbNative: the provider returns calibrated probabilities itself (TypeSafe).
	ProbNative ProbabilityKind = "native"
	// ProbLogprob: read from the provider's token log-probabilities.
	ProbLogprob ProbabilityKind = "logprob"
	// ProbSampled: the share of k repeated answers.
	ProbSampled ProbabilityKind = "sampled"
	// ProbNone: the provider gives no probability; gate as 0.
	ProbNone ProbabilityKind = "none"
)

// Capabilities is what a Backend can do, as probed or declared.
type Capabilities struct {
	// Kinds lists the question types it answers (TypeChoice, TypeNoul,
	// TypeScore). Empty means unknown: Ask does not pre-check.
	Kinds       []string        `json:"kinds"`
	Probability ProbabilityKind `json:"probability"`
	// MaxChoiceOptions is the most options a choice question may have: more
	// is ErrUnsupported. 0 = unbounded. It is a hard cap, never how an
	// adapter splits a question (OptionsPerRequest).
	MaxChoiceOptions int `json:"max_choice_options"`
	// OptionsPerRequest, when set, is how many options one request to the
	// provider carries; the adapter splits a larger choice itself, so callers
	// need not. It is what a probe found, kept with the provider's record.
	OptionsPerRequest int `json:"options_per_request,omitempty"`
	// QuestionsPerRequest is how many questions one request can carry; 0 = unbounded.
	QuestionsPerRequest int `json:"questions_per_request"`
	// Models reports whether the backend lists models (ModelLister).
	Models bool `json:"models"`
}

// Supports reports whether the capabilities admit question type kind (an
// empty Kinds admits everything).
func (c Capabilities) Supports(kind string) bool {
	if len(c.Kinds) == 0 {
		return true
	}
	for _, k := range c.Kinds {
		if k == kind {
			return true
		}
	}
	return false
}

// Backend answers a Request in place of TypeSafe's wire protocol. Client.Ask
// still repairs and validates every answer, records the latency and calls
// OnResult, so a backend only has to produce answers of the right shape.
type Backend interface {
	// Answer returns one Answer per question of req. It must only ever name
	// options present in the question and never return provider text. It may
	// return a non-nil Response together with an error so that the usage of
	// a failed, billed call is still recorded.
	Answer(ctx context.Context, req *Request) (*Response, error)
	Capabilities() Capabilities
}

// ModelLister is implemented by a Backend that can list its models.
type ModelLister interface {
	Models(ctx context.Context) ([]Model, error)
}

// ErrUnsupported is wrapped when a provider cannot answer a question type or
// cannot do what was asked (list models).
var ErrUnsupported = errors.New("jev: not supported by this provider")

// NewBackendClient builds a client that answers through b instead of
// TypeSafe. It needs no API key: the backend holds whatever credential it
// was built with. Only jevconf may call it (source_pin_test.go), so a
// backend's key goes only where its provider record says.
func NewBackendClient(provider, model string, b Backend) *Client {
	return &Client{Provider: provider, Model: model, Backend: b, Retries: 2}
}

type retriesKey struct{}

// WithRetries returns ctx carrying a retry budget: how many times a backend
// may retry one call that failed with a 408, 429 or 5xx (n ≤ 0 = never,
// one attempt). Client.Ask and Client.Models pass Client.Retries this way,
// so a backend honours what its caller set.
func WithRetries(ctx context.Context, n int) context.Context {
	return context.WithValue(ctx, retriesKey{}, max(0, n))
}

// RetriesFrom is the retry budget ctx carries (WithRetries); ok is false
// when it carries none, and a backend then uses its own default.
func RetriesFrom(ctx context.Context) (n int, ok bool) {
	n, ok = ctx.Value(retriesKey{}).(int)
	return n, ok
}

// KeyedHTTPClient is the HTTP client a backend sending a key uses: like
// TypeSafe's, it reads no proxy settings and drops credentials on a redirect
// to another host (credhttp).
func KeyedHTTPClient(timeout time.Duration) *http.Client {
	return credhttp.Client(&http.Client{Timeout: timeout, Transport: keyedTransport()})
}

// askBackend is Ask's body for a client with a Backend: the backend's answers
// in place of post and decode. It returns what OnResult should record.
func (c *Client) askBackend(ctx context.Context, req *Request, started time.Time) (recorded, resp *Response, err error) {
	caps := c.Backend.Capabilities()
	for id, q := range req.Questions {
		if !caps.Supports(q.Type) {
			return nil, nil, fmt.Errorf("%s: %w: provider %q does not answer %q questions", id, ErrUnsupported, c.Provider, q.Type)
		}
	}
	got, err := c.Backend.Answer(WithRetries(ctx, c.Retries), req)
	if got != nil {
		// A copy, so repairing answers never writes into the backend's map.
		cp := *got
		cp.Answers = make(map[string]Answer, len(got.Answers))
		for id, a := range got.Answers {
			cp.Answers[id] = a
		}
		if cp.Model == "" {
			cp.Model = c.Model
		}
		if cp.Provider == "" {
			cp.Provider = c.Provider
		}
		if cp.Probability == "" {
			cp.Probability = caps.Probability
		}
		cp.LatencyMS = time.Since(started).Milliseconds()
		recorded = &cp
	}
	if err != nil {
		return recorded, nil, err
	}
	if recorded == nil {
		return nil, nil, fmt.Errorf("%w: provider %q returned no response", ErrInvalidAnswer, c.Provider)
	}
	return recorded, recorded, nil
}

// backendModels is Models for a client with a Backend.
func (c *Client) backendModels(ctx context.Context) ([]Model, error) {
	if l, ok := c.Backend.(ModelLister); ok {
		return l.Models(WithRetries(ctx, c.Retries))
	}
	return nil, fmt.Errorf("%w: provider %q cannot list models", ErrUnsupported, c.Provider)
}
