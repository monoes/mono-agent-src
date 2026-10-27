package coderhost

import (
	"fmt"
	"unicode/utf8"
)

// Autonomy decides WHO answers a gap a session runs into (a question it cannot
// settle alone). It never decides what may be done: the access level of the
// conversation, budget raises, operator-only settings and the human-in-the-loop
// send gates are not inputs here and nothing in this file can change them. The
// boundaries below are enforced by clamp, whatever the preset or override says.

// Who answers a gap.
const (
	WhoUser   = "user"
	WhoMaster = "master"
	WhoModel  = "model"
)

// What happens to a question nobody answered in time.
const (
	FallbackPark        = "park"
	FallbackTakeDefault = "take_default"
)

// Decision is the policy's answer for one gap: who answers it, and what happens
// when nobody does.
type Decision struct {
	Answerer string `json:"answerer"`
	Fallback string `json:"fallback"` // "timeout_fallback"
}

// HardClass says whether a class of gap can never be answered by the model, nor
// have a default applied for it: acting on it widens what the session does in
// the world (a risky action) or needs something the session was not given (a
// credential).
func HardClass(class string) bool {
	return class == "risky_action" || class == "missing_credential"
}

// Decide is the policy: for a class of gap, with the gap's reversibility and
// whether the session has a master to ask, who answers and what the fallback is.
// It is a pure function of the settings. The result is always clamped: a hard
// class is never answered by the model and never takes a default; a master that
// does not exist is replaced by the user; an unknown class goes to the user.
func Decide(s Settings, class string, reversible, hasMaster bool) Decision {
	d := presetDecision(s.AutonomyPreset, class, reversible)
	if c, ok := s.AutonomyClasses[class]; ok {
		d = Decision{Answerer: c.Who, Fallback: c.Fallback}
	}
	return clamp(d, class, hasMaster)
}

func presetDecision(preset, class string, reversible bool) Decision {
	user := Decision{WhoUser, FallbackPark}
	master := Decision{WhoMaster, FallbackPark}
	model := Decision{WhoModel, FallbackTakeDefault}
	switch preset {
	case "ask-always":
		return user
	case "ask-on-risk":
		switch class {
		case "ambiguity", "scope_growth", "verification_failed":
			if reversible {
				return model
			}
		}
		return master
	case "decide-and-log":
		switch class {
		case "ambiguity", "scope_growth", "verification_failed", "child_conflict":
			return model
		}
		return master
	}
	return user // an unknown preset asks a person
}

func clamp(d Decision, class string, hasMaster bool) Decision {
	if !oneOf(class, GapClasses) {
		return Decision{WhoUser, FallbackPark}
	}
	if !oneOf(d.Answerer, whoValues) {
		d.Answerer = WhoUser
	}
	if !oneOf(d.Fallback, fallbackValues) {
		d.Fallback = FallbackPark
	}
	if HardClass(class) {
		if d.Answerer == WhoModel {
			d.Answerer = WhoMaster
		}
		d.Fallback = FallbackPark
	}
	if d.Answerer == WhoMaster && !hasMaster {
		d.Answerer = WhoUser
	}
	return d
}

// ValidateAutonomy refuses settings that would let the model answer a hard
// class or take a default for it. Settings.Validate calls it, so a write that
// tries it fails with this message and nothing is stored; clamp covers settings
// that were stored before the rule existed.
func ValidateAutonomy(s Settings) error {
	for class, c := range s.AutonomyClasses {
		if !HardClass(class) {
			continue
		}
		if c.Who == WhoModel {
			return fmt.Errorf("autonomy_classes.%s: the model may not answer %s gaps; choose %s or %s", class, class, WhoMaster, WhoUser)
		}
		if c.Fallback == FallbackTakeDefault {
			return fmt.Errorf("autonomy_classes.%s: %s gaps never take a default when nobody answers; use %q", class, class, FallbackPark)
		}
	}
	return nil
}

// AutonomyMatrix is what the policy gives for every class, for display.
func AutonomyMatrix(s Settings, reversible, hasMaster bool) map[string]Decision {
	out := map[string]Decision{}
	for _, c := range GapClasses {
		out[c] = Decide(s, c, reversible, hasMaster)
	}
	return out
}

// trimTo cuts s to at most n bytes without splitting a character.
func trimTo(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) && len(s) > 0 {
		s = s[:len(s)-1]
	}
	return s
}
