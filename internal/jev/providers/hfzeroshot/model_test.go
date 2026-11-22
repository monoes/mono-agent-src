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
