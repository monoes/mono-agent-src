// wails-app/app_onboarding.go
//
// The first-run onboarding's bindings: its state (onboarding_derive.go over
// the real facts gathered here) and the marks the steps write
// (onboarding_store.go). The marks are the app's own UI state and never run
// the CLI; like every binding, each one refuses signed out
// (TestEveryBindingRefusesSignedOut), when the sign-in screen shows instead.
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

// GetOnboardingState is the fast answer the launch gate asks for (it allows
// it a few seconds): whether to show the onboarding at all. It reads the
// stored marks and the profile count, nothing else: no monomind, agent
// roster or browser check. Completed and ProfileID are exact; the monomind,
// agents and browser steps carry their stored marks only (skipped or todo,
// never done), and CurrentStep is worked out from those. The onboarding
// screen and the Finish setup card use GetOnboardingStateFull.
//
// Signed out it is the login-required error. The profile count is never
// guessed as zero: when it cannot be read (CLI missing, an error, a timeout)
// the stored Completed is returned as is, or
// errOnboardingUnknown when nothing is stored, so a failed read never shows
// an existing user the onboarding. The first call on an install that has
// profiles but no marks writes the completed mark, so such an install never
// sees the onboarding.
func (a *App) GetOnboardingState() (*OnboardingState, error) {
	if err := a.signedOut(); err != nil {
		return nil, err
	}
	f, countKnown := a.gatherOnboardingFacts(false)
	return a.onboardingStateFor(f, countKnown)
}

// GetOnboardingStateFull is GetOnboardingState with every fact read: the
// monomind marker, the agent roster and the browser binding, so every step
// that has real state is done when that state holds. A fact that cannot be
// read counts as false. It can take as long as the slowest of those reads
// (the agent roster runs a scan); the onboarding screen and the dashboard's
// Finish setup card use it.
func (a *App) GetOnboardingStateFull() (*OnboardingState, error) {
	if err := a.signedOut(); err != nil {
		return nil, err
	}
	f, countKnown := a.gatherOnboardingFacts(true)
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
		for i := range s.Steps {
			if s.Steps[i].ID == "finish" {
				s.Steps[i].Status = onboardingTodo
				if m.Completed {
					s.Steps[i].Status = onboardingDone
				}
			}
		}
		return &s, nil
	}
	s := deriveOnboarding(m, f)
	if m == nil && f.ProfileCount > 0 {
		err := updateGuiState(path, func(st *guiState) {
			if st.Onboarding == nil {
				st.Onboarding = legacyOnboardingMarks()
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
// still counts as one (one existed when the window chose it). full=false
// reads the profile count only, which leaves MonomindOn, AgentUsable and
// BrowserAssigned false.
func (a *App) gatherOnboardingFacts(full bool) (onboardingFacts, bool) {
	f := onboardingFacts{ProfileID: a.selectedProfileOrEmpty()}
	guess := 0
	if f.ProfileID != "" {
		guess = 1
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
	if pid := f.ProfileID; pid != "" && full {
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
	if err := a.signedOut(); err != nil {
		return err
	}
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
	if err := a.signedOut(); err != nil {
		return err
	}
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
	if err := a.signedOut(); err != nil {
		return err
	}
	return updateGuiState(guiStatePath(a.dbPath), func(st *guiState) { st.ensureOnboarding().Completed = true })
}

// RestartOnboarding opens the onboarding again from Welcome: it clears the
// completed mark, Welcome's seen mark and every skip. What was really done
// (and the other seen marks) stays, so those steps show as done.
func (a *App) RestartOnboarding() error {
	if err := a.signedOut(); err != nil {
		return err
	}
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
