package policy

import (
	"math"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
)

// Provider backoff bounds. A provider asking for more than MaxBackoff is held to MaxBackoff.
const (
	BaseBackoff = time.Second
	MaxBackoff  = 10 * time.Minute
	// QuietReset: a provider that has been quiet this long after its backoff ended starts over at the first step.
	QuietReset = 5 * time.Minute
	// MaxProviders and MaxProviderName bound the memory a hostile or buggy provider name stream can use.
	MaxProviders    = 64
	MaxProviderName = 128
	overflowKey     = "*other*"
	jitterFrac      = 0.25
)

type backoff struct {
	n     int           // consecutive 429s
	until time.Duration // monotonic end of the window
}

type providers struct {
	m    map[string]*backoff
	last time.Duration // highest clock reading seen: the clock is forced monotonic here
}

func newProviders() providers { return providers{m: map[string]*backoff{}} }

// now returns the clock reading, never lower than an earlier one.
func (p *Policy) now() time.Duration {
	t := p.clock.Now()
	if t < p.prov.last {
		return p.prov.last
	}
	p.prov.last = t
	return t
}

func (p *Policy) key(provider string) string {
	if len(provider) > MaxProviderName {
		provider = provider[:MaxProviderName]
	}
	return provider
}

// RateLimited implements governor.Policy: the window is max(Retry-After, exponential step) plus jitter, never above
// MaxBackoff, measured on the monotonic clock. A negative or missing retryAfter counts as no header.
func (p *Policy) RateLimited(provider string, retryAfter time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	k := p.key(provider)
	b, ok := p.prov.m[k]
	if !ok {
		if len(p.prov.m) >= MaxProviders {
			p.prune(now)
		}
		if len(p.prov.m) >= MaxProviders {
			k = overflowKey
			b = p.prov.m[k]
		}
		if b == nil {
			b = &backoff{}
			p.prov.m[k] = b
		}
	}
	if now >= b.until && now-b.until >= QuietReset {
		b.n = 0
	}
	if b.n < 40 {
		b.n++
	}
	step := BaseBackoff << min(b.n-1, 20)
	d := max(step, min(max(retryAfter, 0), MaxBackoff))
	d += time.Duration(float64(d) * jitterFrac * p.jitter())
	d = min(d, MaxBackoff)
	b.until = max(b.until, addSat(now, d))
}

func (p *Policy) jitter() float64 {
	if p.rand == nil {
		return 0
	}
	v := p.rand()
	if !(v >= 0 && v < 1) {
		return 0
	}
	return v
}

// prune drops providers whose window ended and that have been quiet for QuietReset.
func (p *Policy) prune(now time.Duration) {
	for k, b := range p.prov.m {
		if now >= b.until && now-b.until >= QuietReset {
			delete(p.prov.m, k)
		}
	}
}

// Provider implements governor.Policy: while a window is open the provider gets cap/2^n (at least 1, at most cap);
// otherwise exactly the cap. The global decision is not involved, so only this provider is narrowed.
func (p *Policy) Provider(l governor.Limits, provider string) governor.ProviderEffective {
	p.mu.Lock()
	defer p.mu.Unlock()
	capN := max(l.MaxConcurrent, 0)
	now := p.now()
	pe := governor.ProviderEffective{Provider: provider, Value: capN}
	k := p.key(provider)
	b, ok := p.prov.m[k]
	if !ok {
		b, ok = p.prov.m[overflowKey]
	}
	if !ok || now >= b.until || capN < 1 {
		return pe
	}
	pe.Value = clampInt(capN>>min(b.n, 62), 1, capN)
	pe.Until = b.until
	return pe
}

func addSat(a, d time.Duration) time.Duration {
	if a > time.Duration(math.MaxInt64)-d {
		return time.Duration(math.MaxInt64)
	}
	return a + d
}
