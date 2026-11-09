package judgecost

import (
	"context"
	"math"
	"testing"
)

func TestReportReachesTheSinkAndOnlyAPlainAmountIsKnown(t *testing.T) {
	Report(context.Background(), 1, true) // no sink: nothing happens
	for _, c := range []struct {
		usd   float64
		known bool
		want  bool
	}{{0.25, true, true}, {0, true, true}, {0.1, false, false}, {-0.1, true, false}, {math.NaN(), true, false}, {math.Inf(1), true, false}} {
		var gotUSD float64
		var gotKnown bool
		n := 0
		Report(With(context.Background(), func(u float64, k bool) { gotUSD, gotKnown, n = u, k, n+1 }), c.usd, c.known)
		if n != 1 || gotKnown != c.want {
			t.Errorf("%+v: n=%d known=%t", c, n, gotKnown)
		}
		if !gotKnown && gotUSD != 0 {
			t.Errorf("%+v: an unknown cost carried the amount %v", c, gotUSD)
		}
	}
}
