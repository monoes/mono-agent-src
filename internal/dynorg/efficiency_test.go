package dynorg

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestTrivialBriefNeedsNoWorker(t *testing.T) {
	s := &Staffer{Roster: []Model{opus, haiku}, Lead: opus}
	for _, brief := range []string{"fix the typo in the README", "rename the variable foo to bar"} {
		team, err := s.SuggestTeam(context.Background(), brief)
		if err != nil || len(team) > 1 {
			t.Errorf("%q -> %d workers, %v; want none or one", brief, len(team), err)
		}
	}
}

func TestThreeConcernBriefGetsThreeDistinctRoles(t *testing.T) {
	s := &Staffer{Roster: []Model{opus, haiku}, Lead: opus}
	team, err := s.SuggestTeam(context.Background(), "implement the cache layer, then write tests that verify it, and review the change for security problems")
	if err != nil {
		t.Fatal(err)
	}
	roles := map[string]bool{}
	for _, st := range team {
		roles[st.AgentType] = true
	}
	if len(team) != 3 || len(roles) != 3 {
		t.Errorf("team = %d workers, roles %v; want 3 distinct", len(team), roles)
	}
}

func TestTwoBriefsGetDifferentModels(t *testing.T) {
	s := &Staffer{Roster: []Model{opus, haiku}, Lead: opus}
	ctx := context.Background()
	hard, err := s.Staff(ctx, SpawnRequest{Brief: "implement the parser"})
	if err != nil {
		t.Fatal(err)
	}
	light, err := s.Staff(ctx, SpawnRequest{Brief: "summarize the documents about onboarding", Role: "doc-writer"})
	if err != nil {
		t.Fatal(err)
	}
	if hard.Model.Key() == light.Model.Key() || hard.Model.Key() != "claude/opus" || light.Model.Key() != "claude/haiku" {
		t.Errorf("hard = %s, light = %s; want opus for the implementation and haiku for the docs", hard.Model.Key(), light.Model.Key())
	}
	// The lead's own model is no default: with haiku leading, hard work still gets the strongest.
	s.Lead = haiku
	if st, _ := s.Staff(ctx, SpawnRequest{Brief: "implement the parser"}); st.Model.Key() != "claude/opus" {
		t.Errorf("with a cheap lead the implementation model = %s", st.Model.Key())
	}
}

func TestLightWorkGetsLowEffortWithoutJev(t *testing.T) {
	s := &Staffer{Roster: []Model{opus}, Lead: opus}
	st, _ := s.Staff(context.Background(), SpawnRequest{Brief: "investigate where the config is loaded"})
	if st.Effort != "low" {
		t.Errorf("effort = %q, want low for research", st.Effort)
	}
	if st, _ := s.Staff(context.Background(), SpawnRequest{Brief: "implement the parser"}); st.Effort != "" {
		t.Errorf("effort = %q, hard work keeps the model's default", st.Effort)
	}
}

func routerStaffer(router string, conf bool, ch Chooser) *Staffer {
	return &Staffer{Roster: []Model{opus}, Lead: opus, Router: router, Chooser: ch, Library: lib(),
		Picker: fakePicker{agents: []Candidate{{ID: "engineering-code-reviewer", Probability: 0.5}, {ID: "qa-tester", Probability: 0.4}}, agentsConf: conf}}
}

func TestRouterModes(t *testing.T) {
	ctx := context.Background()
	if _, err := routerStaffer(RouterModel, true, nil).Staff(ctx, SpawnRequest{Brief: "review it"}); err == nil || !strings.Contains(err.Error(), "choose a role") {
		t.Errorf("model router without a role: %v", err)
	}
	if st, err := routerStaffer(RouterModel, true, nil).Staff(ctx, SpawnRequest{Brief: "review it", Role: "qa-tester"}); err != nil || st.AgentType != "qa-tester" {
		t.Errorf("model router with a role: %+v, %v", st, err)
	}
	// jev asks even when pick is confident.
	ch := &fakeChooser{answers: map[string]string{"Which agent": "qa-tester"}}
	if st, err := routerStaffer(RouterJev, true, ch).Staff(ctx, SpawnRequest{Brief: "review it"}); err != nil || st.AgentType != "qa-tester" {
		t.Errorf("jev router: %+v, %v", st, err)
	}
	// auto: confident skips Jev; unsure with no Jev leaves it to the lead.
	ch = &fakeChooser{answers: map[string]string{"Which agent": "qa-tester"}}
	if st, _ := routerStaffer("", true, ch).Staff(ctx, SpawnRequest{Brief: "review it"}); st.AgentType != "engineering-code-reviewer" {
		t.Errorf("auto confident: %s", st.AgentType)
	}
	if _, err := routerStaffer(RouterAuto, false, nil).Staff(ctx, SpawnRequest{Brief: "review it"}); err == nil || !strings.Contains(err.Error(), "choose a role") {
		t.Errorf("auto unsure without Jev: %v", err)
	}
	// An unsure pick loses to the app-aware built-in role the brief names.
	if st, err := routerStaffer(RouterAuto, false, nil).Staff(ctx, SpawnRequest{Brief: "add this to my to-do list"}); err != nil || st.AgentType != "task-manager" || st.Access != ProfileWorkspace {
		t.Errorf("app-aware role: %+v, %v", st, err)
	}
}

