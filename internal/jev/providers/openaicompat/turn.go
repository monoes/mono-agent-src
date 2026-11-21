package openaicompat

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/jev"
)

// A turn answers one question: it plans the requests (one, or a hierarchy
// for many options), sends them and sums their usage.
type turn struct {
	b            *Backend
	model, state string
	q            jev.Question
	usage        jev.Usage
	sampled      bool // some request of the turn was sampled
}

// option is one thing a request offers: the id it stands for and its text.
type option struct{ id, text string }

// answer is the distribution over the question's answer keys: option ids
// for a choice, levels for a score, "true"/"false" for a noul.
func (t *turn) answer(ctx context.Context) (map[string]float64, error) {
	opts, err := optionsOf(t.q)
	if err != nil {
		return nil, err
	}
	switch t.q.Type {
	case jev.TypeNoul:
		return t.flat(ctx, opts, []string{"Yes", "No"}, false)
	case jev.TypeScore:
		if len(opts) <= min(10, t.b.perRequest()) {
			return t.flat(ctx, opts, digits(len(opts)), false)
		}
	}
	return t.choose(ctx, opts)
}

// choose answers over opts with letter labels, hierarchically when there
// are more than one request carries: it asks which group holds the answer,
// then answers within that group. An option's probability is its group's
// times its own within the group; the members of a group not visited share
// the group's probability evenly, so every option has one and they sum to 1.
func (t *turn) choose(ctx context.Context, opts []option) (map[string]float64, error) {
	limit := t.b.perRequest()
	if len(opts) <= limit {
		return t.flat(ctx, opts, letters(len(opts)), false)
	}
	groups := split(opts, limit)
	gopts := make([]option, len(groups))
	budget := max(minGroupRunes, summaryRunes/len(groups))
	for i, g := range groups {
		gopts[i] = option{id: fmt.Sprint(i), text: summary(g, budget)}
	}
	gp, err := t.flat(ctx, gopts, letters(len(gopts)), true)
	if err != nil {
		return nil, err
	}
	best := 0
	for i := range groups {
		if gp[fmt.Sprint(i)] > gp[fmt.Sprint(best)] {
			best = i
		}
	}
	inner, err := t.choose(ctx, groups[best])
	if err != nil {
		return nil, err
	}
	out := make(map[string]float64, len(opts))
	for i, g := range groups {
		pg := gp[fmt.Sprint(i)]
		for _, o := range g {
			if i == best {
				out[o.id] = pg * inner[o.id]
			} else {
				out[o.id] = pg / float64(len(g))
			}
		}
	}
	return out, nil
}

// split cuts opts, in order, into at most limit groups of near-equal size.
func split(opts []option, limit int) [][]option {
	size := (len(opts) + limit - 1) / limit
	var out [][]option
	for i := 0; i < len(opts); i += size {
		out = append(out, opts[i:min(i+size, len(opts))])
	}
	return out
}

// flat asks one question over opts labelled labels and returns the
// distribution over the options' ids.
func (t *turn) flat(ctx context.Context, opts []option, labels []string, grouped bool) (map[string]float64, error) {
	byLabel, err := t.dist(ctx, t.prompt(opts, labels, grouped), labels)
	if err != nil {
		return nil, err
	}
	out := make(map[string]float64, len(opts))
	for i, o := range opts {
		out[o.id] = byLabel[labels[i]]
	}
	return out, nil
}

// dist is the distribution over labels the model gives for user: from one
// request's log-probabilities, or from samples when the provider has none.
func (t *turn) dist(ctx context.Context, user string, labels []string) (map[string]float64, error) {
	if t.b.isSampled() {
		return t.sample(ctx, user, labels, false)
	}
	k := t.b.k()
	res, err := t.b.complete(ctx, t.request(user, 0, k))
	t.add(res)
	var he *httpError
	if errors.As(err, &he) && he.status == 400 {
		// Groq and others refuse the logprobs parameter: sample without it,
		// and remember once a request without it succeeds.
		return t.sample(ctx, user, labels, true)
	}
	if err != nil {
		return nil, err
	}
	top := res.top()
	if top == nil {
		// The server ignored logprobs (or returned a shape nobody reads).
		t.b.setSampled(true)
		return t.sample(ctx, user, labels, false)
	}
	t.b.learnK(len(top), k)
	probs, coverage := jev.FromLogprobs(top, labels)
	if coverage < minCoverage {
		return nil, fmt.Errorf("%w: the model put %.0f%% of its answer on the %d allowed labels (it wanted to say something else)",
			jev.ErrInvalidAnswer, coverage*100, len(labels))
	}
	return probs, nil
}

// sample asks samples times at temperature 1 and counts the answers that
// name an allowed label. learn marks the provider as one without
// log-probabilities once a request without them succeeds.
func (t *turn) sample(ctx context.Context, user string, labels []string, learn bool) (map[string]float64, error) {
	t.sampled = true
	counts := map[string]float64{}
	valid := 0
	for i := 0; i < samples; i++ {
		res, err := t.b.complete(ctx, t.request(user, 1, 0))
		t.add(res)
		if err != nil {
			return nil, err
		}
		if learn {
			t.b.setSampled(true)
			learn = false
		}
		if l, ok := matchOne(res.text(), labels); ok {
			counts[l]++
			valid++
		}
	}
	if valid < minValidSamples {
		return nil, fmt.Errorf("%w: only %d of %d samples named an allowed label", jev.ErrInvalidAnswer, valid, samples)
	}
	return jev.Renormalise(counts), nil
}

// matchOne is the label text names, with jev's label matching.
func matchOne(text string, labels []string) (string, bool) {
	word := strings.TrimSpace(text)
	probs, _ := jev.FromLogprobs([]jev.TokenLogprob{{Token: word}}, labels)
	for l, p := range probs {
		if p > 0 {
			return l, true
		}
	}
	return "", false
}

// request is one chat completion of user: one token, with k alternatives
// (k = 0: no log-probabilities asked for).
func (t *turn) request(user string, temperature float64, k int) chatRequest {
	return chatRequest{
		Model:       t.model,
		Messages:    []message{{Role: "system", Content: systemPrompt}, {Role: "user", Content: user}},
		MaxTokens:   1,
		Temperature: temperature,
		Logprobs:    k > 0,
		TopLogprobs: k,
	}
}

func (t *turn) add(res *chatResponse) {
	if res == nil {
		return
	}
	t.usage.InputTokens += res.Usage.PromptTokens
	t.usage.OutputTokens += res.Usage.CompletionTokens
	if c := res.Usage.Cost; c != nil {
		sum := *c
		if t.usage.CostUSD != nil {
			sum += *t.usage.CostUSD
		}
		t.usage.CostUSD = &sum
	}
}

func letters(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = string(rune('A' + i))
	}
	return out
}

func digits(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprint(i)
	}
	return out
}
