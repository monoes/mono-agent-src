package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// onboardingTestApp is an app with a state file of its own and the given
// profile selected in memory ("" selects none).
func onboardingTestApp(t *testing.T, profileID string) *App {
	t.Helper()
	a := &App{dbPath: filepath.Join(t.TempDir(), "monoagent.db")}
	if profileID != "" {
		a.selectForTest(profileID)
	}
	return a
}

func onboardingMarksOf(t *testing.T, a *App) *onboardingMarks {
	t.Helper()
	return loadGuiState(guiStatePath(a.dbPath)).Onboarding
}

func assertNoStateFile(t *testing.T, a *App) {
	t.Helper()
	if _, err := os.Stat(guiStatePath(a.dbPath)); !os.IsNotExist(err) {
		t.Fatalf("a state file was written: %v", err)
	}
}

func TestSkip_RefusesUnskippable(t *testing.T) {
	a := onboardingTestApp(t, "work")
	for _, id := range []string{"welcome", "profile", "finish", "nope", ""} {
		if err := a.SkipOnboardingStep(id); err == nil {
			t.Errorf("SkipOnboardingStep(%q) = nil, want an error", id)
		}
	}
	assertNoStateFile(t, a)
}

func TestMark_RefusesStepsThatAreNotMarked(t *testing.T) {
	a := onboardingTestApp(t, "work")
	for _, id := range []string{"profile", "monomind", "agents", "browser", "finish", "nope", ""} {
		if err := a.MarkOnboardingStep(id); err == nil {
			t.Errorf("MarkOnboardingStep(%q) = nil, want an error", id)
		}
	}
	assertNoStateFile(t, a)
}

func TestMarkAndSkipWriteTheirOwnScope(t *testing.T) {
	a := onboardingTestApp(t, "work")
	for _, id := range []string{"welcome", "system", "permissions", "firstRun", "about"} {
		if err := a.MarkOnboardingStep(id); err != nil {
			t.Fatalf("MarkOnboardingStep(%q): %v", id, err)
		}
	}
	for _, id := range []string{"agents", "agents", "browser", "browser", "monomind", "about"} {
		if err := a.SkipOnboardingStep(id); err != nil {
			t.Fatalf("SkipOnboardingStep(%q): %v", id, err)
		}
	}
	m := onboardingMarksOf(t, a)
	if m == nil || !m.Seen["welcome"] || !m.Seen["system"] || !m.Seen["permissions"] || !m.Seen["firstRun"] || m.Seen["about"] {
		t.Fatalf("seen = %+v", m)
	}
	if len(m.Skipped) != 1 || m.Skipped[0] != "agents" {
		t.Fatalf("machine-wide skipped = %v, want [agents]", m.Skipped)
	}
	pm := m.Profiles["work"]
	if !pm.AboutDone || len(pm.Skipped) != 3 {
		t.Fatalf("work's marks = %+v, want about done and browser, monomind, about skipped once each", pm)
	}
	if m.Completed {
		t.Fatal("marking steps completed the onboarding")
	}
	if err := a.CompleteOnboarding(); err != nil {
		t.Fatal(err)
	}
	if m := onboardingMarksOf(t, a); m == nil || !m.Completed || !m.Seen["welcome"] {
		t.Fatalf("after CompleteOnboarding: %+v", m)
	}
}

func TestPerProfileMarksNeedAProfile(t *testing.T) {
	a := onboardingTestApp(t, "")
	if err := a.MarkOnboardingStep("about"); !errors.Is(err, errNoProfile) {
		t.Errorf("MarkOnboardingStep(about) = %v, want errNoProfile", err)
	}
	for _, id := range []string{"about", "monomind", "browser"} {
		if err := a.SkipOnboardingStep(id); !errors.Is(err, errNoProfile) {
			t.Errorf("SkipOnboardingStep(%q) = %v, want errNoProfile", id, err)
		}
	}
	assertNoStateFile(t, a)
	// Machine-wide marks need none.
	if err := a.MarkOnboardingStep("welcome"); err != nil {
		t.Fatal(err)
	}
	if err := a.SkipOnboardingStep("system"); err != nil {
		t.Fatal(err)
	}
}

