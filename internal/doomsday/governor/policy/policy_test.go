package policy

import (
	"errors"
	"math"
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
)

type fakeClock struct{ now time.Duration }

func (c *fakeClock) Now() time.Duration { return c.now }

var lim = governor.Limits{MaxConcurrent: 30, LivePerTree: 10}

func good() governor.Sample {
	return governor.Sample{Valid: true, Load1: 0.5, Cores: 8, MemAvailFrac: 0.6, DiskFreeBytes: 200 << 30, DiskFreeFrac: 0.5}
}

func newP() (*Policy, *fakeClock) {
	c := &fakeClock{}
	return New(c), c
}

func TestHealthyMachineGetsTheCapAndNeverMore(t *testing.T) {
	p, _ := newP()
	e := p.Decide(lim, good(), nil)
	if e.Value != 30 || e.Cap != 30 || !e.Admitting || len(e.Reasons) != 0 {
		t.Fatalf("%+v", e)
	}
}

func TestSamplerFailureIsExactlySafeMinNeverTheCap(t *testing.T) {
	p, _ := newP()
	p.Decide(lim, good(), nil)
	e := p.Decide(lim, governor.Sample{}, errors.New("proc unreadable"))
	if e.Value != governor.DefaultSafeMin || !e.Admitting || !reflect.DeepEqual(e.Reasons, []governor.Reason{governor.ReasonSampleFail}) {
		t.Fatalf("%+v", e)
	}
	// a charter safe minimum is honoured, clamped to the cap
	l := lim
	l.SafeMin = 5
	if e := p.Decide(l, governor.Sample{}, governor.ErrUnsupported); e.Value != 5 {
		t.Fatalf("%+v", e)
	}
	l.SafeMin = 99
	if e := p.Decide(l, governor.Sample{}, governor.ErrUnsupported); e.Value != 30 {
		t.Fatalf("%+v", e)
	}
	// an unsupported platform never reaches the cap, however often it is asked
	for i := 0; i < 50; i++ {
		if e := p.Decide(lim, governor.Sample{}, governor.ErrUnsupported); e.Value != 2 {
			t.Fatal(e)
		}
	}
}

func TestImplausibleSamplesAreFailures(t *testing.T) {
	nan, inf := math.NaN(), math.Inf(1)
	mut := map[string]func(*governor.Sample){
		"not valid":   func(s *governor.Sample) { s.Valid = false },
		"nan load":    func(s *governor.Sample) { s.Load1 = nan },
		"inf load":    func(s *governor.Sample) { s.Load1 = inf },
		"neg load":    func(s *governor.Sample) { s.Load1 = -1 },
		"huge load":   func(s *governor.Sample) { s.Load1 = 1e30 },
		"zero cores":  func(s *governor.Sample) { s.Cores = 0 },
		"neg cores":   func(s *governor.Sample) { s.Cores = -4 },
		"huge cores":  func(s *governor.Sample) { s.Cores = math.MaxInt },
		"nan mem":     func(s *governor.Sample) { s.MemAvailFrac = nan },
		"neg mem":     func(s *governor.Sample) { s.MemAvailFrac = -0.1 },
		"mem > 1":     func(s *governor.Sample) { s.MemAvailFrac = 1.5 },
		"nan disk":    func(s *governor.Sample) { s.DiskFreeFrac = nan },
		"disk frac>1": func(s *governor.Sample) { s.DiskFreeFrac = 7 },
		"neg disk":    func(s *governor.Sample) { s.DiskFreeFrac = -1 },
	}
	for name, m := range mut {
		t.Run(name, func(t *testing.T) {
			p, _ := newP()
			s := good()
			m(&s)
			e := p.Decide(lim, s, nil)
			if e.Value != 2 || e.Reasons[0] != governor.ReasonSampleFail {
				t.Fatalf("%+v", e)
			}
		})
	}
}

func TestLoadLowersImmediatelyAndProportionally(t *testing.T) {
	p, _ := newP()
	s := good()
	s.Load1 = 16 // 2x the enter threshold of 6.4 -> half of 30
	e := p.Decide(lim, s, nil)
	if e.Value != 12 || e.Reasons[0] != governor.ReasonLoad { // floor(30*6.4/16)
		t.Fatalf("%+v", e)
	}
	s.Load1 = 1e5
	if e := p.Decide(lim, s, nil); e.Value != 2 {
		t.Fatalf("extreme load must reach the floor: %+v", e)
	}
}

