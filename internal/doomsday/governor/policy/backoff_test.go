package policy

import (
	"math"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
)

func TestBackoffLowersOnlyThatProvider(t *testing.T) {
	p, c := newP()
	p.RateLimited("openrouter", 0)
	if pe := p.Provider(lim, "openrouter"); pe.Value != 15 || pe.Until != c.now+time.Second {
		t.Fatalf("%+v", pe)
	}
	if pe := p.Provider(lim, "anthropic"); pe.Value != 30 || pe.Until != 0 {
		t.Fatalf("other provider narrowed: %+v", pe)
	}
	// the global decision is untouched
	if e := p.Decide(lim, good(), nil); e.Value != 30 {
		t.Fatalf("%+v", e)
	}
}

func TestBackoffExpiresOnTheMonotonicClockAndRestoresTheCap(t *testing.T) {
	p, c := newP()
	p.RateLimited("x", 30*time.Second)
	c.now += 29 * time.Second
	if pe := p.Provider(lim, "x"); pe.Value >= 30 {
		t.Fatalf("expired early %+v", pe)
	}
	c.now += 2 * time.Second
	if pe := p.Provider(lim, "x"); pe.Value != 30 || pe.Value > lim.MaxConcurrent {
		t.Fatalf("not restored %+v", pe)
	}
}

func TestBackoffIsExponentialAndCapped(t *testing.T) {
	p, c := newP()
	var windows []time.Duration
	for i := 0; i < 30; i++ {
		p.RateLimited("x", 0)
		windows = append(windows, p.Provider(lim, "x").Until-c.now)
		c.now += 1 // still inside the window: consecutive 429s
	}
	for i, w := range windows {
		want := min(time.Second<<min(i, 20), MaxBackoff)
		if w < want-1 || w > want {
			t.Fatalf("n=%d window %v want ~%v", i+1, w, want)
		}
	}
	if windows[len(windows)-1] > MaxBackoff {
		t.Fatal("cap exceeded")
	}
}

func TestRetryAfterIsBoundedAndHostileValuesAreHarmless(t *testing.T) {
	p, c := newP()
	for _, ra := range []time.Duration{math.MaxInt64, -5, 1 << 62, -math.MaxInt64} {
		q, c2 := newP()
		q.RateLimited("x", ra)
		w := q.Provider(lim, "x").Until - c2.now
		if w < 0 || w > MaxBackoff {
			t.Fatalf("retryAfter %v gave window %v", ra, w)
		}
	}
	// near the end of the clock range the window saturates instead of wrapping
	c.now = time.Duration(math.MaxInt64) - time.Second
	p.RateLimited("x", time.Hour)
	if pe := p.Provider(lim, "x"); pe.Until < c.now || pe.Value >= 30 {
		t.Fatalf("overflow wrapped %+v", pe)
	}
}

func TestClockGoingBackwardsDoesNotShortenABackoff(t *testing.T) {
	p, c := newP()
	c.now = time.Hour
	p.RateLimited("x", time.Minute)
	c.now = 0 // a broken clock steps back
	if pe := p.Provider(lim, "x"); pe.Value >= 30 {
		t.Fatalf("window vanished %+v", pe)
	}
	c.now = time.Hour + 2*time.Minute
	if pe := p.Provider(lim, "x"); pe.Value != 30 {
		t.Fatalf("%+v", pe)
	}
}

func TestQuietProviderStartsOverAndTheMapIsBounded(t *testing.T) {
	p, c := newP()
	for i := 0; i < 5; i++ {
		p.RateLimited("x", 0)
		c.now += 1
	}
	c.now += MaxBackoff + QuietReset + time.Second
	p.RateLimited("x", 0)
	if w := p.Provider(lim, "x").Until - c.now; w > time.Second+time.Second/4 {
		t.Fatalf("did not start over: %v", w)
	}
	// hostile stream of names
	p, _ = newP()
	for i := 0; i < 10*MaxProviders; i++ {
		p.RateLimited(string(rune('a'+i%26))+time.Duration(i).String()+string(make([]byte, 300)), 0)
	}
	if len(p.prov.m) > MaxProviders+1 {
		t.Fatalf("map grew to %d", len(p.prov.m))
	}
	for k := range p.prov.m {
		if len(k) > MaxProviderName {
			t.Fatal("unbounded key")
		}
	}
	// an unknown provider is still narrowed once the table overflowed
	if pe := p.Provider(lim, "brand-new"); pe.Value >= 30 {
		t.Fatalf("overflow provider not narrowed: %+v", pe)
	}
}

func TestJitterIsBoundedAndHostileSourcesAreIgnored(t *testing.T) {
	for _, r := range []float64{math.NaN(), -1, 2, math.Inf(1), 0.9999} {
		c := &fakeClock{}
		p := New(c, WithJitter(func() float64 { return r }))
		p.RateLimited("x", 0)
		w := p.Provider(lim, "x").Until
		if w < time.Second || w > time.Second+time.Second/4 {
			t.Fatalf("jitter %v gave %v", r, w)
		}
	}
}

func TestProviderValueNeverLeavesOneToCap(t *testing.T) {
	p, _ := newP()
	for i := 0; i < 70; i++ {
		p.RateLimited("x", 0)
	}
	for _, cap := range []int{-1, 0, 1, 2, 30, math.MaxInt} {
		l := governor.Limits{MaxConcurrent: cap}
		pe := p.Provider(l, "x")
		if cap < 1 {
			if pe.Value != 0 {
				t.Fatalf("cap %d: %+v", cap, pe)
			}
			continue
		}
		if pe.Value < 1 || pe.Value > cap {
			t.Fatalf("cap %d: %+v", cap, pe)
		}
	}
}

func TestSystemClockIsMonotonic(t *testing.T) {
	c := NewSystemClock()
	a := c.Now()
	time.Sleep(2 * time.Millisecond)
	if b := c.Now(); b <= a {
		t.Fatal(a, b)
	}
}

// A huge Retry-After with jitter would wrap the window negative if it were not bounded before the jitter is added.
func TestHugeRetryAfterWithJitterDoesNotWrap(t *testing.T) {
	c := &fakeClock{}
	p := New(c, WithJitter(func() float64 { return 0.9 }))
	p.RateLimited("x", time.Duration(math.MaxInt64))
	if w := p.Provider(lim, "x").Until; w <= 0 || w > MaxBackoff {
		t.Fatalf("window %v", w)
	}
}

// A clock that steps back must not let a later 429 shorten the window already granted.
func TestStepBackNeverShortensAnExistingWindow(t *testing.T) {
	p, c := newP()
	c.now = time.Hour
	p.RateLimited("x", 5*time.Minute)
	first := p.Provider(lim, "x").Until
	c.now = 0
	p.RateLimited("x", 0)
	if second := p.Provider(lim, "x").Until; second < first {
		t.Fatalf("window shortened from %v to %v", first, second)
	}
}
