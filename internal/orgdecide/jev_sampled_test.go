package orgdecide

import (
	"context"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/jev/providers/openaicompat"
)

// The jev decider is never lowered, at any level it decides at: a unanimous
// sample below the threshold leaves the item to the model. At mid (three
// verdicts, at most 6/8) that is the default 0.8; at full (two verdicts, 6/7)
// it is 0.9.
func TestASampledDeciderKeepsTheConfiguredThreshold(t *testing.T) {
	srv := newOpenAIDecider(t)
	b, err := openaicompat.New(jevconf.Provider{BaseURL: srv.URL, Model: "m", Caps: jev.Capabilities{Probability: jev.ProbSampled}}, "")
	if err != nil {
		t.Fatal(err)
	}
	srv.set("sampled")
	for _, c := range []struct {
		level     string
		threshold float64
		note      string
	}{
		{"mid", DefaultJevThreshold, "p=0.75 below 0.80 ("},
		{"full", 0.9, "p=0.86 below 0.90 ("},
	} {
		fallback := &scriptedDecider{replies: map[string]string{KindGate: `{"verdict":"approve","rationale":"the model"}`}}
		jd := &JevDecider{Client: &jev.Client{Provider: "local", Backend: b, Model: "m"}, Fallback: fallback, Threshold: c.threshold}
		out, err := jd.Decide(context.Background(), gatePrompt(c.level))
		if err != nil {
			t.Fatal(err)
		}
		if len(fallback.prompts) != 1 || out.Verdict.Verdict != "approve" || !strings.Contains(out.Verdict.Rationale, c.note) {
			t.Errorf("%s at %v: %+v", c.level, c.threshold, out)
		}
	}
}
