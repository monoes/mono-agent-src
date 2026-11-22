package openaicompat

import (
	"context"
	"errors"

	"github.com/monoes/mono-agent/internal/jev"
)

// Probe asks the provider one tiny yes/no question and records what came
// back: log-probabilities (and how many alternatives a response carries) or
// none, in which case the backend samples. A 400 to the logprobs parameter
// is retried once without it. It returns the capabilities found; an error
// means the provider did not answer at all.
func (b *Backend) Probe(ctx context.Context) (jev.Capabilities, error) {
	t := &turn{b: b, model: b.model, state: `{"a":2,"b":1}`,
		q: jev.Question{Type: jev.TypeNoul, Instructions: "Is a greater than b?"}}
	opts, _ := optionsOf(t.q)
	user := t.prompt(opts, []string{"Yes", "No"}, false)
	res, err := b.complete(ctx, t.request(user, 0, defaultTopK))
	var he *httpError
	if errors.As(err, &he) && he.status == 400 {
		if _, err := b.complete(ctx, t.request(user, 0, 0)); err != nil {
			return jev.Capabilities{}, err
		}
		b.setSampled(true)
		return b.Capabilities(), nil
	}
	if err != nil {
		return jev.Capabilities{}, err
	}
	top := res.top()
	b.setSampled(len(top) < minAlternatives)
	if len(top) >= minAlternatives {
		b.mu.Lock()
		b.topK = min(defaultTopK, len(top))
		b.mu.Unlock()
	}
	return b.Capabilities(), nil
}
