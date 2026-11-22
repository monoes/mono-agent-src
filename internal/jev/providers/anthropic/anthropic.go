// Package anthropic is the decision adapter for Anthropic's Messages API
// (provider kind "anthropic"). Anthropic returns no log-probabilities, so
// every question is asked Samples times at temperature 1 and its answer's
// probabilities are the shares of the samples (jev.ProbSampled). Each
// sample is one forced call of a tool whose only input is an enum of the
// question's labels, so the model can only name an option the code
// enumerated; no text from the model ever reaches a caller (plan D3).
//
// Every question takes all Samples calls, whatever its number of options:
// only a unanimous five gives an answer p = 1.0 (plan O2).
package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
)

const (
	// Kind is the provider kind this package registers.
	Kind = "anthropic"
	// DefaultModel is used when neither the call nor the provider names one.
	DefaultModel = "claude-haiku-5-5"
	// DefaultBaseURL is used when the provider record has none.
	DefaultBaseURL = "https://api.anthropic.com"
	// Samples is how many times one question is asked (plan D7: k = 5).
	Samples = 5
	// minValid is the fewest allowed answers among the samples.
	minValid = 3
	// maxTokens bounds one tool call; a 64-character label fits.
	maxTokens = 100
	timeout   = 30 * time.Second
)

func init() {
	jevconf.RegisterKind(Kind, func(p jevconf.Provider, key string) (jev.Backend, error) { return New(p, key) })
}

// Backend answers through the Messages API. Build it with New.
type Backend struct {
	base, key, model string
	http             *http.Client
	retries          int
}

// New builds the backend of provider record p with its key. It sends the
// key to p.BaseURL only, through jev.KeyedHTTPClient.
func New(p jevconf.Provider, key string) (*Backend, error) {
	b := &Backend{base: strings.TrimRight(p.BaseURL, "/"), key: key, model: p.Model,
		http: jev.KeyedHTTPClient(timeout), retries: 2}
	if b.base == "" {
		b.base = DefaultBaseURL
	}
	if b.model == "" {
		b.model = DefaultModel
	}
	return b, nil
}

// Capabilities implements jev.Backend.
func (b *Backend) Capabilities() jev.Capabilities {
	return jev.Capabilities{
		Kinds:       []string{jev.TypeChoice, jev.TypeNoul, jev.TypeScore},
		Probability: jev.ProbSampled, QuestionsPerRequest: 1, Models: true,
	}
}

// Answer implements jev.Backend: each question is sampled on its own
// (jev.FanOut), and the usage of every sample is summed.
func (b *Backend) Answer(ctx context.Context, req *jev.Request) (*jev.Response, error) {
	model := req.Model
	if model == "" {
		model = b.model
	}
	state, err := json.Marshal(req.State)
	if err != nil {
		return nil, fmt.Errorf("anthropic: encode state: %w", err)
	}
	resp, err := jev.FanOut(ctx, req, 0, func(ctx context.Context, _ string, q jev.Question) (jev.Answer, jev.Usage, error) {
		return b.answerOne(ctx, model, state, q)
	})
	resp.Model, resp.Probability = model, jev.ProbSampled
	return resp, err
}

func (b *Backend) answerOne(ctx context.Context, model string, state []byte, q jev.Question) (jev.Answer, jev.Usage, error) {
	var usage jev.Usage
	p, err := prepare(q)
	if err != nil {
		return jev.Answer{}, usage, err
	}
	body, err := json.Marshal(b.request(model, state, p))
	if err != nil {
		return jev.Answer{}, usage, fmt.Errorf("anthropic: encode request: %w", err)
	}
	counts := map[string]int{}
	valid, asked := 0, 0
	for ; asked < Samples; asked++ {
		label, u, err := b.sample(ctx, body, p)
		usage.InputTokens += u.InputTokens
		usage.OutputTokens += u.OutputTokens
		if err != nil {
			return jev.Answer{}, usage, err
		}
		if label != "" {
			counts[label]++
			valid++
		}
	}
	if valid < minValid {
		return jev.Answer{}, usage, fmt.Errorf("%w: %d of %d samples were not an allowed answer", jev.ErrInvalidAnswer, asked-valid, asked)
	}
	probs := make(map[string]float64, len(counts))
	for label, n := range counts {
		probs[p.keys[label]] = float64(n) / float64(valid)
	}
	a, err := jev.AnswerFrom(q, probs)
	return a, usage, err
}

// sample asks once. label is the model's answer, or "" when it named no
// allowed option (a miss); err is set only when the call itself failed.
func (b *Backend) sample(ctx context.Context, body []byte, p prepared) (label string, u jev.Usage, err error) {
	raw, err := b.do(ctx, "/v1/messages", body, maxBody)
	if err != nil {
		return "", u, err
	}
	var r messagesResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", u, fmt.Errorf("%w: anthropic: undecodable response", jev.ErrInvalidAnswer)
	}
	u = jev.Usage{InputTokens: r.Usage.InputTokens, OutputTokens: r.Usage.OutputTokens}
	if a := r.toolAnswer(); a != "" {
		if _, ok := p.keys[a]; ok {
			return a, u, nil
		}
	}
	return "", u, nil
}

// Probe sends one tiny noul request (a single sample) to prove the endpoint
// and key work, and returns the capabilities.
func (b *Backend) Probe(ctx context.Context) (jev.Capabilities, error) {
	p, err := prepare(jev.Question{Type: jev.TypeNoul, Instructions: "Is this a connectivity check?"})
	if err != nil {
		return jev.Capabilities{}, err
	}
	body, err := json.Marshal(b.request(b.model, []byte(`{}`), p))
	if err != nil {
		return jev.Capabilities{}, fmt.Errorf("anthropic: encode request: %w", err)
	}
	label, _, err := b.sample(ctx, body, p)
	if err != nil {
		return jev.Capabilities{}, err
	}
	if label == "" {
		return jev.Capabilities{}, fmt.Errorf("%w: the probe's answer was neither yes nor no", jev.ErrInvalidAnswer)
	}
	return b.Capabilities(), nil
}

// Models implements jev.ModelLister: GET /v1/models, data[].id.
func (b *Backend) Models(ctx context.Context) ([]jev.Model, error) {
	raw, err := b.do(ctx, "/v1/models?limit=1000", nil, maxModelsBody)
	if err != nil {
		return nil, err
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("anthropic: undecodable models list")
	}
	out := make([]jev.Model, 0, len(list.Data))
	for _, m := range list.Data {
		if m.ID != "" {
			out = append(out, jev.Model{ID: m.ID})
		}
	}
	return out, nil
}
