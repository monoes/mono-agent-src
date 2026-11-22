// Package openaicompat is the decision adapter for any server that speaks
// OpenAI's chat-completions wire: OpenAI, Ollama (/v1), llama.cpp, vLLM,
// OpenRouter, Together, Fireworks, LM Studio, Groq. It registers the provider
// kind "openai".
//
// Each question goes in its own request asking for one token. Every option
// is given a one-token label (A, B, … for a choice; 0, 1, … for a score;
// Yes/No for a noul) and the answer's probabilities are read from that
// token's top log-probabilities. A server that gives none is sampled
// instead: five answers at temperature 1, their frequencies the
// probabilities. A choice with more options than one request can carry is
// answered hierarchically: which group, then which member. Generated text
// never reaches a caller (plan D3): it is only matched against the allowed
// labels.
package openaicompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
)

// Kind is the provider kind this package registers.
const Kind = "openai"

const (
	// defaultTopK is how many alternatives a request asks for until a
	// response shows the server returns fewer.
	defaultTopK = 20
	// samples is how many answers a provider without log-probabilities gives
	// per question; minValidSamples of them must name an allowed label.
	samples         = 5
	minValidSamples = 3
	// minCoverage is the least probability mass the first token must put on
	// the allowed labels.
	minCoverage = 0.5
	httpTimeout = 90 * time.Second
)

func init() {
	jevconf.RegisterKind(Kind, func(p jevconf.Provider, key string) (jev.Backend, error) { return New(p, key) })
}

// Prober is a backend that can find out what its provider supports.
type Prober interface {
	Probe(ctx context.Context) (jev.Capabilities, error)
}

// Backend is a jev.Backend for one OpenAI-compatible provider.
type Backend struct {
	base, key, model string
	http             *http.Client
	retries          int
	stored           jev.Capabilities

	mu      sync.Mutex
	sampled bool // the provider gives no log-probabilities
	topK    int  // how many alternatives a response carries
	// completionTokens and noTemperature record a model that refused
	// max_tokens or temperature (complete).
	completionTokens, noTemperature bool
}

var (
	_ jev.Backend     = (*Backend)(nil)
	_ jev.ModelLister = (*Backend)(nil)
	_ Prober          = (*Backend)(nil)
)

// New builds the backend of provider p, sending key ("" = keyless) to
// p.BaseURL only. p.Caps, when a probe stored them, says whether to try
// log-probabilities at all.
func New(p jevconf.Provider, key string) (*Backend, error) {
	base := strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
	if base == "" {
		return nil, errors.New("openaicompat: the provider has no base_url")
	}
	b := &Backend{base: base, key: key, model: p.Model, http: jev.KeyedHTTPClient(httpTimeout), retries: 2,
		stored: p.Caps, topK: defaultTopK}
	switch p.Caps.Probability {
	case jev.ProbSampled, jev.ProbNone:
		b.sampled = true
	}
	if m := p.Caps.MaxChoiceOptions; m > 0 && m+labelSlack < defaultTopK {
		b.topK = m + labelSlack // a probe found fewer alternatives (perRequest)
	}
	return b, nil
}

// Capabilities implements jev.Backend: the stored capabilities, with what
// this backend has learned since. It takes one question per request;
// MaxChoiceOptions is how many options one request carries (it chunks a
// larger choice itself, so callers need not).
func (b *Backend) Capabilities() jev.Capabilities {
	c := b.stored
	c.Kinds = append([]string(nil), c.Kinds...)
	if len(c.Kinds) == 0 {
		c.Kinds = []string{jev.TypeChoice, jev.TypeNoul, jev.TypeScore}
	}
	c.MaxChoiceOptions, c.QuestionsPerRequest, c.Models = b.perRequest(), 1, true
	c.Probability = jev.ProbLogprob
	if b.isSampled() {
		c.Probability = jev.ProbSampled
	}
	return c
}

// Answer implements jev.Backend: one request (or, for a large choice or a
// sampled provider, a few) per question, at most jev.DefaultMaxInFlight
// questions at once.
func (b *Backend) Answer(ctx context.Context, req *jev.Request) (*jev.Response, error) {
	state, err := json.Marshal(req.State)
	if err != nil {
		return nil, fmt.Errorf("openaicompat: encode the state: %w", err)
	}
	model := req.Model
	if model == "" {
		model = b.model
	}
	var sampledAny atomic.Bool
	resp, err := jev.FanOut(ctx, req, 0, func(ctx context.Context, _ string, q jev.Question) (jev.Answer, jev.Usage, error) {
		t := &turn{b: b, model: model, state: string(state), q: q}
		probs, err := t.answer(ctx)
		if t.sampled {
			sampledAny.Store(true)
		}
		if err != nil {
			return jev.Answer{}, t.usage, err
		}
		a, err := jev.AnswerFrom(q, probs)
		return a, t.usage, err
	})
	if resp != nil {
		resp.Model = model
		resp.Probability = jev.ProbLogprob
		if sampledAny.Load() {
			resp.Probability = jev.ProbSampled
		}
	}
	return resp, err
}

func (b *Backend) isSampled() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sampled
}

func (b *Backend) setSampled(v bool) {
	b.mu.Lock()
	b.sampled = v
	b.mu.Unlock()
}

func (b *Backend) k() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.topK
}

// learnK records that a response asked for asked alternatives carried got.
func (b *Backend) learnK(got, asked int) {
	if got <= 0 || got >= asked {
		return
	}
	b.mu.Lock()
	if got < b.topK {
		b.topK = got
	}
	b.mu.Unlock()
}

// labelSlack is how many of a response's alternatives are left for tokens
// that are not labels.
const labelSlack = 5

// perRequest is the most options one request carries: k − labelSlack, at
// least 2 and at most the 26 letters.
func (b *Backend) perRequest() int {
	return max(2, min(26, b.k()-labelSlack))
}
