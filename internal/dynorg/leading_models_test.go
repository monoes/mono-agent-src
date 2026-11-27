package dynorg

import (
	"context"
	"strings"
	"testing"
)

func leadingRoster() []Model {
	m := func(id string, cost float64) Model {
		return Model{Runtime: "claude", Model: id, FullAccess: true, Read: true, CostUSD: cost}
	}
	return []Model{m("claude-haiku-5-5", 0.001), m("claude-sonnet-5-5", 0.005), m("claude-opus-5-5", 0.02)}
}

// A planner gets the strongest ready model, and never a small one, whoever
// names it.
func TestPlannerNeverRunsOnASmallModel(t *testing.T) {
	s := &Staffer{Roster: leadingRoster()}
	st, err := s.Staff(context.Background(), SpawnRequest{Brief: "plan the work", Role: "planner"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Model.Model != "claude-opus-5-5" {
		t.Errorf("planner runs on %s", st.Model.Key())
	}
	for _, f := range st.Fallbacks {
		if strings.Contains(f.Model, "haiku") {
			t.Errorf("planner may fall back to %s", f.Key())
		}
	}
	_, err = s.Staff(context.Background(), SpawnRequest{Brief: "plan the work", Role: "planner", Model: "claude-haiku-5-5"})
	if err == nil || !strings.Contains(err.Error(), "too small") {
		t.Errorf("naming a small model for a planner: %v", err)
	}
}

func TestPlannerTakesASmallModelWhenNothingElseIsReady(t *testing.T) {
	s := &Staffer{Roster: leadingRoster()[:1]}
	st, err := s.Staff(context.Background(), SpawnRequest{Brief: "plan the work", Role: "planner"})
	if err != nil || st.Model.Model != "claude-haiku-5-5" {
		t.Errorf("got %s, %v", st.Model.Key(), err)
	}
}

// A verifier is never the executor's own model under an older id.
func TestVerifierIsNotTheExecutorsReplacement(t *testing.T) {
	roster := leadingRoster()
	m, same, err := VerifierFor(&Staffer{Roster: roster[2:]}, Model{Runtime: "claude", Model: "claude-opus-4-8"})
	if err != nil || !same {
		t.Errorf("verifier %s, same %v, %v", m.Key(), same, err)
	}
}