func TestRestartOnboardingKeepsRealState(t *testing.T) {
	a := onboardingTestApp(t, "work")
	if err := a.setSelectedProfile("work"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"welcome", "system", "about"} {
		if err := a.MarkOnboardingStep(id); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"agents", "browser"} {
		if err := a.SkipOnboardingStep(id); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.CompleteOnboarding(); err != nil {
		t.Fatal(err)
	}
	if err := a.RestartOnboarding(); err != nil {
		t.Fatal(err)
	}
	st := loadGuiState(guiStatePath(a.dbPath))
	m := st.Onboarding
	if m == nil {
		t.Fatal("restart removed the marks: an install with profiles would complete itself again")
	}
	if m.Completed || m.Seen["welcome"] || len(m.Skipped) != 0 || len(m.Profiles["work"].Skipped) != 0 {
		t.Fatalf("after restart: %+v", m)
	}
	if !m.Seen["system"] || !m.Profiles["work"].AboutDone || st.SelectedProfile != "work" {
		t.Fatalf("restart dropped real state: %+v (selected %q)", m, st.SelectedProfile)
	}
	// What the window then shows: Welcome again, though a profile exists.
	s, err := a.onboardingStateFor(onboardingFacts{ProfileID: "work", ProfileCount: 1}, true)
	if err != nil || s.Completed || s.CurrentStep != "welcome" {
		t.Fatalf("state after restart = %+v, %v", s, err)
	}
}

// An install that has profiles but no marks predates the onboarding: it is
// reported completed and the mark is written once, so a later restart sticks.
func TestOnboardingStateMarksAnExistingInstallOnce(t *testing.T) {
	a := onboardingTestApp(t, "work")
	s, err := a.onboardingStateFor(onboardingFacts{ProfileID: "work", ProfileCount: 1}, true)
	if err != nil || !s.Completed {
		t.Fatalf("state = %+v, %v", s, err)
	}
	if m := onboardingMarksOf(t, a); m == nil || !m.Completed {
		t.Fatalf("completed mark not written: %+v", m)
	}
	if err := a.RestartOnboarding(); err != nil {
		t.Fatal(err)
	}
	if s, err := a.onboardingStateFor(onboardingFacts{ProfileID: "work", ProfileCount: 1}, true); err != nil || s.Completed {
		t.Fatalf("the mark was written again after a restart: %+v, %v", s, err)
	}
}

// A fresh install (a successful read of zero profiles) writes nothing.
func TestOnboardingStateFreshInstallWritesNothing(t *testing.T) {
	a := onboardingTestApp(t, "")
	if s, err := a.onboardingStateFor(onboardingFacts{}, true); err != nil || s.Completed || s.CurrentStep != "welcome" {
		t.Fatalf("fresh install state = %+v, %v", s, err)
	}
	assertNoStateFile(t, a)
}

// An unread profile count is never zero profiles. With no marks stored it is
// an error and nothing is written, whatever the guess.
func TestOnboardingStateUnknownCountWithoutMarksIsAnError(t *testing.T) {
	for _, f := range []onboardingFacts{{}, {ProfileID: "work", ProfileCount: 1}} {
		a := onboardingTestApp(t, f.ProfileID)
		if s, err := a.onboardingStateFor(f, false); !errors.Is(err, errOnboardingUnknown) || s != nil {
			t.Fatalf("facts %+v: state = %+v, %v; want errOnboardingUnknown", f, s, err)
		}
		assertNoStateFile(t, a)
	}
}

// With marks stored, an unread count returns the stored Completed as is: a
// completed onboarding stays completed, an unfinished one stays open, and
// nothing is written.
func TestOnboardingStateUnknownCountKeepsStoredCompleted(t *testing.T) {
	for _, completed := range []bool{true, false} {
		a := onboardingTestApp(t, "")
		path := guiStatePath(a.dbPath)
		if err := updateGuiState(path, func(st *guiState) {
			st.Onboarding = &onboardingMarks{Completed: completed, Seen: map[string]bool{"welcome": true}}
		}); err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(path)
		s, err := a.onboardingStateFor(onboardingFacts{}, false)
		if err != nil || s.Completed != completed {
			t.Fatalf("stored completed %v: state = %+v, %v", completed, s, err)
		}
		if after, _ := os.ReadFile(path); string(after) != string(before) {
			t.Fatalf("the state file changed: %q -> %q", before, after)
		}
	}
}
