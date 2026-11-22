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
	"errors"
	"fmt"
	"io/fs"
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

// guiStateMu serialises every in-process read and read-modify-write of the
// state file: Wails runs each binding on its own goroutine, a selection saved
// while a mark is written must not lose either change, and on Windows a
// reader holding the file open makes the writer's rename fail. Only Stop's
// cold reader (persistedProfileForStop) stays outside it: it runs with Stop's
// busy lock held and must never wait on a writer.
//
// The lock is per process. The app runs as one window per process; two
// instances writing at once could still lose one change.
var guiStateMu sync.Mutex

// readGuiState reads the state file without the lock. Only a missing file
// or one that is not valid JSON is the zero value (nothing saved, or a damaged
// file); any other read error is returned, so a caller about to write can
// refuse instead of erasing the selection and the marks with an empty state.
func readGuiState(path string) (guiState, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return guiState{}, nil
	}
	if err != nil {
		return guiState{}, err
	}
	var st guiState
	if json.Unmarshal(b, &st) != nil {
		return guiState{}, nil
	}
	return st, nil
}

// readGuiStateLocked is readGuiState under guiStateMu, for a reader that must
// surface a read error.
func readGuiStateLocked(path string) (guiState, error) {
	guiStateMu.Lock()
	defer guiStateMu.Unlock()
	return readGuiState(path)
}

// loadGuiState reads the state file under the lock. A missing or damaged
// file is the zero value, the same as nothing ever saved; so is a file that
// cannot be read, for a reader with no way to report it (nothing is ever
// written from this answer: updateGuiState reads again and refuses).
func loadGuiState(path string) guiState {
	st, err := readGuiStateLocked(path)
	if err != nil {
		return guiState{}
	}
	return st
}

// updateGuiState reads the state file, applies mutate and writes the result
// through a temp file and a rename, so a crash never leaves half a file.
// mutate cannot fail: callers check their input before calling. A file that
// exists but cannot be read is an error and nothing is written.
func updateGuiState(path string, mutate func(*guiState)) error {
	guiStateMu.Lock()
	defer guiStateMu.Unlock()
	st, err := readGuiState(path)
	if err != nil {
		return fmt.Errorf("reading the app state: %w", err)
	}
	mutate(&st)
	return writeGuiState(path, st)
}

// writeGuiState writes st privately (0600 in a 0700 folder), flushed to the
// disk before the rename so a power cut leaves the old file or the new one.
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
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return replaceGuiStateFile(tmp.Name(), path)
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
