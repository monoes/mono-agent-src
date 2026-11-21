// wails-app/app_onboarding.go
//
// The first-run onboarding's bindings: its state (onboarding_derive.go over
// the real facts gathered here) and the marks the steps write
// (onboarding_store.go). The marks are the app's own UI state; they need no
// sign-in and never run the CLI.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
)

// errOnboardingUnknown is GetOnboardingState's answer when the profile list
// could not be read and nothing is stored: whether this is a new install is
// not known, and the window shows the normal app rather than guess.
var errOnboardingUnknown = errors.New("onboarding state unknown: the profile list could not be read")

// GetOnboardingState returns every step's status. A fact that cannot be read
// (signed out, CLI missing) counts as false, except the profile count: an
// unread count never counts as zero, so a failed read never shows an
// existing user the onboarding. With the count unread the stored Completed
// is returned as is, or errOnboardingUnknown when nothing is stored. The
// first call on an install that has profiles but no marks writes the
// completed mark, so such an install never sees the onboarding.
func (a *App) GetOnboardingState() (*OnboardingState, error) {
	f, countKnown := a.gatherOnboardingFacts()
	return a.onboardingStateFor(f, countKnown)
}

// onboardingStateFor derives the state from the stored marks and f, and
// writes the existing install's completed mark once. countKnown is false
// when the profile list could not be read: f.ProfileCount is then a guess,
// Completed comes from the marks alone, and nothing is written.
func (a *App) onboardingStateFor(f onboardingFacts, countKnown bool) (*OnboardingState, error) {
	path := guiStatePath(a.dbPath)
	m := loadGuiState(path).Onboarding
	if !countKnown {
		if m == nil {
			return nil, errOnboardingUnknown
		}
		s := deriveOnboarding(m, f)
		s.Completed = m.Completed
		return &s, nil
	}
	s := deriveOnboarding(m, f)
	if m == nil && f.ProfileCount > 0 {
		err := updateGuiState(path, func(st *guiState) {
			if st.Onboarding == nil {
				st.Onboarding = &onboardingMarks{Completed: true}
			}
		})
		if err != nil {
			a.emitLog("SYSTEM", "WARN", "saving the onboarding state: "+err.Error())
		}
	}
	return &s, nil
}

// gatherOnboardingFacts reads the real state through the same CLI calls and
// checks the other bindings use, side by side. It reports whether the
// profile count was read; when it was not, a profile selected in memory
// still counts as one (one existed when the window chose it).
func (a *App) gatherOnboardingFacts() (onboardingFacts, bool) {
	f := onboardingFacts{ProfileID: a.selectedProfileOrEmpty()}
	guess := 0
	if f.ProfileID != "" {
		guess = 1
	}
	if a.signedOut() != nil {
		f.ProfileCount = guess
		return f, false
	}
	var (
		wg      sync.WaitGroup
		rows    []cliProfile
		listErr error
	)
	run := func(fn func()) {
		wg.Add(1)
		go func() { defer wg.Done(); fn() }()
	}
	run(func() { listErr = a.listProfilesInto(&rows) })
	if pid := f.ProfileID; pid != "" {
		run(func() { f.MonomindOn = a.IsMonomindInitialized() })
		run(func() { f.AgentUsable = a.agentRosterHasReady() })
		run(func() { f.BrowserAssigned = a.browserRunsProfile(pid) })
	}
	wg.Wait()
	if listErr != nil {
		f.ProfileCount = guess
		return f, false
	}
	f.ProfileCount = len(rows)
	return f, true
}

// agentRosterHasReady reports whether `agent roster` (AgentRoster) lists a
// model in the ready state: it passed validation recently on the runtime's
// current version, the same test the chat's default runtime uses.
func (a *App) agentRosterHasReady() bool {
	var r struct {
		Runtimes []struct {
			Models []struct {
				State string `json:"state"`
			} `json:"models"`
		} `json:"runtimes"`
	}
	if err := a.cliJSON(chatCLITimeout, &r, "agent", "roster"); err != nil {
		return false
	}
	for _, rr := range r.Runtimes {
		for _, m := range rr.Models {
			if m.State == "ready" {
				return true
			}
		}
	}
	return false
}

// browserRunsProfile reports whether this client is paired with the bridge
// (GetExtensionStatus: clientPairing "ok") and an attached browser is bound
// to pid (GetBrowsers). A browser bound to no profile, which runs any
// profile by default, does not count: the step binds one on purpose.
func (a *App) browserRunsProfile(pid string) bool {
	var st struct {
		ClientPairing string `json:"clientPairing"`
	}
	if json.Unmarshal([]byte(a.GetExtensionStatus()), &st) != nil || st.ClientPairing != "ok" {
		return false
	}
	r, err := a.GetBrowsers()
	if err != nil {
		return false
	}
	for _, b := range r.Browsers {
		if b.ProfileID == pid || slices.Contains(b.ProfileIDs, pid) {
			return true
		}
	}
	return false
}

// MarkOnboardingStep records a step that has no real state to read:
// welcome, system, permissions and firstRun machine-wide, about for the
// selected profile. Any other id is an error.
func (a *App) MarkOnboardingStep(id string) error {
	path := guiStatePath(a.dbPath)
	switch {
	case slices.Contains(onboardingMachineMarks, id):
		return updateGuiState(path, func(st *guiState) { st.ensureOnboarding().Seen[id] = true })
	case id == "about":
		pid, err := a.profileID()
		if err != nil {
			return err
		}
		return updateGuiState(path, func(st *guiState) {
			st.ensureOnboarding().updateProfile(pid, func(pm *onboardingProfileMarks) { pm.AboutDone = true })
		})
	}
	return fmt.Errorf("onboarding step %q cannot be marked", id)
}

// SkipOnboardingStep records a skipped step: system, agents, permissions and
// firstRun machine-wide; about, monomind and browser for the selected
// profile. welcome, profile and finish cannot be skipped.
func (a *App) SkipOnboardingStep(id string) error {
	path := guiStatePath(a.dbPath)
	switch {
	case slices.Contains(onboardingMachineSkips, id):
		return updateGuiState(path, func(st *guiState) {
			m := st.ensureOnboarding()
			m.Skipped = appendOnce(m.Skipped, id)
		})
	case slices.Contains(onboardingProfileSkips, id):
		pid, err := a.profileID()
		if err != nil {
			return err
		}
		return updateGuiState(path, func(st *guiState) {
			st.ensureOnboarding().updateProfile(pid, func(pm *onboardingProfileMarks) { pm.Skipped = appendOnce(pm.Skipped, id) })
		})
	}
	return fmt.Errorf("onboarding step %q cannot be skipped", id)
}

// CompleteOnboarding records that the onboarding was finished.
func (a *App) CompleteOnboarding() error {
	return updateGuiState(guiStatePath(a.dbPath), func(st *guiState) { st.ensureOnboarding().Completed = true })
}

// RestartOnboarding opens the onboarding again from Welcome: it clears the
// completed mark, Welcome's seen mark and every skip. What was really done
// (and the other seen marks) stays, so those steps show as done.
func (a *App) RestartOnboarding() error {
	return updateGuiState(guiStatePath(a.dbPath), func(st *guiState) {
		m := st.ensureOnboarding() // never nil again: nil would re-complete an install that has profiles
		m.Completed = false
		delete(m.Seen, "welcome")
		m.Skipped = nil
		for id, pm := range m.Profiles {
			pm.Skipped = nil
			m.Profiles[id] = pm
		}
	})
}

func appendOnce(list []string, id string) []string {
	if slices.Contains(list, id) {
		return list
	}
	return append(list, id)
}
