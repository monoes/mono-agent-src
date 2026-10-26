package coderhost

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Settings are the per-session knobs of a coder session. They are stored as one
// JSON document on the session row; a field the document leaves out has its
// default. Settings is the whole truth: Validate says what is allowed.
//
// Which fields an agent may change on its own session is decided by OperatorOnly:
// anything that widens what the session may do or whom it answers to is the
// operator's. Until sessions carry their own tokens (a later phase) every
// change is made through operator-only commands anyway.
type Settings struct {
	// Check-ins: how the master is brought up to date on its children.
	CheckinMinutes  int      `json:"checkin_minutes"`   // 0 = no timed check-ins
	SkipIfUnchanged bool     `json:"skip_if_unchanged"` // no turn when nothing changed since the last check-in
	CheckinTriggers []string `json:"checkin_triggers"`  // events that check in early
	CheckinLimit    int      `json:"checkin_limit"`     // check-ins in a row, with no message from a person between, before the master waits for one
	SilentMinutes   int      `json:"silent_minutes"`    // a child with no events this long counts as silent
	ActiveHours     string   `json:"active_hours"`      // "" = always, or "HH:MM-HH:MM" (local time)

	// Autonomy: who settles a gap the session runs into.
	AutonomyPreset  string                    `json:"autonomy_preset"`
	AutonomyClasses map[string]AutonomyChoice `json:"autonomy_classes"`

	// Limits on the children a master may have.
	MaxChildren   int `json:"max_children"`
	MaxConcurrent int `json:"max_concurrent"`
	MaxDepth      int `json:"max_depth"`

	// Mesh: whether other monomind agents see this session and who may message it.
	MeshVisible bool   `json:"mesh_visible"`
	MeshAlias   string `json:"mesh_alias"` // "" = a name made from the project and start time
	MeshAccept  string `json:"mesh_accept"`
}

// AutonomyChoice overrides the preset for one class of gap.
type AutonomyChoice struct {
	Who      string `json:"who"`      // user | master | model
	Fallback string `json:"fallback"` // park | take_default: what happens when nobody answers
}

