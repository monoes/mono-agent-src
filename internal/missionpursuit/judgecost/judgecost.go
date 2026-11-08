// Package judgecost is the one way a judge model tells the host what a call cost, without changing the judge.Model interface
// that the doomsday strategy engine shares.
//
// The host puts a Sink in the context of a judge call (With); the model adapter reports the cost the runtime measured
// (Report) when its turn ended. The host charges every call: a call that reported nothing, or an amount that is not a
// plain non-negative number, is charged at PerCallCapUSD and never as free. This package has no dependencies, so the
// pursuit files of the coder host may import it.
package judgecost

import (
	"context"
	"math"
)

// PerCallCapUSD is the most one judge call is allowed to cost (the budget the adapter gives its turn) and what the host
// charges when the real cost is unknown.
const PerCallCapUSD = 0.5

// Sink receives the cost of one call. known is false when the runtime reported none.
type Sink func(usd float64, known bool)

type key struct{}

// With returns a context whose judge calls report to s.
func With(ctx context.Context, s Sink) context.Context { return context.WithValue(ctx, key{}, s) }

// Report tells the sink (if any) what the call cost. An amount that is negative, NaN or infinite is reported as unknown.
func Report(ctx context.Context, usd float64, known bool) {
	s, _ := ctx.Value(key{}).(Sink)
	if s == nil {
		return
	}
	if !known || math.IsNaN(usd) || math.IsInf(usd, 0) || usd < 0 {
		usd, known = 0, false
	}
	s(usd, known)
}
