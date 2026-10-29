package dynorg

import (
	"context"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/agentpurposes"
)

func pairs(keys ...string) []agentpurposes.Pair {
	var out []agentpurposes.Pair
	for _, k := range keys {
		p, err := agentpurposes.ParsePair(k)
		if err != nil {
			panic(err)
		}
		out = append(out, p)
	}
	return out
}

func purposeStaffer(set agentpurposes.Set, models ...Model) *Staffer {
	if len(models) == 0 {
		models = []Model{opus, haiku, gpt}
	}
	return &Staffer{Roster: models, Lead: opus, Library: lib(), Purposes: set}
}

func staffOne(t *testing.T, s *Staffer, req SpawnRequest) (Staff, error) {
	t.Helper()
	if req.Brief == "" {
		req.Brief = "implement the change"
	}
	return s.Staff(context.Background(), req)
}

// A parent's spawn picks only from the purpose's chosen pairs, whatever the ranking would have said.
func TestPurposeLimitsTheModelPicked(t *testing.T) {
	s := purposeStaffer(agentpurposes.Set{"coding": {Pairs: pairs("codex/gpt-5.5")}})
	for i := 0; i < 3; i++ { // repeated: no run of luck lets another model through
		st, err := staffOne(t, s, SpawnRequest{Role: "coder"})
		if err != nil {
			t.Fatal(err)
		}
		if st.Model.Key() != "codex/gpt-5.5" {
			t.Fatalf("picked %s, the purpose allows only codex/gpt-5.5", st.Model.Key())
		}
		for _, f := range st.Fallbacks {
			t.Errorf("the fallback list holds %s, outside the chosen set", f.Key())
		}
		if !strings.Contains(strings.Join(st.Why, ";"), "purpose coding") {
			t.Errorf("why = %v", st.Why)
		}
	}
	// A purpose nobody set, and a role whose category is no purpose, keep today's behaviour.
	st, err := staffOne(t, s, SpawnRequest{Role: "reviewer", Access: ProfileResearch})
	if err != nil || st.Model.Key() == "" {
		t.Fatalf("an unconfigured purpose: %v", err)
	}
	if got := len(st.Fallbacks); got != 2 {
		t.Errorf("unconfigured purpose: %d fallbacks, want the other two models", got)
	}
}

// A lead that names a pair outside the set is refused, with the set it may use; a pair inside is honoured.
func TestPurposeRefusesAPairOutsideTheSet(t *testing.T) {
	s := purposeStaffer(agentpurposes.Set{"coding": {Pairs: pairs("claude/haiku", "codex/gpt-5.5")}})
	_, err := staffOne(t, s, SpawnRequest{Role: "coder", Runtime: "claude", Model: "opus"})
	if err == nil || !strings.Contains(err.Error(), "coding") || !strings.Contains(err.Error(), "claude/haiku") {
		t.Fatalf("a named model outside the purpose: %v", err)
	}
	st, err := staffOne(t, s, SpawnRequest{Role: "coder", Runtime: "codex", Model: "gpt-5.5"})
	if err != nil || st.Model.Key() != "codex/gpt-5.5" {
		t.Fatalf("a named model inside the purpose: %v %+v", err, st.Model)
	}
	// Naming the model by its label does not slip past either.
	labelled := gpt
	labelled.Label = "Opus Lookalike"
	s2 := purposeStaffer(agentpurposes.Set{"coding": {Pairs: pairs("claude/haiku")}}, haiku, labelled)
	if _, err := staffOne(t, s2, SpawnRequest{Role: "coder", Model: "Opus Lookalike"}); err == nil {
		t.Fatal("a label named a model outside the set")
	}
}

