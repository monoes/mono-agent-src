package verifier

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/monoes/mono-agent/internal/missionpursuit/judgecost"
	"github.com/monoes/mono-agent/internal/monomind"
)

// Revert proof: charging an unknown, NaN, negative, infinite or absurd cost as 0 (or as the number) turns a row red.
func TestChargeForNeverMakesAnUnknownCostFree(t *testing.T) {
	const capUSD = 0.5
	capMicro := int64(500000)
	for _, c := range []struct {
		name    string
		usd     float64
		known   bool
		micro   int64
		charged bool
	}{
		{"unknown", 0, false, capMicro, false},
		{"unknown with a number", 0.01, false, capMicro, false},
		{"NaN", math.NaN(), true, capMicro, false},
		{"negative", -1, true, capMicro, false},
		{"+Inf", math.Inf(1), true, capMicro, false},
		{"-Inf", math.Inf(-1), true, capMicro, false},
		{"absurd", 1e9, true, capMicro, false},
		{"just above absurd", capUSD*absurdFactor + 0.01, true, capMicro, false},
		{"reported zero", 0, true, 0, true},
		{"reported small, rounded up", 0.0000011, true, 2, true},
		{"reported", 0.2, true, 200000, true},
		{"over the cap but believable", 0.7, true, 700000, true},
	} {
		micro, charged := ChargeFor(c.usd, c.known, capUSD)
		if micro != c.micro || charged != c.charged {
			t.Errorf("%s: %d %v, want %d %v", c.name, micro, charged, c.micro, c.charged)
		}
	}
}

func TestCostingRemembersWhatTheRuntimeSaidEvenWhenTheTurnFailed(t *testing.T) {
	c := &costing{inner: func(context.Context, monomind.ExecOptions, func(monomind.Event)) (*monomind.TurnResult, error) {
		return &monomind.TurnResult{CostUSD: 0.12, HasCostUSD: true}, errors.New("turn failed")
	}}
	if _, err := c.exec(context.Background(), monomind.ExecOptions{}, nil); err == nil {
		t.Fatal("the error is passed on")
	}
	if m, ok := c.charge(0.5); m != 120000 || !ok {
		t.Fatalf("%d %v", m, ok)
	}
	// a turn that reported nothing is charged at the cap
	u := &costing{inner: func(context.Context, monomind.ExecOptions, func(monomind.Event)) (*monomind.TurnResult, error) {
		return nil, errors.New("gone")
	}}
	_, _ = u.exec(context.Background(), monomind.ExecOptions{}, nil)
	if m, ok := u.charge(0.5); m != 500000 || ok {
		t.Fatalf("%d %v", m, ok)
	}
	// no turn was started: nothing was spent
	n := &costing{inner: nil}
	if m, ok := n.charge(0.5); m != 0 || !ok {
		t.Fatalf("%d %v", m, ok)
	}
}

func TestTheCallCapIsTheJudgesCap(t *testing.T) {
	if DefaultCallCapUSD != judgecost.PerCallCapUSD {
		t.Fatalf("%v != %v", DefaultCallCapUSD, judgecost.PerCallCapUSD)
	}
}
