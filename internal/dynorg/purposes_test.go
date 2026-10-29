package dynorg

import (
	"context"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/agentpurposes"
)

func set(rule string, pairs ...string) agentpurposes.Setting {
	s := agentpurposes.Default()
	s.Rule = rule
	for _, k := range pairs {
		rt, m, _ := strings.Cut(k, "/")
		s.Pairs = append(s.Pairs, agentpurposes.Pair{Runtime: rt, Model: m})
	}
	return s
}

func staff(t *testing.T, s *Staffer, req SpawnRequest) Staff {
	t.Helper()
	st, err := s.Staff(context.Background(), req)
	if err != nil {
		t.Fatalf("%+v: %v", req, err)
	}
	return st
}

func why(st Staff) string { return strings.Join(st.Why, "; ") }

const (
	codeBrief   = "implement the parser"
	reviewBrief = "review the diff for bugs"
)

// With nothing set the usual ranking runs: writing work goes to the strongest model.
func TestNoSettingLeavesTheChoiceAlone(t *testing.T) {
	s := &Staffer{Roster: []Model{opus, haiku, gpt}, Lead: opus}
	if st := staff(t, s, SpawnRequest{Brief: codeBrief}); st.Model.Key() != "claude/opus" || st.Purpose != "coding" {
		t.Errorf("staff = %s purpose %q", st.Model.Key(), st.Purpose)
	}
}

func TestStaffModelStaysInsideTheOperatorsSet(t *testing.T) {
	s := &Staffer{Roster: []Model{opus, haiku, gpt}, Lead: opus,
		Purposes: map[string]agentpurposes.Setting{"coding": set("any", "claude/haiku", "codex/gpt-5.5")}}
	st := staff(t, s, SpawnRequest{Brief: codeBrief})
	if st.Model.Key() == "claude/opus" || st.Purpose != "coding" {
		t.Errorf("the model left the set: %s", st.Model.Key())
	}
	for _, f := range st.Fallbacks {
		if f.Key() == "claude/opus" {
			t.Errorf("a fallback is outside the set: %v", keys(st.Fallbacks))
		}
	}
	// Another purpose is not limited by it.
	if st := staff(t, s, SpawnRequest{Brief: reviewBrief}); st.Purpose != "review" {
		t.Errorf("purpose = %q", st.Purpose)
	}
}

func TestAPairOutsideTheSetIsRefusedWhenTheLeadNamesIt(t *testing.T) {
	s := &Staffer{Roster: []Model{opus, haiku, gpt}, Lead: opus,
		Purposes: map[string]agentpurposes.Setting{"coding": set("any", "claude/haiku")}}
	_, err := s.Staff(context.Background(), SpawnRequest{Brief: codeBrief, Runtime: "codex", Model: "gpt-5.5"})
	if err == nil || !strings.Contains(err.Error(), "not one of the models the operator allows") || !strings.Contains(err.Error(), "claude/haiku") {
		t.Errorf("err = %v", err)
	}
	if st := staff(t, s, SpawnRequest{Brief: codeBrief, Runtime: "claude", Model: "haiku"}); st.Model.Key() != "claude/haiku" {
		t.Errorf("a pair of the set was refused: %s", st.Model.Key())
	}
}

func TestTheLeadCannotEscapeBySayingNoModelOrAnotherPurpose(t *testing.T) {
	s := &Staffer{Roster: []Model{opus, haiku, gpt}, Lead: opus, ModelPicker: PickerLead,
		Purposes: map[string]agentpurposes.Setting{"coding": set("any", "claude/haiku")}}
	// The lead must choose, and the choice is checked against the set.
	_, err := s.Staff(context.Background(), SpawnRequest{Brief: codeBrief})
	if err == nil || !strings.Contains(err.Error(), "claude/haiku") || strings.Contains(err.Error(), "opus") {
		t.Errorf("the lead was shown models outside the set: %v", err)
	}
	if _, err := s.Staff(context.Background(), SpawnRequest{Brief: codeBrief, Purpose: "wizardry"}); err == nil || !strings.Contains(err.Error(), "unknown purpose") {
		t.Errorf("an unknown purpose: %v", err)
	}
}

func TestALeadMayNameAPurpose(t *testing.T) {
	s := &Staffer{Roster: []Model{opus, haiku, gpt}, Lead: opus,
		Purposes: map[string]agentpurposes.Setting{"review": set("any", "codex/gpt-5.5")}}
	st := staff(t, s, SpawnRequest{Brief: codeBrief, Purpose: "review"})
	if st.Purpose != "review" || st.Model.Key() != "codex/gpt-5.5" {
		t.Errorf("staff = %s purpose %q", st.Model.Key(), st.Purpose)
	}
}