// The lead can name a purpose; its set applies whatever the role's own category is.
func TestLeadCanNameAPurposeButNotAnUnknownOne(t *testing.T) {
	s := purposeStaffer(agentpurposes.Set{"reviewing": {Pairs: pairs("claude/haiku")}, "coding": {Pairs: pairs("codex/gpt-5.5")}})
	st, err := staffOne(t, s, SpawnRequest{Role: "coder", Purpose: "reviewing"})
	if err != nil || st.Model.Key() != "claude/haiku" {
		t.Fatalf("a coder started for the reviewing purpose: %v %+v", err, st.Model)
	}
	if _, err := staffOne(t, s, SpawnRequest{Role: "coder", Purpose: "world-domination"}); err == nil || !strings.Contains(err.Error(), "coding") {
		t.Fatalf("an unknown purpose: %v (the refusal should list the purposes)", err)
	}
	// Naming a purpose that nobody configured restricts nothing.
	if _, err := staffOne(t, s, SpawnRequest{Role: "coder", Purpose: "design"}); err != nil {
		t.Fatalf("a purpose with nothing chosen: %v", err)
	}
}

// A chosen pair that is not on the validated roster cannot be used: the spawn fails with the reason, or falls back
// only when the operator allowed it.
func TestPurposeWithNoUsablePairFailsUnlessTheOperatorAllowedFallback(t *testing.T) {
	set := agentpurposes.Set{"coding": {Pairs: pairs("claude/ghost")}} // not validated
	_, err := staffOne(t, purposeStaffer(set), SpawnRequest{Role: "coder"})
	if err == nil || !strings.Contains(err.Error(), "claude/ghost") {
		t.Fatalf("an unvalidated chosen pair: %v", err)
	}
	set["coding"] = agentpurposes.Settings{Pairs: pairs("claude/ghost"), Fallback: true}
	st, err := staffOne(t, purposeStaffer(set), SpawnRequest{Role: "coder"})
	if err != nil || st.Model.Key() == "" {
		t.Fatalf("with fallback allowed: %v", err)
	}
	if !strings.Contains(strings.Join(st.Why, ";"), "fall") {
		t.Errorf("the fallback must be recorded: %v", st.Why)
	}
	// A pair the worker cannot run (no write access for a coding worker) is not usable either.
	set = agentpurposes.Set{"coding": {Pairs: pairs("pi")}}
	if _, err := staffOne(t, purposeStaffer(set, opus, readOK), SpawnRequest{Role: "coder"}); err == nil {
		t.Fatal("a read-only model was picked for a coding worker")
	}
}

func TestPurposeRules(t *testing.T) {
	// order: the first of the operator's pairs that can run the worker, even when another ranks higher.
	st, err := staffOne(t, purposeStaffer(agentpurposes.Set{"coding": {Pairs: pairs("codex/gpt-5.5", "claude/opus"), Rule: agentpurposes.RuleOrder}}), SpawnRequest{Role: "coder"})
	if err != nil || st.Model.Key() != "codex/gpt-5.5" {
		t.Fatalf("order: %v %s", err, st.Model.Key())
	}
	st, err = staffOne(t, purposeStaffer(agentpurposes.Set{"coding": {Pairs: pairs("claude/opus", "codex/gpt-5.5"), Rule: agentpurposes.RuleOrder}}), SpawnRequest{Role: "coder"})
	if err != nil || st.Model.Key() != "claude/opus" {
		t.Fatalf("order, other order: %v %s", err, st.Model.Key())
	}
	// order: skips a pair that is not usable and takes the next.
	st, err = staffOne(t, purposeStaffer(agentpurposes.Set{"coding": {Pairs: pairs("claude/ghost", "codex/gpt-5.5"), Rule: agentpurposes.RuleOrder}}), SpawnRequest{Role: "coder"})
	if err != nil || st.Model.Key() != "codex/gpt-5.5" {
		t.Fatalf("order with an unusable first pair: %v %s", err, st.Model.Key())
	}
	// cheapest: the lowest validation cost among the chosen pairs.
	st, err = staffOne(t, purposeStaffer(agentpurposes.Set{"coding": {Pairs: pairs("claude/opus", "codex/gpt-5.5", "claude/haiku"), Rule: agentpurposes.RuleCheapest}}), SpawnRequest{Role: "coder"})
	if err != nil || st.Model.Key() != "claude/haiku" {
		t.Fatalf("cheapest: %v %s", err, st.Model.Key())
	}
	st, err = staffOne(t, purposeStaffer(agentpurposes.Set{"coding": {Pairs: pairs("claude/opus", "codex/gpt-5.5"), Rule: agentpurposes.RuleCheapest}}), SpawnRequest{Role: "coder"})
	if err != nil || st.Model.Key() != "codex/gpt-5.5" {
		t.Fatalf("cheapest within the set: %v %s", err, st.Model.Key())
	}
}

