package jevcli

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
)

const loweredList = "capture, inbox, people_links, retry and api_auto"

// The consent and status text says what a threshold means for a sampling
// provider, naming exactly the surfaces that lower it, and says nothing for
// any other provider.
func TestOnlyASamplingProviderGetsTheThresholdNote(t *testing.T) {
	claude := jevconf.Provider{Name: "claude", Caps: jev.Capabilities{Probability: jev.ProbSampled}}
	status := sampledNote(claude)
	for _, want := range []string{"claude", "5 answers", "Only " + loweredList + " lower", "all 5 agree",
		"every other surface, the ai.choose, ai.decide, browser.jev and applications.evaluate nodes and the org decider keep the configured threshold"} {
		if !strings.Contains(status, want) {
			t.Errorf("note %q lacks %q", status, want)
		}
	}
	for _, s := range jevconf.Surfaces {
		note := surfaceSampledNote(claude, s)
		want := "Surface " + string(s) + " keeps the configured threshold: a threshold above that never passes there."
		if jevconf.LowersSampled(s) {
			want = "Surface " + string(s) + " lowers a higher threshold to that, so it passes only when all 5 agree."
		}
		if !strings.Contains(note, want) || !strings.Contains(note, "Only "+loweredList+" lower") {
			t.Errorf("%s: note %q lacks %q", s, note, want)
		}
	}
	for _, kind := range []jev.ProbabilityKind{"", jev.ProbNative, jev.ProbLogprob, jev.ProbSoftmax, jev.ProbNone} {
		p := jevconf.Provider{Name: "x", Caps: jev.Capabilities{Probability: kind}}
		if got := sampledNote(p) + surfaceSampledNote(p, jevconf.Inbox); got != "" {
			t.Errorf("%q: note %q", kind, got)
		}
	}
}

// `jev status --json` says which surfaces lower a sampled threshold, so the
// app need not keep its own list.
func TestStatusMarksTheSurfacesThatLowerASampledThreshold(t *testing.T) {
	cfg := jevTestCfg(t, true)
	out, _, err := runJev(t, cfg, "", "status")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	surfaces, _ := jsonMap(t, out)["surfaces"].([]any)
	for _, s := range surfaces {
		m := s.(map[string]any)
		if m["sampled_lowers_threshold"] == true {
			got = append(got, m["surface"].(string))
		}
	}
	if want := "capture,inbox,people_links,retry,api_auto"; strings.Join(got, ",") != want {
		t.Errorf("marked %v, want %s", got, want)
	}
}