func TestMemoryAndDisk(t *testing.T) {
	p, _ := newP()
	s := good()
	s.MemAvailFrac = 0.12
	if e := p.Decide(lim, s, nil); e.Value != 15 || e.Reasons[0] != governor.ReasonMemory || !e.Admitting {
		t.Fatalf("%+v", e)
	}
	s.MemAvailFrac = 0.05
	e := p.Decide(lim, s, nil)
	if e.Value != 2 || e.Admitting || e.Reasons[0] != governor.ReasonMemoryStop {
		t.Fatalf("%+v", e)
	}
	p, _ = newP()
	s = good()
	s.DiskFreeBytes = 5 << 30
	if e := p.Decide(lim, s, nil); e.Value != 15 || e.Reasons[0] != governor.ReasonDisk {
		t.Fatalf("%+v", e)
	}
	p, _ = newP()
	s = good()
	s.DiskFreeFrac = 0.05
	if e := p.Decide(lim, s, nil); e.Value != 15 || e.Reasons[0] != governor.ReasonDisk {
		t.Fatalf("%+v", e)
	}
}

func TestRecoveryRampIsSlowAndNeedsConsecutiveGoodSamples(t *testing.T) {
	p, _ := newP()
	bad := good()
	bad.Load1 = 1e5
	p.Decide(lim, bad, nil) // down to 2
	var got []int
	for i := 0; i < 9; i++ {
		e := p.Decide(lim, good(), nil)
		got = append(got, e.Value)
		if e.Reasons[len(e.Reasons)-1] != governor.ReasonRecovering {
			t.Fatalf("step %d lacks recovering: %+v", i, e)
		}
	}
	// one step per RecoverySamples good samples
	if !reflect.DeepEqual(got, []int{2, 2, 3, 3, 3, 4, 4, 4, 5}) {
		t.Fatalf("ramp %v", got)
	}
	// a bad sample in the middle resets the count
	p, _ = newP()
	p.Decide(lim, bad, nil)
	p.Decide(lim, good(), nil)
	p.Decide(lim, good(), nil)
	p.Decide(lim, bad, nil)
	if e := p.Decide(lim, good(), nil); e.Value != 2 {
		t.Fatalf("%+v", e)
	}
	// failure restarts the ramp at the floor
	p, _ = newP()
	for i := 0; i < 20; i++ {
		p.Decide(lim, good(), nil)
	}
	p.Decide(lim, governor.Sample{}, errors.New("x"))
	if e := p.Decide(lim, good(), nil); e.Value != 2 {
		t.Fatalf("%+v", e)
	}
}

func TestRampReachesTheCapAndStopsThere(t *testing.T) {
	p, _ := newP()
	bad := good()
	bad.Load1 = 1e5
	p.Decide(lim, bad, nil)
	var e governor.Effective
	for i := 0; i < 28*RecoverySamples+5; i++ {
		e = p.Decide(lim, good(), nil)
	}
	if e.Value != 30 || len(e.Reasons) != 0 {
		t.Fatalf("%+v", e)
	}
}

// Hysteresis: a load that wobbles around the enter threshold must not flap the value.
func TestHysteresisHoldsInsideTheBand(t *testing.T) {
	p, _ := newP()
	s := good()
	s.Load1 = 16
	low := p.Decide(lim, s, nil).Value
	var seen = map[int]bool{}
	for i := 0; i < 200; i++ {
		// between exit (0.7*8=5.6) and enter (6.4): never clears, never raises
		s.Load1 = 5.7 + float64(i%2)*0.6
		e := p.Decide(lim, s, nil)
		seen[e.Value] = true
		if e.Value > low {
			t.Fatalf("raised inside the band at %d: %+v", i, e)
		}
		if e.Reasons[0] != governor.ReasonLoad {
			t.Fatalf("band left early: %+v", e)
		}
	}
	// below the exit threshold it clears and ramps
	s.Load1 = 5
	for i := 0; i < RecoverySamples; i++ {
		p.Decide(lim, s, nil)
	}
	if e := p.Decide(lim, s, nil); e.Value <= low {
		t.Fatalf("did not recover: %+v", e)
	}
}

func TestMemoryStopBandAndAdmitting(t *testing.T) {
	p, _ := newP()
	s := good()
	s.MemAvailFrac = 0.05
	p.Decide(lim, s, nil)
	s.MemAvailFrac = 0.09 // above stop-enter, below stop-exit 0.10: still not admitting
	if e := p.Decide(lim, s, nil); e.Admitting {
		t.Fatalf("%+v", e)
	}
	s.MemAvailFrac = 0.11
	if e := p.Decide(lim, s, nil); !e.Admitting {
		t.Fatalf("%+v", e)
	}
}

