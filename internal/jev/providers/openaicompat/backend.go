// Package openaicompat is the decision adapter for any server that speaks
// OpenAI's chat-completions wire: OpenAI, Ollama (/v1), llama.cpp, vLLM,
// OpenRouter, Together, Fireworks, LM Studio, Groq. It registers the provider
// kind "openai".
//
// Each question goes in its own request asking for one token. Every option
// is given a one-token label (A, B, … for a choice; 0, 1, … for a score;
// Yes/No for a noul) and the answer's probabilities are read from that
// token's top log-probabilities, conservatively (jev.Conservative): the
// answer keeps only the raw mass the model put on its label, through every
// level of a hierarchy, and what went to tokens that are no label goes to
// the other options, never to the answer. A server that gives none (or
// only the chosen token's) is sampled instead: five answers at temperature
// 1, sent at once, add-one smoothed over the samples asked (jev.SampledProbs),
//
//	p(o) = (count(o) + 1) / (samples asked + options),
//
// a sample that names no label counting toward the samples asked but toward
// no option, its share then going to the other options. So five samples
// never read 0.9: 5 of 5 on two options is 6/7 ≈ 0.857, and an answer whose
// requests were all sampled records what a unanimous result would read
// (jev.Answer.Unanimous), which a low-risk surface may lower its threshold
// to (jevconf.GateThreshold). A response's Probability is sampled when any
// request of any of its questions was. A choice with more options than one
// request can carry is answered hierarchically: which group, then which
// member. Generated text never reaches a caller (plan D3): it is only
// matched against the allowed labels.
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
	// minAlternatives is the fewest alternatives a response must carry to be
	// read as log-probabilities: the chosen token alone says nothing about
	// the others, so such a provider is sampled.
	minAlternatives = 2
	// noCoverage is label mass so small the model answered something else
	// entirely (errNotALabel).
	noCoverage = 0.01
	// retryTopK is what a request asks for after a 400 to defaultTopK, for
	// hosts that cap top_logprobs (some at 5).
	retryTopK   = 5
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
	m := p.Caps.OptionsPerRequest
	if m == 0 {
		m = p.Caps.MaxChoiceOptions // a record saved before OptionsPerRequest
	}
	if m > 0 && m+labelSlack < defaultTopK {
		b.topK = m + labelSlack // a probe found fewer alternatives (perRequest)
	}
	return b, nil
}

// Capabilities implements jev.Backend: the stored capabilities, with what
// this backend has learned since. It takes one question per request and
// any number of options (MaxChoiceOptions 0); OptionsPerRequest is how many
// one request carries (it splits a larger choice itself).
func (b *Backend) Capabilities() jev.Capabilities {
	c := b.stored
	c.Kinds = append([]string(nil), c.Kinds...)
	if len(c.Kinds) == 0 {
		c.Kinds = []string{jev.TypeChoice, jev.TypeNoul, jev.TypeScore}
	}
	c.MaxChoiceOptions, c.OptionsPerRequest, c.QuestionsPerRequest, c.Models = 0, b.perRequest(), 1, true
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
	// A cap an earlier client found on top_logprobs plans this call too.
	if most := recall(b.base, model).topLogprobs; most > 0 {
		b.learnK(most, defaultTopK)
	}
	var (
		sampledAny atomic.Bool
		served     atomic.Value // the model a response named, for a request that named none
	)
	resp, err := jev.FanOut(ctx, req, 0, func(ctx context.Context, _ string, q jev.Question) (jev.Answer, jev.Usage, error) {
		t := &turn{b: b, model: model, state: string(state), q: q}
		probs, err := t.answer(ctx)
		if t.sampled {
			sampledAny.Store(true)
		}
		if t.served != "" {
			served.CompareAndSwap(nil, t.served)
		}
		if err != nil {
			return jev.Answer{}, t.usage, err
		}
		a, err := jev.AnswerFrom(q, probs)
		a.Unanimous = t.unanimous
		return a, t.usage, err
	})
	if resp != nil {
		// The model sent; when none was (the server's default), the one a
		// response named.
		resp.Model = model
		if s, ok := served.Load().(string); ok && model == "" {
			resp.Model = s
		}
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
	if got < minAlternatives || got >= asked {
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