func TestSkillsAreCappedAndRouted(t *testing.T) {
	ctx := context.Background()
	s := &Staffer{Roster: []Model{opus}, Lead: opus, Library: lib()}
	if _, err := s.Staff(ctx, SpawnRequest{Brief: "x", Skills: []string{"a", "b", "c", "d"}}); err == nil {
		t.Error("four skills must be refused")
	}
	pk := fakePicker{skills: []Candidate{{ID: "a", Probability: 0.8}, {ID: "b", Probability: 0.7}, {ID: "c", Probability: 0.1}}, skillsOK: true}
	s.Picker = pk
	if st, _ := s.Staff(ctx, SpawnRequest{Brief: "x"}); len(st.Skills) != 2 {
		t.Errorf("a far weaker third skill is left out: %+v", st.Skills)
	}
	s.Router, s.Chooser = RouterJev, &fakeChooser{answers: map[string]string{"Which skill": "none"}}
	if st, _ := s.Staff(ctx, SpawnRequest{Brief: "x"}); len(st.Skills) != 0 {
		t.Errorf("Jev said no skill: %+v", st.Skills)
	}
	s.Router, s.Picker = RouterModel, pk
	if st, _ := s.Staff(ctx, SpawnRequest{Brief: "x", Role: "coder"}); len(st.Skills) != 0 {
		t.Errorf("model router picks no skills for the lead: %+v", st.Skills)
	}
}

func TestWorkspaceProfile(t *testing.T) {
	p := workerSystemPrompt(Staff{Role: "Task Manager", Access: ProfileWorkspace}, "/w", nil, false)
	for _, want := range []string{"`task`", "`profile documents`", "`people`", "vault", "WORKFLOW", "`workflow validate`", "NEVER finalize a send", "monoagentcli hil", "Do not try to bypass"} {
		if !strings.Contains(p, want) {
			t.Errorf("workspace prompt misses %q", want)
		}
	}
	if strings.Contains(p, "Never read, send or change the user's messages") {
		t.Error("workspace replaces the no-messages rule")
	}
	if !strings.Contains(workerSystemPrompt(Staff{Role: "C", Access: ProfileCoding}, "/w", nil, false), "Never read, send or change the user's messages") {
		t.Error("other profiles keep the rule")
	}
	if !accessWithin(ProfileWorkspace, ProfileQA) || !accessWithin(ProfileWorkspace, ProfileCoding) || accessWithin(ProfileCoding, ProfileWorkspace) || accessWithin(ProfileQA, ProfileWorkspace) {
		t.Error("workspace is a superset of coding, qa and automation")
	}
	if !writes(ProfileWorkspace) || !usesBrowser(ProfileWorkspace) {
		t.Error("workspace writes and drives the browser")
	}
	for _, id := range []string{"task-manager", "doc-writer", "people-researcher", "vault-keeper", "workflow-builder", "automation-operator", "browser-operator", "org-designer", "data-analyst", "security-reviewer"} {
		r, ok := builtinByID(id)
		if !ok || len(r.Body) == 0 || len(r.Body) > 400 {
			t.Errorf("app role %s missing or too long", id)
		}
	}
	if profileForCategory("automation", "x") != ProfileWorkspace || profileForCategory("testing", "qa-tester") != ProfileQA {
		t.Error("automation defaults to workspace, qa stays qa")
	}
}

// The worker's profile section with the hooks themselves is tested in
// app_access_test.go (profileSection).
func TestProfileContextHook(t *testing.T) {
	defer func() { ProfileContext = nil }()
	if strings.Contains(workerSystemPrompt(Staff{Role: "R", Access: ProfileCoding}, "/w", nil, false)+profileSectionOf(profileRuleOf(context.Background()), ""), "Profile context") {
		t.Error("nil hooks must add nothing")
	}
	ProfileContext = func(context.Context) string { return "Profile: Work (id work)" }
	// The coder lead has the profile context in its own prompt: the extras don't repeat it.
	if p := LeadExtras(RouterAuto); strings.Contains(p, "Profile: Work") {
		t.Error("lead extras repeat the profile context")
	}
}

func TestLeadPromptEncodesEfficiency(t *testing.T) {
	p := LeadPrompt(DefaultLimits(), true)
	for _, want := range []string{"EFFICIENCY FIRST", "at most 3 skills", "cheapest model", "Pick a different model", "Reuse veterans", "NEVER finalize a send"} {
		if !strings.Contains(p, want) {
			t.Errorf("lead prompt misses %q", want)
		}
	}
	if !strings.Contains(LeadExtras(RouterModel), "spawns without a role are refused") {
		t.Error("model router note")
	}
}

type listerLib struct{ fakeLibrary }

func (listerLib) ListAgents(context.Context) map[string][]AgentInfo {
	return map[string][]AgentInfo{"testing": {{ID: "qa-tester", Description: "Tests everything"}}, "engineering": {{ID: "engineering-code-reviewer"}}}
}

func TestRosterListsAgentsHintsAndSuggestion(t *testing.T) {
	c, _ := newTestConductor(t, &execScript{}, Limits{MaxAgents: 4, MaxConcurrent: 2})
	c.cfg.Staffer.Library = listerLib{lib()}
	c.cfg.Staffer.Roster = []Model{opus, haiku}
	out, err := c.Handle(context.Background(), ToolRoster, json.RawMessage(`{"brief":"implement the cache, write tests for it and review it for security"}`))
	if err != nil {
		t.Fatal(err)
	}
	var v RosterView
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Agents["testing"]) != 1 || v.Agents["testing"][0].Description != "Tests everything" {
		t.Errorf("agents = %+v", v.Agents)
	}
	tiers := map[string]string{}
	for _, h := range v.ModelHints {
		tiers[h.Model] = h.Tier
	}
	if tiers["claude/haiku"] != "cheap" || tiers["claude/opus"] != "strong" {
		t.Errorf("hints = %+v", v.ModelHints)
	}
	if len(v.Suggested) != 3 {
		t.Errorf("suggested = %+v", v.Suggested)
	}
	if len(v.Roles) < 15 {
		t.Errorf("builtin roles = %d", len(v.Roles))
	}
}
