package verifier

import (
	"context"
	"math"
	"sync"

	"github.com/monoes/mono-agent/internal/monomind"
)

// ExecFunc is monomind.Exec; tests give a fake.
type ExecFunc func(ctx context.Context, opts monomind.ExecOptions, onEvent func(monomind.Event)) (*monomind.TurnResult, error)

// Meter is what the host implements (a later slice) so the reviewer's spend is reserved before the call and settled once
// after it. An error from Reserve refuses the call; no model is asked.
type Meter interface {
	Reserve(ctx context.Context, capUSD float64) (settle func(chargedMicro int64, known bool), err error)
}

// absurdFactor: a reported cost beyond this many caps is not believed (the cap is what the host charges instead).
const absurdFactor = 100

// ChargeFor is what the host charges for a call whose runtime reported usd (known: it reported anything at all). A cost that
// is unknown, not a number, infinite, negative or absurd is never free: it is the cap and the charge says it is not known. A
// believable cost is charged as reported, rounded up to the micro-dollar, even when it passed the cap (the overshoot is real).
func ChargeFor(usd float64, known bool, capUSD float64) (micro int64, charged bool) {
	capMicro := int64(math.Ceil(capUSD * 1e6))
	if !known || math.IsNaN(usd) || math.IsInf(usd, 0) || usd < 0 || usd > capUSD*absurdFactor {
		return capMicro, false
	}
	return int64(math.Ceil(usd * 1e6)), true
}

// costing wraps the reviewer's Exec and remembers what the runtime said the turn cost, whether the turn succeeded or not.
type costing struct {
	inner ExecFunc
	mu    sync.Mutex
	// called: a turn was at least attempted. usd/known: the runtime's report.
	called, known, completed bool
	usd                      float64
}

func (c *costing) exec(ctx context.Context, o monomind.ExecOptions, on func(monomind.Event)) (*monomind.TurnResult, error) {
	c.mu.Lock()
	c.called = true
	c.mu.Unlock()
	o.RequireSandbox = true // the reviewer turn fails closed where the runtime cannot be sandboxed
	res, err := c.inner(ctx, o, on)
	c.mu.Lock()
	if res != nil && res.HasCostUSD {
		c.usd, c.known = res.CostUSD, true
		c.completed = res.SawDone && res.Err == nil && res.ExitCode == 0
	}
	c.mu.Unlock()
	return res, err
}

// charge is what to settle: nothing was spent when no turn was started, else ChargeFor what was reported.
func (c *costing) charge(capUSD float64) (int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.called {
		return 0, true
	}
	// A zero is believed only from a turn that clearly completed and reported a cost; otherwise the cap is charged.
	if c.known && c.usd == 0 && !c.completed {
		return ChargeFor(0, false, capUSD)
	}
	return ChargeFor(c.usd, c.known, capUSD)
}
