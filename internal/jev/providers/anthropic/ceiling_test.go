package anthropic

import (
	"context"
	"fmt"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
)

// Every answer records what a unanimous five would read on its number of
// options, so a low-risk surface's threshold is capped at it: five agreeing samples
// pass any threshold of a surface that takes the lowering, four never pass one above their own share.
func TestAnAnswerRecordsItsUnanimousProbability(t *testing.T) {
	for _, k := range []int{2, 3, 12, 40} {
		ids := make([]string, k)
		for i := range ids {
			ids[i] = fmt.Sprintf("o%02d", i)
		}
		for _, agree := range []int{5, 4} {
			samples := []string{ids[0], ids[0], ids[0], ids[0], ids[0]}
			if agree == 4 {
				samples[4] = ids[1]
			}
			f := newFake(t, sequence(samples...))
			b, err := New(jevconf.Provider{BaseURL: f.URL}, testKey)
			if err != nil {
				t.Fatal(err)
			}
			c := &jev.Client{Provider: "claude", Backend: b}
			resp, err := c.Ask(context.Background(), map[string]any{}, map[string]jev.Question{"q": choice(ids...)})
			if err != nil {
				t.Fatal(err)
			}
			a := resp.Answers["q"]
			_, p := jev.GateValue(resp, a)
			if !near(a.Unanimous, 6/float64(5+k)) || !near(p, float64(agree+1)/float64(5+k)) {
				t.Fatalf("k=%d, %d of 5: p=%v, unanimous=%v", k, agree, p, a.Unanimous)
			}
			if got := p >= jevconf.GateThreshold(jevconf.Inbox, resp, a, 0.9); got != (agree == 5) {
				t.Errorf("k=%d, %d of 5 at 0.9: passes %v", k, agree, got)
			}
		}
	}
}