// Reviewing, verifying and judging can be kept from the model that wrote the work.
func TestPurposeDifferentFromAuthor(t *testing.T) {
	set := agentpurposes.Set{"reviewing": {Pairs: pairs("claude/opus", "claude/haiku"), DifferentFromAuthor: true}}
	st, err := staffOne(t, purposeStaffer(set), SpawnRequest{Role: "reviewer", authorKey: "claude/opus"})
	if err != nil || st.Model.Key() != "claude/haiku" {
		t.Fatalf("the author's model must not review: %v %s", err, st.Model.Key())
	}
	// Only the author's model is available: refused, fallback or not.
	only := agentpurposes.Set{"reviewing": {Pairs: pairs("claude/opus"), DifferentFromAuthor: true, Fallback: true}}
	if _, err := staffOne(t, purposeStaffer(only, opus), SpawnRequest{Role: "reviewer", authorKey: "claude/opus"}); err == nil {
		t.Fatal("the author's model reviewed its own work")
	}
	// The lead cannot name the author's model either.
	if _, err := staffOne(t, purposeStaffer(set), SpawnRequest{Role: "reviewer", Runtime: "claude", Model: "opus", authorKey: "claude/opus"}); err == nil {
		t.Fatal("the lead named the author's model")
	}
	// Without the flag the author may be picked; without an author nothing is excluded.
	free := agentpurposes.Set{"reviewing": {Pairs: pairs("claude/opus")}}
	if st, err := staffOne(t, purposeStaffer(free), SpawnRequest{Role: "reviewer", authorKey: "claude/opus"}); err != nil || st.Model.Key() != "claude/opus" {
		t.Fatalf("flag off: %v", err)
	}
	if st, err := staffOne(t, purposeStaffer(set), SpawnRequest{Role: "reviewer"}); err != nil || st.Model.Key() == "" {
		t.Fatalf("no author known: %v", err)
	}
}

func TestPurposeEffort(t *testing.T) {
	set := agentpurposes.Set{"coding": {Pairs: pairs("claude/opus"), Effort: "high"}}
	st, err := staffOne(t, purposeStaffer(set), SpawnRequest{Role: "coder"})
	if err != nil || st.Effort != "high" {
		t.Fatalf("the purpose's effort: %v %q", err, st.Effort)
	}
	if st, err = staffOne(t, purposeStaffer(set), SpawnRequest{Role: "coder", Effort: "low"}); err != nil || st.Effort != "low" {
		t.Fatalf("the lead's own effort wins: %v %q", err, st.Effort)
	}
	// A model without that level is simply left on its own effort.
	set = agentpurposes.Set{"coding": {Pairs: pairs("claude/haiku"), Effort: "high"}}
	if st, err = staffOne(t, purposeStaffer(set), SpawnRequest{Role: "coder"}); err != nil || st.Effort == "high" {
		t.Fatalf("an effort the model lacks: %v %q", err, st.Effort)
	}
}

// With no purposes, staffing is what it was: the existing tests cover it; this pins that a nil set restricts nothing.
func TestNoPurposesRestrictNothing(t *testing.T) {
	for _, set := range []agentpurposes.Set{nil, {}, {"coding": {}}} {
		st, err := staffOne(t, purposeStaffer(set), SpawnRequest{Role: "coder", Runtime: "claude", Model: "opus"})
		if err != nil || st.Model.Key() != "claude/opus" {
			t.Errorf("set %v: %v %s", set, err, st.Model.Key())
		}
	}
}
