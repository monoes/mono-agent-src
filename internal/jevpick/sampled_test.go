package jevpick

import (
	"context"
	"errors"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
)

// The element pick (action fallback, record replay, the social bots) clicks
// and types: a unanimous sample below the threshold picks nothing.
func TestASampledPickKeepsTheConfiguredThreshold(t *testing.T) {
	fb := jevtest.NewFakeBackend(nil)
	fb.SetSampled(jevtest.Fixed(map[string]string{"target": "2"}), 5)
	c := &jev.Client{Provider: "local", Backend: fb}
	page := newPickPage()
	picked, err := Pick(context.Background(), c, page, Target{Intent: "add to cart", Kind: "click"}, 0.9)
	if !errors.Is(err, ErrNoMatch) || len(page.marked) != 0 {
		t.Fatalf("unanimous below the threshold: %+v %v (marked %v)", picked, err, page.marked)
	}
}
