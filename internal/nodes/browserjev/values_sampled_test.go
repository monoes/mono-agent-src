package browserjev

import (
	"context"
	"fmt"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/jevpick"
)

// Typing a configured value is never lowered: a unanimous sample below the
// plain 0.6 or the vault 0.9 types nothing.
func TestASampledValueKeepsTheConfiguredThreshold(t *testing.T) {
	a := &jevpick.Action{ID: "f", Kind: "fill", Label: "API token", Node: 1}
	page := &jevpick.PageState{}
	for _, c := range []struct {
		name   string
		secret bool
		values int
	}{
		{"vault value, reads 6/7", true, 1},
		{"plain value, reads 6/11", false, 5},
	} {
		fb := jevtest.NewFakeBackend(nil)
		fb.SetSampled(jevtest.Fixed(map[string]string{"value": "Token"}), 5)
		client := &jev.Client{Provider: "local", Backend: fb}
		values := []namedValue{{Name: "Token", Value: "tok-123456", Secret: c.secret}}
		for i := 1; i < c.values; i++ {
			values = append(values, namedValue{Name: fmt.Sprintf("other%d", i), Value: fmt.Sprintf("value-%d", i)})
		}
		vp, err := pickValue(context.Background(), client, values, scrubber{values: values}, "save the token", a, page, "text")
		if err != nil {
			t.Fatal(err)
		}
		if vp.name != "Token" || vp.value != nil {
			t.Errorf("%s: picked %q p=%v, typed %v", c.name, vp.name, vp.probability, vp.value != nil)
		}
	}
}
