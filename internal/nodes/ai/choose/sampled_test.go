package choose

import (
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/workflow"
)

// A routed branch may lead to a send, so a unanimous sample below
// min_confidence goes to low_confidence.
func TestASampledChoiceKeepsTheConfiguredThreshold(t *testing.T) {
	for minP, low := range map[float64]bool{0.9: true, 0.7: false} {
		cfg, err := parseConfig(map[string]interface{}{"cases": []any{"a", "b", "c"}, "min_confidence": minP})
		if err != nil {
			t.Fatal(err)
		}
		byValue := map[string]chooseCase{}
		for _, c := range cfg.cases {
			byValue[c.value] = c
		}
		a, err := jevtest.Sampled(buildQuestions(cfg)[primaryQuestion], "b", 5) // reads 0.75
		if err != nil {
			t.Fatal(err)
		}
		resp := &jev.Response{Probability: jev.ProbSampled, Answers: map[string]jev.Answer{primaryQuestion: a}}
		handle, _ := decide(cfg, byValue, workflow.Item{}, resp, "m")
		if (handle == LowConfidenceHandle) != low {
			t.Errorf("min_confidence %v: handle %q", minP, handle)
		}
	}
}
