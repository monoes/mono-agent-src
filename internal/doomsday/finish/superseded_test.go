package finish

import "testing"

// A turn on a superseded pin runs its replacement, so the two are one model to
// every distinctness rule.
func TestSupersededPinIsItsReplacement(t *testing.T) {
	pin := func(model string) Pin {
		return Pin{Provider: "claude", Model: model, Version: "2", PolicyHash: "h"}
	}
	c := Charter{Criteria: []Criterion{{ID: "c"}}, Boss: pin("claude-opus-5"), Verifier: pin("claude-opus-5-5")}
	if c.CannotFinish() == "" {
		t.Error("a boss on a superseded id verified itself")
	}
	if !pin("claude-opus-4-8").SameModel(Pin{Provider: "claude", Model: "claude-opus-5-5"}) {
		t.Error("the replacement a turn ran on is not the pin")
	}
}
