package captureclassify

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
)

// A sampled provider suggests a route above its reach only when all five
// samples agree.
func TestASampledRouteNeedsAUnanimousSample(t *testing.T) {
	for _, agree := range []int{5, 4} {
		fb := jevtest.NewFakeBackend(nil)
		fb.SetSampled(jevtest.Fixed(map[string]string{QuestionID: "job_posting"}), agree)
		c := &jev.Client{Provider: "local", Backend: fb}
		r, err := Classify(context.Background(), c, Input{URL: "https://example.test/job", Content: "We are hiring", Threshold: 0.9})
		if err != nil {
			t.Fatal(err)
		}
		if r.Kind != "job_posting" || (r.SuggestedRoute != "") != (agree == 5) {
			t.Errorf("%d of 5: %+v", agree, r)
		}
	}
}
