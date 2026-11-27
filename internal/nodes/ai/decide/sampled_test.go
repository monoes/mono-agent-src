package decide

import (
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/workflow"
)

// A routed branch may lead to a send, so every kind of unanimous sample below
// min_confidence goes to low_confidence.
func TestASampledDecisionKeepsTheConfiguredThreshold(t *testing.T) {
	for _, c := range []struct {
		raw  map[string]interface{}
		pick string
	}{
		{map[string]interface{}{"kind": "noul"}, "true"},
		{map[string]interface{}{"kind": "noul"}, "false"},
		{map[string]interface{}{"kind": "score", "levels": []any{"low", "mid", "high"}}, "2"},
		{map[string]interface{}{"kind": "choice", "options": []any{"x", "y"}}, "y"},
	} {
		c.raw["question"], c.raw["min_confidence"] = "q?", 0.9
		cfg, err := parseConfig(c.raw)
		if err != nil {
			t.Fatal(err)
		}
		a, err := jevtest.Sampled(buildQuestion(cfg), c.pick, 5)
		if err != nil {
			t.Fatal(err)
		}
		resp := &jev.Response{Probability: jev.ProbSampled, Answers: map[string]jev.Answer{questionID: a}}
		if handle, _ := decide(cfg, workflow.Item{}, resp, "m"); handle != LowConfidenceHandle {
			t.Errorf("%s %q: handle %q, want %q", cfg.kind, c.pick, handle, LowConfidenceHandle)
		}
	}
}