func TestAStalePairIsSkipped(t *testing.T) {
	stale := haiku
	stale.Stale = true
	s := &Staffer{Roster: []Model{opus, stale, gpt}, Lead: opus,
		Purposes: map[string]agentpurposes.Setting{"review": set("order", "claude/haiku", "codex/gpt-5.5")}}
	st := staff(t, s, SpawnRequest{Brief: reviewBrief})
	if st.Model.Key() != "codex/gpt-5.5" {
		t.Errorf("a stale pair was chosen: %s", st.Model.Key())
	}
	if _, err := s.Staff(context.Background(), SpawnRequest{Brief: reviewBrief, Runtime: "claude", Model: "haiku"}); err == nil {
		t.Error("a stale pair named by the lead was accepted")
	}
}

func TestFallbackNoneFailsAndFallbackAnyUsesEveryModel(t *testing.T) {
	stale := haiku
	stale.Stale = true
	rosterOf := []Model{opus, stale, gpt}
	none := set("any", "claude/haiku")
	s := &Staffer{Roster: rosterOf, Lead: opus, Purposes: map[string]agentpurposes.Setting{"review": none}}
	_, err := s.Staff(context.Background(), SpawnRequest{Brief: reviewBrief})
	if err == nil || !strings.Contains(err.Error(), "purpose review") || !strings.Contains(err.Error(), "claude/haiku") {
		t.Errorf("fallback none: %v", err)
	}
	any := none
	any.Fallback = agentpurposes.FallbackAny
	s.Purposes["review"] = any
	st := staff(t, s, SpawnRequest{Brief: reviewBrief})
	if st.Model.Key() == "" || !strings.Contains(why(st), "operator allowed any validated model") {
		t.Errorf("fallback any: %s: %s", st.Model.Key(), why(st))
	}
}

func TestRules(t *testing.T) {
	roster := []Model{opus, haiku, gpt}
	cases := []struct {
		name  string
		set   agentpurposes.Setting
		brief string
		want  string
	}{
		{"order picks the first stated", set("order", "codex/gpt-5.5", "claude/haiku"), codeBrief, "codex/gpt-5.5"},
		{"order, the other way round", set("order", "claude/haiku", "codex/gpt-5.5"), codeBrief, "claude/haiku"},
		{"cheapest", set("cheapest", "claude/opus", "codex/gpt-5.5"), codeBrief, "codex/gpt-5.5"},
		{"any: heavy work takes the strongest of the set", set("any", "claude/haiku", "codex/gpt-5.5"), codeBrief, "codex/gpt-5.5"},
		{"any: light work takes the cheapest of the set", set("any", "claude/opus", "codex/gpt-5.5"), "write a doc about the cache", "codex/gpt-5.5"},
	}
	for _, tc := range cases {
		s := &Staffer{Roster: roster, Lead: opus, Purposes: map[string]agentpurposes.Setting{"coding": tc.set, "writing": tc.set}}
		if st := staff(t, s, SpawnRequest{Brief: tc.brief}); st.Model.Key() != tc.want {
			t.Errorf("%s: %s, want %s (%s)", tc.name, st.Model.Key(), tc.want, why(st))
		}
	}
}

func TestCheapestSkipsABadFit(t *testing.T) {
	s := &Staffer{Roster: []Model{opus, haiku}, Lead: opus,
		Purposes: map[string]agentpurposes.Setting{"coding": set("cheapest", "claude/opus", "claude/haiku")}}
	// haiku is the cheaper but has a track record that is a bad fit for engineering.
	s.Quality = track(failures(nil, haiku, "engineering", 3))
	if st := staff(t, s, SpawnRequest{Brief: codeBrief}); st.Model.Key() != "claude/opus" {
		t.Errorf("cheapest ignored the quality bar: %s", st.Model.Key())
	}
}

