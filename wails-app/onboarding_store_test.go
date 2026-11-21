package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func stateFileForTest(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), guiStateFile)
}

func TestSavingSelectionKeepsOnboardingMarks(t *testing.T) {
	path := stateFileForTest(t)
	if err := updateGuiState(path, func(st *guiState) {
		st.Onboarding = &onboardingMarks{Completed: true}
	}); err != nil {
		t.Fatal(err)
	}
	if err := saveSelectedProfile(path, "work"); err != nil {
		t.Fatal(err)
	}
	st := loadGuiState(path)
	if st.SelectedProfile != "work" {
		t.Fatalf("SelectedProfile = %q, want work", st.SelectedProfile)
	}
	if st.Onboarding == nil || !st.Onboarding.Completed {
		t.Fatalf("onboarding marks lost: %+v", st.Onboarding)
	}
}

func TestSavingMarksKeepsSelection(t *testing.T) {
	path := stateFileForTest(t)
	if err := saveSelectedProfile(path, "work"); err != nil {
		t.Fatal(err)
	}
	if err := updateGuiState(path, func(st *guiState) {
		st.Onboarding = &onboardingMarks{
			Seen:     map[string]bool{"welcome": true},
			Skipped:  []string{"system"},
			Profiles: map[string]onboardingProfileMarks{"work": {Skipped: []string{"browser"}, AboutDone: true}},
		}
	}); err != nil {
		t.Fatal(err)
	}
	st := loadGuiState(path)
	if st.SelectedProfile != "work" {
		t.Fatalf("selection lost: %q", st.SelectedProfile)
	}
	if got := loadSelectedProfile(path); got != "work" {
		t.Fatalf("loadSelectedProfile = %q", got)
	}
	m := st.Onboarding
	if m == nil || !m.Seen["welcome"] || len(m.Skipped) != 1 || m.Skipped[0] != "system" ||
		!m.Profiles["work"].AboutDone || len(m.Profiles["work"].Skipped) != 1 {
		t.Fatalf("marks = %+v", m)
	}
}

func TestOldStateFileLoads(t *testing.T) {
	path := stateFileForTest(t)
	if err := os.WriteFile(path, []byte(`{"selected_profile":"work"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st := loadGuiState(path)
	if st.SelectedProfile != "work" || st.Onboarding != nil {
		t.Fatalf("loadGuiState = %+v", st)
	}
}

func TestDamagedStateFileIsEmpty(t *testing.T) {
	path := stateFileForTest(t)
	if err := os.WriteFile(path, []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	if st := loadGuiState(path); st.SelectedProfile != "" || st.Onboarding != nil {
		t.Fatalf("damaged file loaded as %+v", st)
	}
	if err := updateGuiState(path, func(st *guiState) {
		st.Onboarding = &onboardingMarks{Completed: true}
	}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var back guiState
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("file after update is not valid JSON: %q (%v)", b, err)
	}
	if back.Onboarding == nil || !back.Onboarding.Completed {
		t.Fatalf("file after update = %q", b)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want 0600", st.Mode().Perm())
	}
}

// Concurrent writers (Wails runs each binding on its own goroutine) never
// lose each other's change.
func TestConcurrentStateUpdatesKeepEveryMark(t *testing.T) {
	path := stateFileForTest(t)
	const n = 20
	done := make(chan error, n)
	for i := 0; i < n; i++ {
		id := string(rune('a' + i))
		go func() {
			done <- updateGuiState(path, func(st *guiState) {
				m := st.ensureOnboarding()
				m.Seen[id] = true
			})
		}()
	}
	for i := 0; i < n; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if m := loadGuiState(path).Onboarding; m == nil || len(m.Seen) != n {
		t.Fatalf("marks after %d concurrent updates: %+v", n, m)
	}
}
