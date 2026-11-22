package hfzeroshot

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
)

// The backend names a model even when the record has none: the endpoint's
// last path segment, else its host. jev.Client's fallback to its own model
// is not what this checks.
func TestTheResponseNamesTheModel(t *testing.T) {
	for _, c := range []struct{ base, model, want string }{
		{"https://router.huggingface.co/hf-inference/models/facebook/bart-large-mnli", "", "bart-large-mnli"},
		{"https://router.huggingface.co/hf-inference/models/facebook/bart-large-mnli/", "", "bart-large-mnli"},
		{"https://abc123.endpoints.huggingface.cloud", "", "abc123.endpoints.huggingface.cloud"},
		{"https://abc123.endpoints.huggingface.cloud/", "", "abc123.endpoints.huggingface.cloud"},
		{"http://127.0.0.1:8080/x", "my-nli", "my-nli"},
	} {
		if got := modelOf(jevconf.Provider{BaseURL: c.base, Model: c.model}); got != c.want {
			t.Errorf("modelOf(%q, %q) = %q, want %q", c.base, c.model, got, c.want)
		}
	}
	f := newFake(t, pairs(0.8, 0.2))
	b, err := New(jevconf.Provider{BaseURL: f.URL + "/models/nli-small"}, "")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := b.Answer(context.Background(), &jev.Request{Questions: map[string]jev.Question{"q": {Type: jev.TypeNoul}}})
	if err != nil || resp.Model != "nli-small" {
		t.Fatalf("model %q %v", resp.Model, err)
	}
	if got := jev.ResponseLabel(&jev.Response{Provider: "hf"}, ""); got != "hf" {
		t.Errorf("label %q", got)
	}
}

// Scores are a forced softmax, reported as such (not native, not
// calibrated), and a reply that scores only some labels keeps the missing
// mass off the answer: 0.6 for one label of three stays 0.6.
func TestScoresAreSoftmaxAndAPartialReplyIsNotInflated(t *testing.T) {
	f := newFake(t, pairs(0.6)) // the other two labels get no score
	c := providerClient(t, f.URL, testKey)
	q := jev.Question{Type: jev.TypeChoice, Criteria: map[string]any{"a": "A", "b": "B", "c": "C"}}
	resp, err := c.Ask(context.Background(), map[string]any{}, map[string]jev.Question{"q": q})
	if err != nil {
		t.Fatal(err)
	}
	id, p := jev.GateValue(resp, resp.Answers["q"])
	if id != "a" || !near(p, 0.6) || resp.Probability != jev.ProbSoftmax || c.Backend.Capabilities().Probability != jev.ProbSoftmax {
		t.Fatalf("gate %s %v, probability %s", id, p, resp.Probability)
	}
}
