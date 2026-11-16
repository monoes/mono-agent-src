package doomsday

import "testing"

var (
	pinA = Pin{Provider: "claude", Model: "opus", Version: "1"}
	pinB = Pin{Provider: "codex", Model: "gpt", Version: "2"}
)

func TestBuildRunCharter(t *testing.T) {
	c, err := BuildRunCharter(RunSpec{Goal: " ship it ", BudgetUSD: 2.5, Criteria: []string{"it works", " "}, Boss: pinA, Verifier: pinB})
	if err != nil {
		t.Fatal(err)
	}
	if c.Goal != "ship it" || c.Limits.BudgetMicro != 2500000 || c.Limits.NoCeiling || len(c.Criteria) != 1 || c.Pins.Judging != "default" {
		t.Fatalf("charter: %+v", c)
	}
	if len(c.Lines) != len(EverythingLines()) || c.Has(LinePlatformCaps) {
		t.Fatalf("lines: %v", c.Lines)
	}
	if c.Limits.TreeDepth != DefaultTreeDepth || c.Limits.MaxTotal != DefaultMaxTotal {
		t.Fatalf("limits not defaulted: %+v", c.Limits)
	}
}

func TestBuildRunCharterRefusals(t *testing.T) {
	base := RunSpec{Goal: "g", Boss: pinA, Verifier: pinB}
	for name, s := range map[string]RunSpec{
		"no budget choice": base,
		"both budgets":     {Goal: "g", Boss: pinA, Verifier: pinB, BudgetUSD: 1, NoCeiling: true},
		"empty goal":       {Boss: pinA, Verifier: pinB, BudgetUSD: 1},
		"verifier is boss": {Goal: "g", Boss: pinA, Verifier: pinA, BudgetUSD: 1},
		"unknown lift":     {Goal: "g", Boss: pinA, Verifier: pinB, BudgetUSD: 1, Lift: []Line{"nope"}},
	} {
		if _, err := BuildRunCharter(s); err == nil {
			t.Errorf("%s: no refusal", name)
		}
	}
	s := base
	s.NoCeiling = true
	if c, err := BuildRunCharter(s); err != nil || !c.Limits.NoCeiling {
		t.Fatalf("no-ceiling: %v %+v", err, c.Limits)
	}
	s.Lift = []Line{LineSpending}
	if c, _ := BuildRunCharter(s); len(c.Lines) != 1 || !c.Has(LineSpending) {
		t.Fatalf("--lift not honoured: %v", c.Lines)
	}
}

func TestParsePin(t *testing.T) {
	if p, err := ParsePin("claude/opus@4.1"); err != nil || p != (Pin{"claude", "opus", "4.1"}) {
		t.Fatalf("%v %+v", err, p)
	}
	if p, err := ParsePin("codex/gpt-5"); err != nil || p.Version != "unknown" {
		t.Fatalf("%v %+v", err, p)
	}
	for _, bad := range []string{"", "opus", "/opus", "claude/"} {
		if _, err := ParsePin(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestPickPins(t *testing.T) {
	b, v, err := PickPins([]Pin{pinA, pinA, pinB})
	if err != nil || b != pinA || v != pinB {
		t.Fatalf("%v %+v %+v", err, b, v)
	}
	if _, _, err := PickPins([]Pin{pinA, pinA}); err == nil {
		t.Fatal("one distinct model gave a verifier")
	}
	if _, _, err := PickPins(nil); err == nil {
		t.Fatal("no models accepted")
	}
}
