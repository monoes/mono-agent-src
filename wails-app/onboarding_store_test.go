package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
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

// unreadableStateFile writes a state file holding a selection and marks, then
// makes it unreadable (mode 0000). It skips where that does not stop a read.
func unreadableStateFile(t *testing.T, path string) []byte {
	t.Helper()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("mode 0000 does not stop this reader")
	}
	body := []byte(`{"selected_profile":"work","onboarding":{"completed":false,"seen":{"welcome":true},"profiles":{"work":{"skipped":["browser"]}}}}`)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o600) })
	return body
}

func assertStateFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(want) {
		t.Fatalf("state file now %q (%v), want it unchanged: %q", got, err, want)
	}
}

// A state file that exists but cannot be read is never overwritten: the
// write is refused with an error, and the selection and marks survive.
func TestUnreadableStateFileIsNeverOverwritten(t *testing.T) {
	path := stateFileForTest(t)
	body := unreadableStateFile(t, path)
	if err := saveSelectedProfile(path, "other"); err == nil {
		t.Fatal("saveSelectedProfile over an unreadable file = nil, want an error")
	}
	if err := updateGuiState(path, func(st *guiState) { st.ensureOnboarding().Completed = true }); err == nil {
		t.Fatal("updateGuiState over an unreadable file = nil, want an error")
	}
	if _, err := readGuiStateLocked(path); err == nil {
		t.Fatal("readGuiStateLocked of an unreadable file = nil error")
	}
	assertStateFileBytes(t, path, body)
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
