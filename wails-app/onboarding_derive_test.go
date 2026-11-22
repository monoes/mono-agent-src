package main

import (
	"reflect"
	"testing"
)

func stepStatuses(s OnboardingState) map[string]string {
	out := map[string]string{}
	for _, st := range s.Steps {
		out[st.ID] = st.Status
	}
	return out
}

func stepIDs(s OnboardingState) []string {
	ids := make([]string, 0, len(s.Steps))
	for _, st := range s.Steps {
		ids = append(ids, st.ID)
	}
	return ids
}

var wantStepIDs = []string{"welcome", "system", "profile", "about", "monomind", "agents", "browser", "permissions", "firstRun", "finish"}

func TestDerive_FreshInstallStartsAtWelcome(t *testing.T) {
	s := deriveOnboarding(nil, onboardingFacts{})
	if s.Completed || s.CurrentStep != "welcome" || s.ProfileID != "" {
		t.Fatalf("state = %+v", s)
	}
	if got := stepIDs(s); !reflect.DeepEqual(got, wantStepIDs) {
		t.Fatalf("step ids = %v, want %v", got, wantStepIDs)
	}
	for _, st := range s.Steps {
		if st.Status != "todo" {
			t.Fatalf("step %s = %s, want todo", st.ID, st.Status)
		}
	}
}

func TestDerive_ExistingInstallIsCompleted(t *testing.T) {
	s := deriveOnboarding(nil, onboardingFacts{ProfileID: "work", ProfileCount: 2})
	if !s.Completed {
		t.Fatalf("an install with profiles and no marks is not completed: %+v", s)
	}
	got := stepStatuses(s)
	for _, id := range []string{"welcome", "system", "profile", "permissions", "firstRun", "finish"} {
		if got[id] != "done" {
			t.Fatalf("statuses = %v, want %s done", got, id)
		}
	}
	if s.CurrentStep != "about" {
		t.Fatalf("CurrentStep = %q, want the first real gap (about)", s.CurrentStep)
	}
}

func TestDerive_ResumesAtFirstTodo(t *testing.T) {
	m := &onboardingMarks{
		Seen:     map[string]bool{"welcome": true, "system": true},
		Profiles: map[string]onboardingProfileMarks{"work": {Skipped: []string{"about"}}},
	}
	s := deriveOnboarding(m, onboardingFacts{ProfileID: "work", ProfileCount: 1})
	got := stepStatuses(s)
	if s.Completed || s.CurrentStep != "monomind" || got["about"] != "skipped" || got["profile"] != "done" ||
		got["welcome"] != "done" || got["system"] != "done" || got["monomind"] != "todo" {
		t.Fatalf("state = %+v", s)
	}
}

func TestDerive_RealStateBeatsSkip(t *testing.T) {
	m := &onboardingMarks{
		Skipped:  []string{"agents"},
		Profiles: map[string]onboardingProfileMarks{"work": {Skipped: []string{"monomind", "browser"}}},
	}
	f := onboardingFacts{ProfileID: "work", ProfileCount: 1, MonomindOn: true, AgentUsable: true, BrowserAssigned: true}
	got := stepStatuses(deriveOnboarding(m, f))
	for _, id := range []string{"monomind", "agents", "browser"} {
		if got[id] != "done" {
			t.Errorf("%s = %s, want done", id, got[id])
		}
	}
}

func TestDerive_CompletedButNoProfilesReopensAtProfile(t *testing.T) {
	m := &onboardingMarks{Completed: true, Seen: map[string]bool{"welcome": true}}
	s := deriveOnboarding(m, onboardingFacts{})
	if s.Completed || s.CurrentStep != "profile" {
		t.Fatalf("state = %+v", s)
	}
	// Never seen Welcome: it opens there.
	s = deriveOnboarding(&onboardingMarks{Completed: true}, onboardingFacts{})
	if s.Completed || s.CurrentStep != "welcome" {
		t.Fatalf("state without welcome seen = %+v", s)
	}
}

// A fresh install that restarts after Welcome resumes at System, not past it.
func TestDerive_NoProfileYetResumesAtSystem(t *testing.T) {
	m := &onboardingMarks{Seen: map[string]bool{"welcome": true}}
	if s := deriveOnboarding(m, onboardingFacts{}); s.Completed || s.CurrentStep != "system" {
		t.Fatalf("state = %+v", s)
	}
	m.Skipped = []string{"system"}
	if s := deriveOnboarding(m, onboardingFacts{}); s.CurrentStep != "profile" || stepStatuses(s)["system"] != "skipped" {
		t.Fatalf("state with system skipped = %+v", s)
	}
}

func TestDerive_PerProfileMarksFollowTheSelectedProfile(t *testing.T) {
	m := &onboardingMarks{Profiles: map[string]onboardingProfileMarks{"a": {AboutDone: true, Skipped: []string{"browser"}}}}
	got := stepStatuses(deriveOnboarding(m, onboardingFacts{ProfileID: "b", ProfileCount: 2}))
	if got["about"] != "todo" || got["browser"] != "todo" {
		t.Fatalf("statuses for b = %v", got)
	}
	got = stepStatuses(deriveOnboarding(m, onboardingFacts{ProfileID: "a", ProfileCount: 2}))
	if got["about"] != "done" || got["browser"] != "skipped" {
		t.Fatalf("statuses for a = %v", got)
	}
}

func TestDerive_AllDoneCurrentStepIsFinish(t *testing.T) {
	m := &onboardingMarks{
		Completed: true,
		Seen:      map[string]bool{"welcome": true, "system": true, "permissions": true, "firstRun": true},
		Profiles:  map[string]onboardingProfileMarks{"w": {AboutDone: true}},
	}
	f := onboardingFacts{ProfileID: "w", ProfileCount: 1, MonomindOn: true, AgentUsable: true, BrowserAssigned: true}
	s := deriveOnboarding(m, f)
	if !s.Completed || s.CurrentStep != "finish" {
		t.Fatalf("state = %+v", s)
	}
	for _, st := range s.Steps {
		if st.Status != "done" {
			t.Fatalf("step %s = %s", st.ID, st.Status)
		}
	}
	// Finish itself not yet done: it is the current step too.
	m.Completed = false
	if s := deriveOnboarding(m, f); s.Completed || s.CurrentStep != "finish" || stepStatuses(s)["finish"] != "todo" {
		t.Fatalf("state before finishing = %+v", s)
	}
}
