package sdconfine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/missionpursuit/verifier"
)

type canaryCalls struct {
	n     int
	heavy []bool
	conf  verifier.Confiner
	base  string
	out   func(heavy bool) []verifier.CanaryResult
}

func (cc *canaryCalls) fn(_ context.Context, conf verifier.Confiner, base string, o verifier.CanaryOptions) []verifier.CanaryResult {
	cc.n++
	cc.heavy = append(cc.heavy, !o.SkipHeavy)
	cc.conf, cc.base = conf, base
	if cc.out != nil {
		return cc.out(!o.SkipHeavy)
	}
	rs := []verifier.CanaryResult{{ID: "C0", Verdict: verifier.CanaryHeld}, {ID: "C4", Verdict: verifier.CanaryHeld}}
	if !o.SkipHeavy {
		rs = append(rs, verifier.CanaryResult{ID: "C8", Heavy: true, Verdict: verifier.CanaryHeld})
	}
	return rs
}

func TestSelfTestRunsTheLightCanariesEveryTimeAndTheHeavyOnesOncePerTenMinutes(t *testing.T) {
	f := newFakeSys()
	c := newConfiner(t, f, Config{})
	cc := &canaryCalls{}
	c.canaries = cc.fn
	ctx := context.Background()
	for i, want := range []bool{true, false, false} {
		if err := c.SelfTest(ctx); err != nil {
			t.Fatal(err)
		}
		if cc.heavy[i] != want {
			t.Fatalf("self test %d: heavy = %v, want %v", i, cc.heavy[i], want)
		}
		f.sleep(4 * time.Minute)
	}
	// 12 minutes after the proof: heavy again
	if err := c.SelfTest(ctx); err != nil || !cc.heavy[3] {
		t.Fatalf("%v %v", err, cc.heavy)
	}
	if cc.conf != verifier.Confiner(c) || cc.base != c.cfg.ScratchBase {
		t.Fatalf("the canaries ran through %v on %q, not through the confiner and its scratch base", cc.conf, cc.base)
	}
	// the cached proof is keyed on the property list and the systemd version: a different one is not covered by it
	f.sleep(time.Minute)
	c.version = 263
	cc.heavy = nil
	if err := c.SelfTest(ctx); err != nil || !cc.heavy[0] {
		t.Fatalf("a new systemd version reused the old proof: %v %v", err, cc.heavy)
	}
	// and copies made by Hide share it
	cc.heavy = nil
	if err := c.Hide("/srv/p").SelfTest(ctx); err != nil || cc.heavy[0] {
		t.Fatalf("a per-check copy lost the proof: %v %v", err, cc.heavy)
	}
}

func TestABrokenCanaryFailsTheSelfTestByNameAndNeverCachesTheHeavyProof(t *testing.T) {
	f := newFakeSys()
	c := newConfiner(t, f, Config{})
	cc := &canaryCalls{out: func(heavy bool) []verifier.CanaryResult {
		rs := []verifier.CanaryResult{{ID: "C0", Verdict: verifier.CanaryHeld}, {ID: "C4", Name: "secrets by path", Verdict: verifier.CanaryBroken, Detail: "the program could read it"}}
		if heavy {
			rs = append(rs, verifier.CanaryResult{ID: "C8", Heavy: true, Verdict: verifier.CanaryHeld})
		}
		return rs
	}}
	c.canaries = cc.fn
	err := c.SelfTest(context.Background())
	if err == nil || !strings.Contains(err.Error(), "C4") {
		t.Fatalf("%v", err)
	}
	if low := strings.ToLower(err.Error()); strings.Contains(low, "unconfined") {
		t.Fatalf("the refusal must say which canary, never 'unconfined': %v", err)
	}
	cc.out = nil
	if err := c.SelfTest(context.Background()); err != nil || !cc.heavy[1] {
		t.Fatalf("a failed self test cached a heavy proof: %v %v", err, cc.heavy)
	}
	// a heavy failure after a good light run is a failure too, and drops the proof
	cc.out = func(heavy bool) []verifier.CanaryResult {
		if heavy {
			return []verifier.CanaryResult{{ID: "C8", Heavy: true, Verdict: verifier.CanaryBroken}}
		}
		return []verifier.CanaryResult{{ID: "C0", Verdict: verifier.CanaryHeld}}
	}
	f.sleep(time.Hour)
	if err := c.SelfTest(context.Background()); err == nil || !strings.Contains(err.Error(), "C8") {
		t.Fatalf("%v", err)
	}
	cc.out = nil
	if err := c.SelfTest(context.Background()); err != nil || !cc.heavy[len(cc.heavy)-1] {
		t.Fatalf("%v %v", err, cc.heavy)
	}
}

func TestNoCanaryResultsOrANotRunOneIsNeverAPass(t *testing.T) {
	f := newFakeSys()
	c := newConfiner(t, f, Config{})
	cc := &canaryCalls{out: func(bool) []verifier.CanaryResult { return nil }}
	c.canaries = cc.fn
	if err := c.SelfTest(context.Background()); err == nil {
		t.Fatal("a self test that ran nothing passed")
	}
	cc.out = func(bool) []verifier.CanaryResult {
		return []verifier.CanaryResult{{ID: "C0", Verdict: verifier.CanaryHeld}, {ID: "C8", Verdict: verifier.CanaryNotRun}}
	}
	if err := c.SelfTest(context.Background()); err == nil {
		t.Fatal("a canary that could not run passed")
	}
}

func TestACancelledSelfTestRunsNothingAndReturnsTheContextError(t *testing.T) {
	c := newConfiner(t, newFakeSys(), Config{})
	cc := &canaryCalls{}
	c.canaries = cc.fn
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.SelfTest(ctx); !errors.Is(err, context.Canceled) || cc.n != 0 {
		t.Fatalf("%v, %d runs", err, cc.n)
	}
}
