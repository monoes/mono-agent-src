// Package hfzeroshot is the decision adapter for a Hugging Face zero-shot
// classification endpoint (provider kind "hf-zeroshot"): an Inference
// Endpoint, a TEI server, or the router's hf-inference models. The
// provider's base_url is the model's full endpoint URL. The model scores
// the candidate labels the code sends (a choice's option descriptions, a
// noul's two answers) and nothing else, so it can only name an option the
// code enumerated (plan D3). It answers choice and noul questions; a score
// is jev.ErrUnsupported.
package hfzeroshot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
)

const (
	// Kind is the provider kind this package registers.
	Kind = "hf-zeroshot"
	// MaxLabels is the most options one choice question may have.
	MaxLabels = 50
	// MaxInputChars caps the text sent: these models have short contexts.
	MaxInputChars = 6000
	timeout       = 30 * time.Second
)

func init() {
	jevconf.RegisterKind(Kind, func(p jevconf.Provider, key string) (jev.Backend, error) { return New(p, key) })
}

// Backend answers through one zero-shot classification endpoint. Build it with New.
type Backend struct {
	url, key string
	http     *http.Client
	retries  int
}

// New builds the backend of provider record p with its key ("" = none is
// sent). It posts to p.BaseURL exactly, through jev.KeyedHTTPClient.
func New(p jevconf.Provider, key string) (*Backend, error) {
	if strings.TrimSpace(p.BaseURL) == "" {
		return nil, errors.New("hf-zeroshot: base_url must be the model's full endpoint URL")
	}
	return &Backend{url: p.BaseURL, key: key, http: jev.KeyedHTTPClient(timeout), retries: 2}, nil
}

// Capabilities implements jev.Backend.
func (b *Backend) Capabilities() jev.Capabilities {
	return jev.Capabilities{
		Kinds:       []string{jev.TypeChoice, jev.TypeNoul},
		Probability: jev.ProbNative, MaxChoiceOptions: MaxLabels, QuestionsPerRequest: 1,
	}
}

// Answer implements jev.Backend: one classification per question (jev.FanOut).
func (b *Backend) Answer(ctx context.Context, req *jev.Request) (*jev.Response, error) {
	state, err := json.Marshal(req.State)
	if err != nil {
		return nil, fmt.Errorf("hf-zeroshot: encode state: %w", err)
	}
	resp, err := jev.FanOut(ctx, req, 0, func(ctx context.Context, _ string, q jev.Question) (jev.Answer, jev.Usage, error) {
		a, err := b.answerOne(ctx, state, q)
		return a, jev.Usage{}, err
	})
	resp.Probability = jev.ProbNative
	return resp, err
}

func (b *Backend) answerOne(ctx context.Context, state []byte, q jev.Question) (jev.Answer, error) {
	labels, keys, err := candidates(q)
	if err != nil {
		return jev.Answer{}, err
	}
	scores, err := b.classify(ctx, inputs(q, state), labels)
	if err != nil {
		return jev.Answer{}, err
	}
	probs := make(map[string]float64, len(scores))
	for label, s := range scores {
		probs[keys[label]] = s
	}
	if q.Type == jev.TypeNoul {
		p := jev.Renormalise(map[string]float64{"true": probs["true"], "false": probs["false"]})
		if p == nil {
			return jev.Answer{}, fmt.Errorf("%w: hf-zeroshot: no usable score for yes or no", jev.ErrInvalidAnswer)
		}
		return jev.NoulAnswer(p["true"])
	}
	return jev.ChoiceAnswer(q, probs)
}

func (b *Backend) classify(ctx context.Context, text string, labels []string) (map[string]float64, error) {
	body, err := json.Marshal(classifyRequest{Inputs: text, Parameters: parameters{CandidateLabels: labels}})
	if err != nil {
		return nil, fmt.Errorf("hf-zeroshot: encode request: %w", err)
	}
	raw, err := b.post(ctx, body)
	if err != nil {
		return nil, err
	}
	return decodeScores(raw, labels)
}

// candidates is q's candidate labels and the answer key of each: a
// choice's option descriptions (the id when a description is empty; a
// repeated label gets " (<id>)" appended) mapping to their ids, or a noul's
// two answers mapping to "true" and "false".
func candidates(q jev.Question) (labels []string, keys map[string]string, err error) {
	keys = map[string]string{}
	descs := criteria(q.Criteria)
	switch q.Type {
	case jev.TypeNoul:
		yes, no := descs["true"], descs["false"]
		if yes == "" || no == "" || yes == no {
			yes, no = "yes", "no"
		}
		return []string{yes, no}, map[string]string{yes: "true", no: "false"}, nil
	case jev.TypeChoice:
		ids := jev.OrderedOptionIDs(q)
		if len(ids) == 0 {
			return nil, nil, fmt.Errorf("%w: the question has no options", jev.ErrInvalidAnswer)
		}
		if len(ids) > MaxLabels {
			return nil, nil, fmt.Errorf("%w: hf-zeroshot takes at most %d options in one question, this one has %d",
				jev.ErrUnsupported, MaxLabels, len(ids))
		}
		for _, id := range ids {
			label := descs[id]
			if label == "" {
				label = id
			}
			for {
				if _, dup := keys[label]; !dup {
					break
				}
				label = fmt.Sprintf("%s (%s)", label, id)
			}
			labels = append(labels, label)
			keys[label] = id
		}
		return labels, keys, nil
	}
	return nil, nil, fmt.Errorf("%w: hf-zeroshot answers choice and noul questions, not %q", jev.ErrUnsupported, q.Type)
}

// inputs is the text classified: the question's instructions, then the
// state as compact JSON, cut to MaxInputChars characters.
func inputs(q jev.Question, state []byte) string {
	s := string(state)
	if in := describe(q.Instructions); in != "" {
		s = in + "\n\n" + s
	}
	if utf8.RuneCountInString(s) <= MaxInputChars {
		return s
	}
	r := []rune(s)
	return string(r[:MaxInputChars])
}

// Probe sends one tiny noul request to prove the endpoint and key work, and
// returns the capabilities.
func (b *Backend) Probe(ctx context.Context) (jev.Capabilities, error) {
	if _, err := b.answerOne(ctx, []byte(`{}`), jev.Question{Type: jev.TypeNoul, Instructions: "Is this a connectivity check?"}); err != nil {
		return jev.Capabilities{}, err
	}
	return b.Capabilities(), nil
}

// criteria reads a choice's or noul's {id: description} map.
func criteria(c any) map[string]string {
	out := map[string]string{}
	switch c := c.(type) {
	case map[string]any:
		for k, v := range c {
			out[k] = describe(v)
		}
	case map[string]string:
		for k, v := range c {
			out[k] = strings.TrimSpace(v)
		}
	}
	return out
}

// describe is v as text: a string as is, anything else as JSON.
func describe(v any) string {
	switch v := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}