func TestDifferentFromAuthor(t *testing.T) {
	diff := set("any", "claude/opus", "codex/gpt-5.5")
	diff.DifferentFromAuthor = true
	two := &Staffer{Roster: []Model{opus, gpt}, Lead: opus, Purposes: map[string]agentpurposes.Setting{"review": diff}}
	// The author is the lead by default.
	if st := staff(t, two, SpawnRequest{Brief: reviewBrief}); st.Model.Key() != "codex/gpt-5.5" {
		t.Errorf("a reviewer ran on the lead's model: %s", st.Model.Key())
	}
	// The code that knows the author says so.
	if st := staff(t, two, SpawnRequest{Brief: reviewBrief, AuthorModel: "codex/gpt-5.5"}); st.Model.Key() != "claude/opus" {
		t.Errorf("a reviewer ran on the author's model: %s", st.Model.Key())
	}
	// The lead can't name the author's model either.
	if _, err := two.Staff(context.Background(), SpawnRequest{Brief: reviewBrief, AuthorModel: "claude/opus", Runtime: "claude", Model: "opus"}); err == nil || !strings.Contains(err.Error(), "different model") {
		t.Errorf("naming the author's model: %v", err)
	}
	// One validated model: it falls back to it and says so.
	one := &Staffer{Roster: []Model{opus}, Lead: opus, Purposes: map[string]agentpurposes.Setting{"review": diff}}
	st := staff(t, one, SpawnRequest{Brief: reviewBrief})
	if st.Model.Key() != "claude/opus" || !strings.Contains(why(st), "different-from-author fell back") {
		t.Errorf("one model: %s: %s", st.Model.Key(), why(st))
	}
	if st := staff(t, one, SpawnRequest{Brief: reviewBrief, Runtime: "claude", Model: "opus"}); !strings.Contains(why(st), "fell back") {
		t.Errorf("one model, named: %s", why(st))
	}
}

func TestTheVerifierDrawsFromTheReviewSet(t *testing.T) {
	s := &Staffer{Roster: []Model{opus, haiku, gpt}, Lead: opus,
		Purposes: map[string]agentpurposes.Setting{"review": set("any", "codex/gpt-5.5")}}
	if m, same := VerifierFor(s, opus); m.Key() != "codex/gpt-5.5" || same {
		t.Errorf("verifier = %s same=%v", m.Key(), same)
	}
	// Its own staffing still passes the set.
	st := staff(t, s, SpawnRequest{Brief: "verify task", Role: "reviewer", Runtime: "codex", Model: "gpt-5.5", AuthorModel: "claude/opus"})
	if st.Model.Key() != "codex/gpt-5.5" {
		t.Errorf("staff = %s", st.Model.Key())
	}
}

func TestThePurposeEffort(t *testing.T) {
	e := set("any", "claude/opus")
	e.Effort = "high"
	s := &Staffer{Roster: []Model{opus, haiku}, Lead: opus, Purposes: map[string]agentpurposes.Setting{"coding": e}}
	if st := staff(t, s, SpawnRequest{Brief: codeBrief}); st.Effort != "high" {
		t.Errorf("effort = %q", st.Effort)
	}
	if st := staff(t, s, SpawnRequest{Brief: codeBrief, Effort: "low"}); st.Effort != "low" {
		t.Errorf("the lead's effort: %q", st.Effort)
	}
}

func TestADefaultModelMatchesTheRosterId(t *testing.T) {
	s := &Staffer{Roster: []Model{opus, readOK}, Lead: opus,
		Purposes: map[string]agentpurposes.Setting{"research": set("any", "pi/default")}}
	if st := staff(t, s, SpawnRequest{Brief: "investigate where the config is loaded"}); st.Model.Key() != "pi/default" {
		t.Errorf("staff = %s", st.Model.Key())
	}
}

// The purposes ride the categories the built-in roles already carry: this
// holds the two tables together, so a role added with a new category is seen.
func TestBuiltinRoleCategoriesMapOntoPurposes(t *testing.T) {
	want := map[string]string{
		"reviewer": "review", "researcher": "research", "tester": "testing", "planner": "planning", "coder": "coding",
		"task-manager": "", "doc-writer": "writing", "people-researcher": "research", "vault-keeper": "security-audit",
		"workflow-builder": "automation", "automation-operator": "automation", "browser-operator": "automation",
		"org-designer": "planning", "data-analyst": "data", "security-reviewer": "security-audit",
	}
	if len(want) != len(builtinRoles) {
		t.Errorf("%d built-in roles, %d decided: decide the new one's purpose", len(builtinRoles), len(want))
	}
	for _, r := range builtinRoles {
		if got := agentpurposes.ForCategory(r.Category); got != want[r.ID] {
			t.Errorf("role %s (category %s) = purpose %q, want %q", r.ID, r.Category, got, want[r.ID])
		}
	}
}

func TestSpawnToolOffersThePurpose(t *testing.T) {
	for _, lead := range []bool{true, false} {
		props := spawnSpec(lead, Profiles).Schema["properties"].(map[string]any)
		if _, ok := props["purpose"]; !ok {
			t.Errorf("lead=%v: org_spawn has no purpose", lead)
		}
		if _, ok := props["author_model"]; ok {
			t.Errorf("lead=%v: org_spawn lets the lead name the author model", lead)
		}
	}
}
