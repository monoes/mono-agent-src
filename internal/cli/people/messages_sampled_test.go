package people

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
)

// A sampled provider labels a message above its reach only when all five
// samples agree on the intent.
func TestASampledIntentNeedsAUnanimousSample(t *testing.T) {
	for _, agree := range []int{5, 4} {
		fb := jevtest.NewFakeBackend(nil)
		fb.SetSampled(jevtest.Fixed(map[string]string{"intent": "lead"}), agree)
		c := &jev.Client{Provider: "local", Backend: fb, Model: "m"}
		got, err := classifyInboxMessage(context.Background(), c, inboxCandidate{ID: "m1", Body: "I want to buy"}, 0.9)
		if err != nil {
			t.Fatal(err)
		}
		if agree == 5 && (got.Unsure || got.Intent != "lead") || agree == 4 && (!got.Unsure || got.Intent != "") {
			t.Errorf("%d of 5: %+v", agree, got)
		}
	}
}