func TestCapChangesAreFollowedDownAndNeverRaiseAboveTheNewCap(t *testing.T) {
	p, _ := newP()
	p.Decide(lim, good(), nil)
	l := lim
	l.MaxConcurrent = 4
	if e := p.Decide(l, good(), nil); e.Value != 4 || e.Cap != 4 {
		t.Fatalf("%+v", e)
	}
	l.MaxConcurrent = 0
	if e := p.Decide(l, good(), nil); e.Value != 0 || e.Admitting {
		t.Fatalf("%+v", e)
	}
	l.MaxConcurrent = -3
	if e := p.Decide(l, governor.Sample{}, errors.New("x")); e.Value != 0 || e.Admitting || e.Cap != 0 {
		t.Fatalf("%+v", e)
	}
}

func TestReasonsSliceIsNotShared(t *testing.T) {
	p, _ := newP()
	s := good()
	s.Load1 = 16
	a := p.Decide(lim, s, nil)
	a.Reasons[0] = "mutated"
	if b := p.Decide(lim, s, nil); b.Reasons[0] != governor.ReasonLoad {
		t.Fatal("reasons shared between decisions")
	}
}

// Property: for random Limits and random (including hostile) samples and errors, the invariant always holds and
// the value never rises faster than one step per RecoverySamples good samples.
func TestPropertyInvariantForAnyInput(t *testing.T) {
	rng := rand.New(rand.NewSource(1155))
	floats := []float64{0, 1, -1, 0.5, 0.15, 0.08, 1e300, -1e300, math.NaN(), math.Inf(1), math.Inf(-1), math.MaxFloat64, math.SmallestNonzeroFloat64}
	ints := []int{0, 1, 2, -1, 8, 64, math.MaxInt, math.MinInt, 1 << 20}
	pickF := func() float64 {
		if rng.Intn(3) == 0 {
			return floats[rng.Intn(len(floats))]
		}
		return rng.Float64() * 2
	}
	for run := 0; run < 300; run++ {
		p, _ := newP()
		for i := 0; i < 200; i++ {
			l := governor.Limits{MaxConcurrent: ints[rng.Intn(len(ints))], SafeMin: ints[rng.Intn(len(ints))], LivePerTree: rng.Intn(50)}
			if rng.Intn(2) == 0 {
				l.MaxConcurrent = rng.Intn(100)
			}
			s := governor.Sample{Valid: rng.Intn(8) != 0, Load1: pickF() * 20, Cores: ints[rng.Intn(len(ints))], MemAvailFrac: pickF(), DiskFreeBytes: uint64(rng.Int63()), DiskFreeFrac: pickF()}
			if rng.Intn(2) == 0 {
				s.Cores = rng.Intn(64)
			}
			var err error
			if rng.Intn(6) == 0 {
				err = errors.New("x")
			}
			e := p.Decide(l, s, err)
			if l.MaxConcurrent < 1 {
				if e.Value != 0 || e.Admitting || e.Cap != 0 {
					t.Fatalf("cap<1: %+v for %+v", e, l)
				}
				continue
			}
			wd := l.WithDefaults()
			if e.Cap != l.MaxConcurrent || e.Value > e.Cap || e.Value < wd.SafeMin || wd.SafeMin < 1 {
				t.Fatalf("invariant broken: %+v limits=%+v sample=%+v err=%v", e, l, s, err)
			}
			if err != nil && e.Value != wd.SafeMin {
				t.Fatalf("error must give safe min: %+v", e)
			}
		}
	}
}

// Property: the value rises by at most one per RecoverySamples consecutive decisions (cap fixed).
func TestPropertyRaiseRateIsBounded(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	p, _ := newP()
	prev, sinceRaise := -1, 0
	for i := 0; i < 20000; i++ {
		s := good()
		s.Load1 = rng.Float64() * 20
		s.MemAvailFrac = 0.04 + rng.Float64()*0.5
		var err error
		if rng.Intn(10) == 0 {
			err = errors.New("x")
		}
		e := p.Decide(lim, s, err)
		if prev >= 0 && e.Value > prev {
			if e.Value-prev != 1 {
				t.Fatalf("jumped %d -> %d", prev, e.Value)
			}
			if sinceRaise < RecoverySamples-1 {
				t.Fatalf("raised after only %d samples", sinceRaise)
			}
			sinceRaise = 0
		} else {
			sinceRaise++
		}
		prev = e.Value
	}
}
