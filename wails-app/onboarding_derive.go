// wails-app/onboarding_derive.go
//
// Each onboarding step's status, worked out from the stored marks
// (onboarding_store.go) and the real state of the machine. A step whose
// result can be read (a profile exists, monomind is set up, an agent passed
// validation, a browser runs the profile) is done when that is true, even
// if it was skipped earlier; only what cannot be read is taken from the marks.
package main

import "slices"

// Step statuses.
const (
	onboardingDone    = "done"
	onboardingSkipped = "skipped"
	onboardingTodo    = "todo"
)

// onboardingStepIDs are the steps in the order they are shown.
var onboardingStepIDs = []string{
	"welcome", "system", "profile", "about", "monomind", "agents", "browser", "permissions", "firstRun", "finish",
}

// Which steps can be marked or skipped, and where the mark lives: in the
// machine-wide marks or in the selected profile's.
var (
	onboardingMachineMarks = []string{"welcome", "system", "permissions", "firstRun"}
	onboardingMachineSkips = []string{"system", "agents", "permissions", "firstRun"}
	onboardingProfileSkips = []string{"about", "monomind", "browser"}
)

// OnboardingStep is one step and its status (done, skipped or todo).
type OnboardingStep struct {
	ID     string `json:"id"`
	Status string `json:"status"` // done | skipped | todo
}

// OnboardingState is what the onboarding window shows.
type OnboardingState struct {
	Completed   bool             `json:"completed"`
	CurrentStep string           `json:"currentStep"` // first step that is todo; "finish" when none
	ProfileID   string           `json:"profileId"`   // "" before the first profile
	Steps       []OnboardingStep `json:"steps"`       // always the 10 ids, in order
}

// onboardingFacts is the real state the statuses are read from. A fact that
// could not be read is false.
type onboardingFacts struct {
	ProfileID       string // selected profile, "" if none
	ProfileCount    int
	MonomindOn      bool // IsMonomindInitialized for ProfileID
	AgentUsable     bool // the agent roster has at least one entry that passed validation
	BrowserAssigned bool // extension paired and a browser assigned to ProfileID
}

// deriveOnboarding works out every step's status.
//
// No marks at all with profiles present is an install that predates the
// onboarding: it counts as completed. With no profile the onboarding is never
// completed, whatever is stored; one completed before (every profile was
// deleted since) reopens at Welcome if that was never seen, else at the
// profile step.
func deriveOnboarding(m *onboardingMarks, f onboardingFacts) OnboardingState {
	if m == nil {
		m = &onboardingMarks{Completed: f.ProfileCount > 0}
	}
	var pm onboardingProfileMarks
	if f.ProfileID != "" {
		pm = m.Profiles[f.ProfileID]
	}
	completed := m.Completed && f.ProfileCount > 0
	done := map[string]bool{
		"welcome":     m.Seen["welcome"],
		"system":      m.Seen["system"],
		"profile":     f.ProfileCount > 0,
		"about":       pm.AboutDone,
		"monomind":    f.MonomindOn,
		"agents":      f.AgentUsable,
		"browser":     f.BrowserAssigned,
		"permissions": m.Seen["permissions"],
		"firstRun":    m.Seen["firstRun"],
		"finish":      completed,
	}
	skipped := func(id string) bool {
		if slices.Contains(onboardingProfileSkips, id) {
			return slices.Contains(pm.Skipped, id)
		}
		return slices.Contains(onboardingMachineSkips, id) && slices.Contains(m.Skipped, id)
	}
	s := OnboardingState{Completed: completed, ProfileID: f.ProfileID, Steps: make([]OnboardingStep, 0, len(onboardingStepIDs))}
	for _, id := range onboardingStepIDs {
		status := onboardingTodo
		switch {
		case done[id]:
			status = onboardingDone
		case skipped(id):
			status = onboardingSkipped
		}
		if status == onboardingTodo && s.CurrentStep == "" {
			s.CurrentStep = id
		}
		s.Steps = append(s.Steps, OnboardingStep{ID: id, Status: status})
	}
	if s.CurrentStep == "" {
		s.CurrentStep = "finish"
	}
	if f.ProfileCount == 0 && m.Completed {
		s.CurrentStep = "profile"
		if !m.Seen["welcome"] {
			s.CurrentStep = "welcome"
		}
	}
	return s
}
