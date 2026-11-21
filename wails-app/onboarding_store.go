// wails-app/onboarding_store.go
//
// The first-run onboarding's marks, kept in the app's UI state file
// (gui-state.json) beside the profile selection. Only what cannot be read
// from real state is stored here: which steps were seen or skipped, "about
// you" answered per profile, and whether the onboarding was completed
// (onboarding_derive.go turns these and the real facts into step statuses).
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// onboardingMarks is the stored part of the onboarding.
type onboardingMarks struct {
	Completed bool                              `json:"completed"`
	Seen      map[string]bool                   `json:"seen,omitempty"`     // machine-wide marks: welcome, system, permissions, firstRun
	Skipped   []string                          `json:"skipped,omitempty"`  // machine-wide skipped step ids: system, agents, permissions, firstRun
	Profiles  map[string]onboardingProfileMarks `json:"profiles,omitempty"` // keyed by profile id
}

// onboardingProfileMarks are the marks of the steps that belong to one profile.
type onboardingProfileMarks struct {
	Skipped   []string `json:"skipped,omitempty"` // about, monomind, browser
	AboutDone bool     `json:"about_done,omitempty"`
}

// guiStateMu serialises every read-modify-write of the state file: Wails runs
// each binding on its own goroutine, and a selection saved while a mark is
// written must not lose either change.
var guiStateMu sync.Mutex

// loadGuiState reads the state file. A missing or damaged file is the zero
// value, the same as nothing ever saved.
func loadGuiState(path string) guiState {
	var st guiState
	b, err := os.ReadFile(path)
	if err != nil {
		return guiState{}
	}
	if json.Unmarshal(b, &st) != nil {
		return guiState{}
	}
	return st
}

// updateGuiState reads the state file, applies mutate and writes the result
// through a temp file and a rename, so a crash never leaves half a file.
// mutate cannot fail: callers check their input before calling.
func updateGuiState(path string, mutate func(*guiState)) error {
	guiStateMu.Lock()
	defer guiStateMu.Unlock()
	st := loadGuiState(path)
	mutate(&st)
	return writeGuiState(path, st)
}

// writeGuiState writes st privately (0600 in a 0700 folder).
func writeGuiState(path string, st guiState) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), guiStateFile+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ensureOnboarding returns the state's marks, creating them (and their
// Seen map) when missing, for a mutate func that sets one.
func (st *guiState) ensureOnboarding() *onboardingMarks {
	if st.Onboarding == nil {
		st.Onboarding = &onboardingMarks{}
	}
	if st.Onboarding.Seen == nil {
		st.Onboarding.Seen = map[string]bool{}
	}
	return st.Onboarding
}

// updateProfile applies fn to one profile's marks and stores the result.
func (m *onboardingMarks) updateProfile(profileID string, fn func(*onboardingProfileMarks)) {
	if m.Profiles == nil {
		m.Profiles = map[string]onboardingProfileMarks{}
	}
	pm := m.Profiles[profileID]
	fn(&pm)
	m.Profiles[profileID] = pm
}
