package coderhost

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/monoes/mono-agent/internal/dbconn"
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

	// Approval of child sessions: whether the lead of a parent session may approve the sessions below it
	// ("off" | "lead"), and the most a session's budget may be for the lead to approve it (0 = no ceiling).
	// Read from the mission's root session by the tasks store (ApprovalPolicy); operator-only. "lead" needs
	// a ceiling above 0, so that a lead's approval always has a spend limit to measure by.
	AgentApproval      string  `json:"agent_approval"`
	ApprovalCeilingUSD float64 `json:"approval_ceiling_usd"`

	// MissionDirections is K: how many directions a mission lead keeps and may have open at once (1 to 6; 0 is the
	// default of 3). Operator-only; session_spawn enforces it (sessionspawn_directions.go). It is optional: nil
	// (absent from the document) means nobody chose, so a child inherits its parent's K, while an operator's
	// explicit 0 is stored as 0. A pointer, so that an unrelated writer cannot turn "not set" into "set to 0".
	MissionDirections *int `json:"mission_directions,omitempty"`

	// Mesh: whether other monomind agents see this session and who may message it.
	MeshVisible bool   `json:"mesh_visible"`
	MeshAlias   string `json:"mesh_alias"` // "" = the chat's name with dashes for spaces, else one made from the project and start time
	MeshAccept  string `json:"mesh_accept"`

	// ChatMode: 'off' | 'mission' | 'open', the conversation's mode, chosen before its first message and locked from
	// then on (chatmode.go). It lives in the column coder_sessions.chat_mode, not in the document: reading fills it
	// from the column and a write moves it there.
	ChatMode string `json:"chat_mode,omitempty"`

	// Mission: the operator's switch for a mission chat, kept as the legacy spelling of ChatMode == 'mission' (a patch
	// or `settings set --mission` still names it). On, every turn of the conversation starts as a confirmed mission.
	// Reading fills it from the mode; it is never stored in the document.
	Mission bool `json:"mission"`

	// MissionWorkerReservations: which accounting mode a mission worker run of this conversation starts in when the
	// session is idle: a reservation per run (money held out of the budget) instead of the cap the run reads from the
	// session. Off by default; operator-only, and written only through Store.UpdateSettings. A run follows the mode
	// already in flight on its session, so flipping it never mixes the two. A store that cannot answer leaves it off.
	MissionWorkerReservations bool `json:"mission_worker_reservations"`

	// OwedStopNotice: set by the operator's `doomsday stop` when it stopped a run but could not write the chat message; the
	// next stop writes it and clears this. A closed code and times only, never text: the message is rebuilt from them. It
	// names the run (charter, generation, the time its stop was recorded) so another run's flag is not honoured. Operator-only:
	// an agent's patch naming it is refused, and a patch naming it cannot make a stop write anything it did not owe.
	OwedStopNotice *OwedStopNotice `json:"owed_stop_notice,omitempty"`
}

// OwedStopNotice is the flag Settings.OwedStopNotice holds.
type OwedStopNotice struct {
	Charter    string `json:"charter"`
	Generation int64  `json:"generation"`
	IntentAt   int64  `json:"intent_at"` // unix seconds the run's stop was recorded
	Code       string `json:"code"`      // one of OwedStopCodes
	At         int64  `json:"at"`        // unix seconds of the stop (or, for "later", of the repair) the message is dated
}

// valid says whether the flag names a run, times and a known code.
func (o *OwedStopNotice) valid() bool {
	return oneOf(o.Code, OwedStopCodes) && o.Generation > 0 && o.At > 0 && o.IntentAt > 0 && o.Charter != "" && len(o.Charter) <= 128
}

// OwedStopCodes are the verdicts an owed message can have; the wording of each is the doomsday package's.
var OwedStopCodes = []string{"plain", "partial", "best_effort", "later"}

