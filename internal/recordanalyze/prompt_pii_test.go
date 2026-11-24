package recordanalyze

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/recording"
)

// R16-D3: typed email, phone and address values stay out of the analyzer
// prompt; the step keeps its {{input}} placeholder instead.
func TestPromptMasksPersonalTypedValues(t *testing.T) {
	typed := func(id string, seq int, name, val string) recording.Event {
		return recording.Event{ID: id, Seq: seq, Type: recording.EvType, URL: "https://app.test/form", Value: val,
			Target: &recording.Fingerprint{Tag: "input", InputType: "text", Name: name}}
	}
	evs := []recording.Event{
		{ID: "e0", Seq: 0, Type: recording.EvNavigate, URL: "https://app.test/form"},
		typed("e1", 1, "contact", "jane.doe@example.org"),
		typed("e2", 2, "tel", "+44 20 7946 0958"),
		typed("e3", 3, "addr", "221B Baker Street, London"),
		typed("e4", 4, "city", "Springfield"),
	}
	a := Detect(Normalize(&recording.Summary{ID: "r", URL: "https://app.test/form", Goal: "g"}, evs))
	p, err := BuildPrompt(a, &Env{Analysis: a}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"jane.doe@example.org", "7946 0958", "Baker Street"} {
		if strings.Contains(p, leak) {
			t.Errorf("prompt leaks %q", leak)
		}
	}
	if !strings.Contains(p, "Springfield") {
		t.Error("ordinary typed value should stay")
	}
	if !strings.Contains(p, "{{") {
		t.Error("masked values should be replaced by an input placeholder")
	}
}