// Allowed values.
var (
	AutonomyPresets = []string{"ask-always", "ask-on-risk", "decide-and-log"}
	GapClasses      = []string{"ambiguity", "scope_growth", "risky_action", "verification_failed", "missing_credential", "child_conflict"}
	CheckinEvents   = []string{"child_finished", "child_blocked", "child_failed", "child_question", "budget_80", "child_silent"}
	MeshAccepts     = []string{"none", "project", "subtree"}
	whoValues       = []string{"user", "master", "model"}
	fallbackValues  = []string{"park", "take_default"}
	activeHours     = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]-([01][0-9]|2[0-3]):[0-5][0-9]$`)
	meshAlias       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

// OperatorOnly lists the fields only the operator changes: what the session may
// do on its own (autonomy, limits, who may message it).
var OperatorOnly = []string{"checkin_limit", "autonomy_preset", "autonomy_classes", "max_children", "max_concurrent", "max_depth", "mesh_accept"}

// DefaultSettings is what a session has until something is changed.
func DefaultSettings() Settings {
	return Settings{
		CheckinMinutes:  15,
		CheckinLimit:    48, // twelve hours of 15-minute check-ins
		SkipIfUnchanged: true,
		CheckinTriggers: append([]string{}, CheckinEvents...),
		SilentMinutes:   30,
		AutonomyPreset:  "ask-on-risk",
		AutonomyClasses: map[string]AutonomyChoice{},
		MaxChildren:     4,
		MaxConcurrent:   2,
		MaxDepth:        2,
		MeshVisible:     true,
		MeshAccept:      "subtree", // not "project": that trusts any local process that claims the project
	}
}

func oneOf(v string, allowed []string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

func within(name string, v, lo, hi int) error {
	if v < lo || v > hi {
		return fmt.Errorf("%s must be between %d and %d", name, lo, hi)
	}
	return nil
}

// Validate says whether s may be stored.
func (s Settings) Validate() error {
	if s.CheckinMinutes != 0 {
		if err := within("checkin_minutes", s.CheckinMinutes, 1, 1440); err != nil {
			return err
		}
	}
	if err := within("silent_minutes", s.SilentMinutes, 5, 1440); err != nil {
		return err
	}
	if err := within("checkin_limit", s.CheckinLimit, 1, 500); err != nil {
		return err
	}
	if s.ActiveHours != "" {
		if !activeHours.MatchString(s.ActiveHours) || s.ActiveHours[:5] == s.ActiveHours[6:] {
			return errors.New(`active_hours must be empty or "HH:MM-HH:MM" with two different times`)
		}
	}
	seen := map[string]bool{}
	for _, e := range s.CheckinTriggers {
		if !oneOf(e, CheckinEvents) {
			return fmt.Errorf("checkin_triggers: %q is not one of %s", e, strings.Join(CheckinEvents, ", "))
		}
		if seen[e] {
			return fmt.Errorf("checkin_triggers: %q is listed twice", e)
		}
		seen[e] = true
	}
	if !oneOf(s.AutonomyPreset, AutonomyPresets) {
		return fmt.Errorf("autonomy_preset must be one of %s", strings.Join(AutonomyPresets, ", "))
	}
	for class, c := range s.AutonomyClasses {
		if !oneOf(class, GapClasses) {
			return fmt.Errorf("autonomy_classes: %q is not one of %s", class, strings.Join(GapClasses, ", "))
		}
		if !oneOf(c.Who, whoValues) || !oneOf(c.Fallback, fallbackValues) {
			return fmt.Errorf("autonomy_classes.%s needs who (%s) and fallback (%s)", class, strings.Join(whoValues, "|"), strings.Join(fallbackValues, "|"))
		}
	}
	if err := ValidateAutonomy(s); err != nil {
		return err
	}
	if err := within("max_children", s.MaxChildren, 0, 20); err != nil {
		return err
	}
	if err := within("max_concurrent", s.MaxConcurrent, 1, 10); err != nil {
		return err
	}
	if err := within("max_depth", s.MaxDepth, 1, 2); err != nil { // 2 is the hard limit of the design
		return err
	}
	if s.MeshAlias != "" && !meshAlias.MatchString(s.MeshAlias) {
		return errors.New("mesh_alias must be letters, digits, '.', '_' and '-', starting with a letter or digit, at most 64 characters")
	}
	if !oneOf(s.MeshAccept, MeshAccepts) {
		return fmt.Errorf("mesh_accept must be one of %s", strings.Join(MeshAccepts, ", "))
	}
	return nil
}

// Apply returns s with the fields of patch (a JSON object) replaced, after
// checking the result. A key that is not a setting is refused, so a typo does
// not silently change nothing. It also returns the keys the patch changed.
func (s Settings) Apply(patch []byte) (Settings, []string, error) {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(patch, &keys); err != nil {
		return s, nil, fmt.Errorf("the patch is not a JSON object: %w", err)
	}
	out := s
	// A patched map or list replaces the old one (not merged into it), and the
	// copy shares no storage with s.
	out.AutonomyClasses = map[string]AutonomyChoice{}
	if _, patched := keys["autonomy_classes"]; !patched {
		for k, v := range s.AutonomyClasses {
			out.AutonomyClasses[k] = v
		}
	}
	out.CheckinTriggers = nil
	if _, patched := keys["checkin_triggers"]; !patched {
		out.CheckinTriggers = append([]string{}, s.CheckinTriggers...)
	}
	dec := json.NewDecoder(bytes.NewReader(patch))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return s, nil, fmt.Errorf("the patch has a setting that does not exist or has the wrong type: %w", err)
	}
	if out.CheckinTriggers == nil {
		out.CheckinTriggers = []string{}
	}
	if err := out.Validate(); err != nil {
		return s, nil, err
	}
	changed := make([]string, 0, len(keys))
	for k := range keys {
		changed = append(changed, k)
	}
	sort.Strings(changed)
	return out, changed, nil
}

// TouchesOperatorOnly reports which of the changed keys are the operator's.
func TouchesOperatorOnly(changed []string) []string {
	var out []string
	for _, k := range changed {
		if oneOf(k, OperatorOnly) {
			out = append(out, k)
		}
	}
	return out
}

// GetSettings returns a session's settings; a session with none stored has the
// defaults, and reading never creates the session.
func (s *Store) GetSettings(profileID, conversationID string) (Settings, error) {
	def := DefaultSettings()
	var raw string
	err := s.db.QueryRow(`SELECT settings FROM coder_sessions WHERE profile_id = ? AND conversation_id = ?`, profileID, conversationID).Scan(&raw)
	if err != nil || raw == "" || raw == "{}" {
		return def, nil //nolint:nilerr // no row: the defaults
	}
	out, _, err := def.Apply([]byte(raw))
	if err != nil {
		return def, fmt.Errorf("coder host: stored settings of %s are not valid: %w", conversationID, err)
	}
	return out, nil
}

// SaveSettings stores a session's settings (creating the session if needed).
func (s *Store) SaveSettings(profileID, conversationID string, v Settings) error {
	if err := v.Validate(); err != nil {
		return err
	}
	if _, err := s.EnsureSession(profileID, conversationID); err != nil {
		return err
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE coder_sessions SET settings = ?, updated_at = ? WHERE profile_id = ? AND conversation_id = ?`, string(raw), s.stamp(), profileID, conversationID)
	return err
}

// SetUnattended records whether the session may run with nobody there. The
// pane always shows it.
func (s *Store) SetUnattended(profileID, conversationID string, on bool) error {
	if _, err := s.EnsureSession(profileID, conversationID); err != nil {
		return err
	}
	v := 0
	if on {
		v = 1
	}
	_, err := s.db.Exec(`UPDATE coder_sessions SET unattended = ?, updated_at = ? WHERE profile_id = ? AND conversation_id = ?`, v, s.stamp(), profileID, conversationID)
	return err
}