// AutonomyChoice overrides the preset for one class of gap.
type AutonomyChoice struct {
	Who      string `json:"who"`      // user | master | model
	Fallback string `json:"fallback"` // park | take_default: what happens when nobody answers
}

// Allowed values.
var (
	AutonomyPresets = []string{"ask-always", "ask-on-risk", "decide-and-log"}
	GapClasses      = []string{"ambiguity", "scope_growth", "risky_action", "verification_failed", "missing_credential", "child_conflict", "plan_approval"}
	CheckinEvents   = []string{"child_finished", "child_blocked", "child_failed", "child_question", "budget_80", "child_silent"}
	MeshAccepts     = []string{"none", "project", "subtree"}
	AgentApprovals  = []string{"off", "lead"}
	whoValues       = []string{"user", "master", "model"}
	fallbackValues  = []string{"park", "take_default"}
	activeHours     = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]-([01][0-9]|2[0-3]):[0-5][0-9]$`)
	meshAlias       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

// OperatorOnly lists the fields only the operator changes: what the session may
// do on its own (autonomy, limits, who may message it).
var OperatorOnly = []string{"checkin_limit", "autonomy_preset", "autonomy_classes", "max_children", "max_concurrent", "max_depth", "mesh_accept", "agent_approval", "approval_ceiling_usd", "mission", "chat_mode", "mission_directions", "mission_worker_reservations", "owed_stop_notice"}

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
		MaxDepth:        3,
		AgentApproval:   "off",
		ChatMode:        ChatModeOff,
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
func (s Settings) Validate() error { return s.validateAt(MaxSessionDepth) }

// validateAt is Validate with the deepest max_depth the session's tree allows (the store's depthCeiling).
func (s Settings) validateAt(depthCeiling int) error {
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
	if err := within("max_depth", s.MaxDepth, 1, depthCeiling); err != nil { // the hard limit of the design (tasks.HardMaxSessionDepth), or a charter's
		return err
	}
	if s.MissionDirections != nil {
		if err := within("mission_directions", *s.MissionDirections, 0, hardMissionDirections); err != nil {
			return err
		}
	}
	if !oneOf(s.AgentApproval, AgentApprovals) {
		return fmt.Errorf("agent_approval must be one of %s", strings.Join(AgentApprovals, ", "))
	}
	if !(s.ApprovalCeilingUSD >= 0 && s.ApprovalCeilingUSD <= 1e6) { // also false for NaN
		return errors.New("approval_ceiling_usd must be between 0 and 1000000 (0 = no ceiling)")
	}
	if o := s.OwedStopNotice; o != nil && !o.valid() {
		return errors.New("owed_stop_notice needs a charter, a generation, times and one of " + strings.Join(OwedStopCodes, ", "))
	}
	if s.AgentApproval == "lead" && s.ApprovalCeilingUSD <= 0 {
		return errors.New("agent_approval lead needs approval_ceiling_usd above 0: a lead's approval must have a spend limit to measure by")
	}
	if s.MeshAlias != "" && !meshAlias.MatchString(s.MeshAlias) {
		return errors.New("mesh_alias can use letters, digits, '.', '_' and '-' (no spaces: write meeting-notes), must start with a letter or digit, and is at most 64 characters")
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
	return s.applyAt(patch, MaxSessionDepth)
}

// applyAt is Apply with the deepest max_depth the session's tree allows.
func (s Settings) applyAt(patch []byte, depthCeiling int) (Settings, []string, error) {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(patch, &keys); err != nil {
		return s, nil, fmt.Errorf("the patch is not a JSON object: %w", err)
	}
	out := s
	// The decode below writes through a non-nil pointer, so out gets a pointer of its own.
	out.MissionDirections = cloneInt(s.MissionDirections)
	if s.OwedStopNotice != nil {
		c := *s.OwedStopNotice
		out.OwedStopNotice = &c
	}
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
	if err := out.validateAt(depthCeiling); err != nil {
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
	return s.settingsIn(s.db, profileID, conversationID)
}

// settingsIn is GetSettings read through q.
func (s *Store) settingsIn(q querier, profileID, conversationID string) (Settings, error) {
	var raw, mode string
	err := q.QueryRow(`SELECT settings, chat_mode FROM coder_sessions WHERE profile_id = ? AND conversation_id = ?`, profileID, conversationID).Scan(&raw, &mode)
	out, err := settingsFromRow(conversationID, raw, err, s.readCeiling())
	return withChatMode(out, mode), err
}

// settingsFromRow is the settings a row read gave: no row, or nothing stored, is the defaults.
func settingsFromRow(conversationID, raw string, err error, depthCeiling int) (Settings, error) {
	def := DefaultSettings()
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (raw == "" || raw == "{}")) {
		return def, nil // no row, or nothing stored: the defaults
	}
	if err != nil {
		return def, fmt.Errorf("coder host: reading the settings of %s: %w", conversationID, err) // a database error is not "no settings"
	}
	rest, flag := splitStoredOwedFlag(raw)
	out, _, err := def.applyAt([]byte(rest), depthCeiling)
	if err != nil {
		return def, fmt.Errorf("coder host: stored settings of %s are not valid: %w", conversationID, err)
	}
	out.OwedStopNotice = flag
	return out, nil
}

// splitStoredOwedFlag takes owed_stop_notice out of a stored settings document and reads it leniently: fields this build
// does not know (a later version's) are ignored, and a flag that is malformed or invalid is dropped, so a bad flag never
// makes the rest of the chat's settings unreadable. The dropped flag (and the fields this build does not know) are not kept: the
// next write of the chat's settings, from any caller, stores the document without them. Only stored documents are read this way; a patch is still strict.
func splitStoredOwedFlag(raw string) (string, *OwedStopNotice) {
	if !strings.Contains(raw, `"owed_stop_notice"`) {
		return raw, nil
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &doc) != nil {
		return raw, nil
	}
	val, ok := doc["owed_stop_notice"]
	if !ok {
		return raw, nil
	}
	delete(doc, "owed_stop_notice")
	rest, err := json.Marshal(doc)
	if err != nil {
		return raw, nil
	}
	var o OwedStopNotice
	if json.Unmarshal(val, &o) != nil || !o.valid() {
		return string(rest), nil
	}
	return string(rest), &o
}

// ParseSettings reads the stored settings document of a session row (as `SELECT settings FROM coder_sessions`
// gives it), for a caller that reads the row inside a transaction of its own. A missing row (sql.ErrNoRows as
// err) is the defaults.
func ParseSettings(conversationID, raw string, err error) (Settings, error) {
	return settingsFromRow(conversationID, raw, err, MaxSessionDepth)
}

// SettingsChange computes the settings a write stores from the settings as they are NOW: cur was read inside the
// write transaction that stores the result, so a patch applied to cur cannot bring back a value another writer
// changed since the caller last looked. tx is that transaction's connection, for checks that must hold at the
// moment of the write (the mission lock reads the task sessions through it). An error rolls the write back.
type SettingsChange func(ctx context.Context, tx *sql.Conn, cur Settings) (Settings, error)

// settingsAfterRead, when set by a test, runs inside UpdateSettings right after the current settings were read.
var settingsAfterRead func()

// UpdateSettings is the one way settings are written: a read-modify-write of the stored document inside one
// BEGIN IMMEDIATE transaction. Every writer passes what it wants to change (a patch applied to cur), never an
// object it read earlier, so two writers of different keys both survive and an operator-only key (the Mission
// toggle) cannot be put back by a stale copy. The result is validated before it is stored. It returns the
// settings as stored.
func (s *Store) UpdateSettings(profileID, conversationID string, change SettingsChange) (Settings, error) {
	return s.updateSettings(profileID, conversationID, true, false, change)
}

// ErrNoSession is UpdateExistingSettings' answer for a conversation with no session row.
var ErrNoSession = errors.New("coder host: no such session")

// UpdateExistingSettings is UpdateSettings for bookkeeping that must not bring a session into being: it never creates
// the row (ErrNoSession when there is none, so a chat deleted meanwhile stays deleted) and leaves updated_at alone (it is
// not an edit of the chat). Used for the doomsday owed-notice flag.
func (s *Store) UpdateExistingSettings(profileID, conversationID string, change SettingsChange) (Settings, error) {
	return s.updateSettings(profileID, conversationID, true, true, change)
}

// updateSettings is UpdateSettings; strict false lets change replace a stored document that cannot be read
// (SaveSettings does, as the way to repair one).
func (s *Store) updateSettings(profileID, conversationID string, strict, existingOnly bool, change SettingsChange) (Settings, error) {
	ctx := context.Background()
	created, updated := s.stamp(), s.stamp() // read before the write lock is taken: a clock that writes would wait on it
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return Settings{}, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		dbconn.AbandonBegin(conn)
		return Settings{}, fmt.Errorf("coder host: begin settings write: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	if !existingOnly {
		if err := ensureSessionOn(ctx, conn, profileID, conversationID, created); err != nil {
			return Settings{}, err
		}
	}
	var raw, was string
	err = conn.QueryRowContext(ctx, `SELECT settings, chat_mode FROM coder_sessions WHERE profile_id = ? AND conversation_id = ?`, profileID, conversationID).Scan(&raw, &was)
	if existingOnly && errors.Is(err, sql.ErrNoRows) {
		return Settings{}, ErrNoSession
	}
	cur, err := settingsFromRow(conversationID, raw, err, s.readCeiling())
	cur = withChatMode(cur, was)
	if err != nil && strict {
		return Settings{}, err
	}
	if settingsAfterRead != nil {
		settingsAfterRead()
	}
	next, err := change(ctx, conn, cur)
	if err != nil {
		return Settings{}, err
	}
	if err := next.validateAt(s.writeCeiling(profileID, conversationID, cur.MaxDepth)); err != nil {
		return Settings{}, err
	}
	mode, err := ResolveChatMode(cur, next)
	if err != nil {
		return Settings{}, err
	}
	if err := writeChatMode(ctx, conn, profileID, conversationID, cur.ChatMode, mode); err != nil {
		return Settings{}, err
	}
	next = withChatMode(next, mode)
	doc := next
	doc.ChatMode, doc.Mission = "", false // the column is the truth, not the document
	out, err := json.Marshal(doc)
	if err != nil {
		return Settings{}, err
	}
	if existingOnly {
		_, err = conn.ExecContext(ctx, `UPDATE coder_sessions SET settings = ? WHERE profile_id = ? AND conversation_id = ?`, string(out), profileID, conversationID)
	} else {
		_, err = conn.ExecContext(ctx, `UPDATE coder_sessions SET settings = ?, updated_at = ? WHERE profile_id = ? AND conversation_id = ?`, string(out), updated, profileID, conversationID)
	}
	if err != nil {
		return Settings{}, err
	}
	if _, err := conn.ExecContext(context.WithoutCancel(ctx), "COMMIT"); err != nil {
		return Settings{}, fmt.Errorf("coder host: commit settings write: %w", err)
	}
	committed = true
	return next, nil
}

// SaveSettings replaces a session's whole settings document (creating the session if needed). It is for
// fixtures and for the initial settings of a session nobody else holds: it writes what it is given, so a caller
// that read the object earlier would bring back stale values. No production writer uses it
// (TestSettingsAreWrittenOnlyThroughUpdateSettings); they go through UpdateSettings.
func (s *Store) SaveSettings(profileID, conversationID string, v Settings) error {
	if err := v.Validate(); err != nil {
		return err
	}
	_, err := s.updateSettings(profileID, conversationID, false, false, func(context.Context, *sql.Conn, Settings) (Settings, error) { return v, nil })
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
