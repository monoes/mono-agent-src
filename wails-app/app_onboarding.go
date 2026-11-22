// wails-app/app_onboarding.go
//
// The first-run onboarding's bindings: its state (onboarding_derive.go over
// the real facts gathered here) and the marks the steps write
// (onboarding_store.go). The marks are the app's own UI state and never run
// the CLI; like every binding, each one refuses signed out
// (TestEveryBindingRefusesSignedOut), when the sign-in screen shows instead.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/profiledir"
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
// writes the existing install's completed mark once. A state file that exists
// but cannot be read is an error. countKnown is false when the profile list
// could not be read: f.ProfileCount is then a guess, Completed comes from the
// marks alone, and nothing is written.
func (a *App) onboardingStateFor(f onboardingFacts, countKnown bool) (*OnboardingState, error) {
	path := guiStatePath(a.dbPath)
	st, err := readGuiStateLocked(path)
	if err != nil {
		return nil, fmt.Errorf("reading the onboarding state: %w", err)
	}
	m := st.Onboarding
	if !countKnown {
		if m == nil {
			return nil, errOnboardingUnknown
		}
		// A completed onboarding had a profile, so the answer stays
		// self-consistent: profile and finish done, Completed true.
		if m.Completed && f.ProfileCount == 0 {
			f.ProfileCount = 1
		}
		s := deriveOnboarding(m, f)
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

// onboardingFullReadTimeout bounds the whole full read: a fact not in by then
// counts as false (todo), and a profile list not in by then is an unread count.
var onboardingFullReadTimeout = 12 * time.Second

// gatherOnboardingFacts reads the real state through the same CLI calls and
// checks the other bindings use, side by side. It reports whether the
// profile count was read; when it was not, a profile selected in memory
// still counts as one (one existed when the window chose it). When profiles
// exist but none is selected (the startup read failed), it selects one as
// startup does, so the per-profile steps can be marked. full=false reads the
// profile count only, which leaves MonomindOn, AgentUsable and
// BrowserAssigned false; full=true reads every fact within
// onboardingFullReadTimeout.
func (a *App) gatherOnboardingFacts(full bool) (onboardingFacts, bool) {
	f := onboardingFacts{ProfileID: a.selectedProfileOrEmpty()}
	ctx := context.Background()
	if full {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, onboardingFullReadTimeout)
		defer cancel()
	}
	type listed struct {
		rows []cliProfile
		err  error
	}
	listCh := make(chan listed, 1)
	go func() {
		var rows []cliProfile
		err := a.listProfilesInto(&rows)
		listCh <- listed{rows, err}
	}()
	var facts *onboardingFactChans
	if full && f.ProfileID != "" {
		facts = a.startOnboardingFacts(f.ProfileID)
	}
	countKnown := false
	select {
	case l := <-listCh:
		if l.err == nil {
			countKnown = true
			f.ProfileCount = len(l.rows)
			if f.ProfileID == "" && len(l.rows) > 0 {
				f.ProfileID = a.selectProfileForOnboarding(l.rows)
				if full && f.ProfileID != "" {
					facts = a.startOnboardingFacts(f.ProfileID)
				}
			}
		}
	case <-ctx.Done():
	}
	if !countKnown && f.ProfileID != "" {
		f.ProfileCount = 1
	}
	if facts != nil {
		recv := func(c chan bool) bool {
			select {
			case v := <-c:
				return v
			case <-ctx.Done():
				return false
			}
		}
		f.MonomindOn = recv(facts.monomind)
		f.AgentUsable = recv(facts.agents)
		f.BrowserAssigned = recv(facts.browser)
	}
	return f, countKnown
}

// onboardingFactChans carry the three slow facts, each sent once.
type onboardingFactChans struct{ monomind, agents, browser chan bool }

// startOnboardingFacts starts reading pid's facts side by side. A read that
// outlives the caller's deadline finishes into its buffered channel and is
// dropped.
func (a *App) startOnboardingFacts(pid string) *onboardingFactChans {
	c := &onboardingFactChans{make(chan bool, 1), make(chan bool, 1), make(chan bool, 1)}
	go func() { c.monomind <- isMonomindInitializedAt(profiledir.Root(a.db, pid)) }()
	go func() { c.agents <- a.agentRosterHasReady() }()
	go func() { c.browser <- a.browserRunsProfile(pid) }()
	return c
}

// selectProfileForOnboarding selects a profile the way startup does (the
// saved one if it still exists, else the oldest) when none is selected, and
// returns the selection. A selection made meanwhile by another call wins.
func (a *App) selectProfileForOnboarding(rows []cliProfile) string {
	id, err := resolveSelectedProfile(guiStatePath(a.dbPath), rows)
	if id == "" {
		return a.selectedProfileOrEmpty()
	}
	if err != nil {
		a.emitLog("SYSTEM", "WARN", "selecting a profile: "+err.Error())
	}
	if a.selectedProfilePtr.CompareAndSwap(nil, &id) {
		a.restartProfileWatchers()
		a.emitLog("SYSTEM", "INFO", "Selected profile: "+id)
		return id
	}
	return a.selectedProfileOrEmpty()
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
	var (
		wg sync.WaitGroup
		st struct {
			ClientPairing string `json:"clientPairing"`
		}
		stErr error
		r     *BrowsersReport
		err   error
	)
	wg.Add(2)
	go func() { defer wg.Done(); stErr = json.Unmarshal([]byte(a.GetExtensionStatus()), &st) }()
	go func() { defer wg.Done(); r, err = a.GetBrowsers() }()
	wg.Wait()
	if stErr != nil || st.ClientPairing != "ok" || err != nil {
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
