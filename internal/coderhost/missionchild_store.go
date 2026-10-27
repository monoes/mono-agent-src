package coderhost

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/tasks"
)

// Mission child sessions, data model and store (docs/mastermind/plans/2026-10-27-mission-child-sessions.md,
// phase P-A). A task session's lead may mark a task to run in a child coder session of its own, within a
// policy (mode, how many children, what share of the lead's remaining budget they may carve). This file
// keeps the policy, the marks and the audit trail, and answers whether there is room for one more. It
// starts no session: the spawn path is a later phase.
//
// Authority:
//   - A caller is a CallerScope. The lead's is a LeadScope made from a verified session token (never from an
//     argument); the person's is OperatorScope. Any other scope is refused.
//   - Only the lead of that task session marks, reserves or links a child. A caller whose process holds a
//     task-bound mission binding (a worker or verifier of the mission), or whose binding was not checked, is refused. The store cannot see the
//     environment, so the tool layer passes the verified binding to NewLeadScope.
//   - Every refusal of "is this your task" is the same error (errMissionRefused), whether the session is
//     someone else's, in another profile, or does not exist, and whether the task is in it or not.
//   - The lead raises max_children only up to the lead session's own max_children setting (operator-only) and
//     share_pct only up to ShareHardMax. Mode "all" is the operator's to turn on; the lead may switch off and marked freely and lower from all.
//     Settings, access and the lead's own budget are never written from here.
//   - The one write outside the coder_mission_* tables is releaseCarve: it lowers a child's budget when its
//     turn has ended, never raises it and never to 0 (0 means uncapped).
//   - The files read tasks and task_sessions and never write them (see autonomy_guard_test.go).

// Mission child policy modes and limits.
const (
	ModeOff    = "off"    // no task runs in its own session
	ModeMarked = "marked" // only tasks the lead marked
	ModeAll    = "all"    // every worker task (the operator's to turn on)

	// ShareHardMax is the most of the lead's remaining budget its children may carve, in percent.
	ShareHardMax = 80
	// LargeChildBudgetUSD is the carve from which a child counts as large when nobody said.
	LargeChildBudgetUSD = 3.00
	// MaxChildAttempts is how many times one task may be started in its own session.
	MaxChildAttempts = 3
	// MaxMissionEvents is how many audit rows a task session keeps (the newest).
	MaxMissionEvents = 200

	defaultPolicyMaxChildren = 2
	defaultPolicySharePct    = 50
	policyMaxChildrenLimit   = 20 // as settings.Validate
	minDerivedCarveUSD       = 0.50
)

// Child row states and end reasons.
const (
	ChildMarked   = "marked"
	ChildSpawning = "spawning"
	ChildSpawned  = "spawned"
	ChildEnded    = "ended"
	ChildFailed   = "failed"
	ChildGone     = "gone"
	ChildReleased = "released"

	EndDone       = "done"
	EndNoFinish   = "no_finish"
	EndStopped    = "stopped"
	EndBudget     = "budget"
	EndExpired    = "expired"
	EndDeleted    = "deleted"
	EndLeadStop   = "lead_stopped"
	EndCascade    = "cascade"
	originLead    = "lead"
	originRule    = "rule"
	sizeLarge     = "large"
	sizeSmall     = "small"
	displayBubble = "bubble"
	displayChart  = "chart"
)

// errMissionRefused is the one answer to "you may not do this to that task": not the lead, a bound worker,
// another session, another profile, a task that is not in the session, a session that does not exist.
var errMissionRefused = fmt.Errorf("%w: that is not a task of a mission you lead", ErrNotYours)

// MissionBinding is what the tool layer found out about the calling process's task binding. The zero value
// is "unknown", which the store treats as bound: a scope built without the check is refused.
type MissionBinding int

const (
	BindingUnknown MissionBinding = iota // not checked: refused
	BindingNone                          // verified: the process holds no task-bound mission binding
	BindingTask                          // verified: it holds one (a worker or verifier of a mission): refused
)

// LeadScope is the CallerScope of the lead of a task session: the Caller of its verified session token plus
// the result of checking the process for a task-bound mission binding. Build it with NewLeadScope. Only
// BindingNone passes; a scope with no binding result (the zero LeadScope, BindingUnknown) is refused, so a
// tool that forgets the check fails closed.
type LeadScope struct {
	Caller  Caller
	binding MissionBinding
}

// NewLeadScope makes the scope of a verified caller whose binding was checked.
func NewLeadScope(c Caller, b MissionBinding) LeadScope { return LeadScope{Caller: c, binding: b} }

// Role is WhoMaster.
func (LeadScope) Role() string { return WhoMaster }

// ConversationID is the lead's own conversation, from the verified token.
func (l LeadScope) ConversationID() string { return l.Caller.ConversationID }

// Policy is the rules for the child sessions of one task session.
type Policy struct {
	ProfileID          string  `json:"profileId"`
	SessionID          int64   `json:"sessionId"`
	LeadConversationID string  `json:"leadConversationId"`
	Mode               string  `json:"mode"`
	MaxChildren        int     `json:"maxChildren"`
	SharePct           int     `json:"sharePct"`
	DefaultBudgetUSD   float64 `json:"defaultBudgetUsd"`
	RaisedBy           string  `json:"raisedBy,omitempty"`
	UpdatedAt          string  `json:"updatedAt,omitempty"`
	Rev                int64   `json:"rev"`    // bumped by every write; SetPolicy writes only against the revision it read
	Stored             bool    `json:"stored"` // false: the defaults, nothing stored yet
}

// PolicyPatch changes a policy; a nil field is left alone.
type PolicyPatch struct {
	Mode             *string
	MaxChildren      *int
	SharePct         *int
	DefaultBudgetUSD *float64
}

// MarkOptions are what the lead may say about a marked task.
type MarkOptions struct {
	Size      string  // "", large or small
	BudgetUSD float64 // 0 = derive from the share
}

// ChildRow is one task marked to run in its own session.
type ChildRow struct {
	ID                  int64   `json:"id"`
	ProfileID           string  `json:"profileId"`
	SessionID           int64   `json:"sessionId"`
	TaskID              int64   `json:"taskId"`
	LeadConversationID  string  `json:"leadConversationId"`
	ChildConversationID string  `json:"childConversationId,omitempty"`
	Origin              string  `json:"origin"`
	Size                string  `json:"size,omitempty"`
	BudgetUSD           float64 `json:"budgetUsd"`
	ReservedUSD         float64 `json:"reservedUsd"` // the carve held while the task is spawning
	State               string  `json:"state"`
	EndReason           string  `json:"endReason,omitempty"`
	Attempts            int     `json:"attempts"`
	CreatedAt           string  `json:"createdAt"`
	UpdatedAt           string  `json:"updatedAt"`
	SpawnedAt           string  `json:"spawnedAt,omitempty"`
	EndedAt             string  `json:"endedAt,omitempty"`
}

// MissionEvent is one audit row.
type MissionEvent struct {
	ID        int64  `json:"id"`
	SessionID int64  `json:"sessionId"`
	TaskID    int64  `json:"taskId,omitempty"`
	Kind      string `json:"kind"`
	Who       string `json:"who"`
	Note      string `json:"note,omitempty"`
	At        string `json:"at"`
}

// ChildCounts are the numbers the check-in digest shows.
type ChildCounts struct {
	Live   int `json:"live"`   // spawning or spawned
	Queued int `json:"queued"` // marked, waiting for room
	Failed int `json:"failed"`
}

// Room is the answer of RoomFor.
type Room struct {
	OK            bool    `json:"ok"`
	Capped        bool    `json:"capped"`     // the lead has a budget, so the share applies
	AllowedUSD    float64 `json:"allowedUsd"` // share_pct of the lead's remaining budget (when Capped)
	Reason        string  `json:"reason,omitempty"`
	Live          int     `json:"live"`
	Ceiling       int     `json:"ceiling"`
	Running       int     `json:"running"`
	MaxConcurrent int     `json:"maxConcurrent"`
}

type missionSession struct {
	id       int64
	lead     string
	state    string
	archived bool
}

// loadMissionSession reads the task session (read-only). found is false when the profile has none with that id.
func (s *Store) loadMissionSession(profileID string, sessionID int64) (m missionSession, found bool, err error) {
	var conv sql.NullString
	var archived string
	err = s.db.QueryRow(`SELECT conversation_id, state, archived_at FROM task_sessions WHERE profile_id = ? AND id = ?`,
		profileID, sessionID).Scan(&conv, &m.state, &archived)
	if errors.Is(err, sql.ErrNoRows) {
		return m, false, nil
	}
	if err != nil {
		return m, false, err
	}
	m.id, m.lead, m.archived = sessionID, conv.String, archived != ""
	return m, true, nil
}

// leadOf identifies the caller as the lead of the task session. Every way of not being it is errMissionRefused.
func (s *Store) leadOf(sc CallerScope, profileID string, sessionID int64) (LeadScope, missionSession, error) {
	l, ok := sc.(LeadScope)
	if !ok || l.binding != BindingNone || l.Caller.ProfileID == "" || l.Caller.ConversationID == "" || l.Caller.ProfileID != profileID || profileID == "" {
		return LeadScope{}, missionSession{}, errMissionRefused
	}
	m, found, err := s.loadMissionSession(profileID, sessionID)
	if err != nil {
		return LeadScope{}, missionSession{}, err
	}
	if !found || m.lead == "" || m.lead != l.Caller.ConversationID {
		return LeadScope{}, missionSession{}, errMissionRefused
	}
	return l, m, nil
}

// openForLead says whether the lead may still change the session's children: draft or running, not archived.
func (m missionSession) openForLead() error {
	if m.archived || (m.state != "draft" && m.state != "running") {
		return fmt.Errorf("%w: the mission is %s; marks and ceilings change only while it is a draft or running", ErrInvalid, m.state)
	}
	return nil
}

func modeRank(m string) int {
	switch m {
	case ModeOff:
		return 0
	case ModeMarked:
		return 1
	case ModeAll:
		return 2
	}
	return -1
}

// GetPolicy returns the policy of a task session: the stored one, or the defaults (Stored false). It is a read
// for the host and the operator surface only: it takes no caller, so a tool must not expose it to a session; the lead is the session's conversation as task_sessions says.
func (s *Store) GetPolicy(profileID string, sessionID int64) (Policy, error) {
	p := Policy{ProfileID: profileID, SessionID: sessionID, Mode: ModeMarked, MaxChildren: defaultPolicyMaxChildren, SharePct: defaultPolicySharePct}
	err := s.db.QueryRow(`SELECT lead_conversation_id, mode, max_children, share_pct, default_budget_usd, raised_by, updated_at, rev FROM coder_mission_policy
		WHERE profile_id = ? AND session_id = ?`, profileID, sessionID).Scan(&p.LeadConversationID, &p.Mode, &p.MaxChildren, &p.SharePct, &p.DefaultBudgetUSD, &p.RaisedBy, &p.UpdatedAt, &p.Rev)
	if err == nil {
		p.Stored = true
		return p, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return p, err
	}
	m, found, err := s.loadMissionSession(profileID, sessionID)
	if err != nil {
		return p, err
	}
	if !found {
		return p, fmt.Errorf("%w: no such mission", ErrInvalid)
	}
	p.LeadConversationID = m.lead
	return p, nil
}

// SetPolicy changes the policy of a task session, as the lead or as the operator.
//
// The lead may raise max_children up to the lead session's own max_children setting and share_pct up to
// ShareHardMax, may lower anything, and may switch the mode between off and marked or lower it from all.
// The operator may set the mode freely (mode "all" is the operator's alone) and the same numbers within the
// hard limits. Every changed field writes an audit row naming who changed it.
func (s *Store) SetPolicy(sc CallerScope, profileID string, sessionID int64, patch PolicyPatch) (Policy, error) {
	var who string
	var lead bool
	var m missionSession
	switch v := sc.(type) {
	case OperatorScope:
		who = "operator"
		var found bool
		var err error
		if m, found, err = s.loadMissionSession(profileID, sessionID); err != nil {
			return Policy{}, err
		} else if !found || m.archived || m.lead == "" {
			return Policy{}, fmt.Errorf("%w: no open mission with a lead conversation", ErrInvalid)
		}
	case LeadScope:
		l, sess, err := s.leadOf(v, profileID, sessionID)
		if err != nil {
			return Policy{}, err
		}
		if err := sess.openForLead(); err != nil {
			return Policy{}, err
		}
		who, lead, m = "lead:"+l.Caller.ConversationID, true, sess
	default:
		return Policy{}, errMissionRefused
	}
	plan := func(cur Policy) (Policy, []MissionEvent, error) {
		next := cur
		next.LeadConversationID = m.lead
		var notes []MissionEvent
		note := func(kind, text string) { notes = append(notes, MissionEvent{Kind: kind, Who: who, Note: text}) }
		raised := false

		if patch.Mode != nil {
			if modeRank(*patch.Mode) < 0 {
				return Policy{}, nil, fmt.Errorf("%w: mode must be off, marked or all", ErrInvalid)
			}
			if lead && *patch.Mode == ModeAll && cur.Mode != ModeAll {
				return Policy{}, nil, fmt.Errorf("%w: a lead can switch between off and marked, and lower from all, but only the operator turns on mode all", ErrOperatorOnly)
			}
			if *patch.Mode != cur.Mode {
				note("mode", fmt.Sprintf("mode %s -> %s", cur.Mode, *patch.Mode))
				next.Mode = *patch.Mode
			}
		}
		if patch.MaxChildren != nil {
			n := *patch.MaxChildren
			if n < 0 || n > policyMaxChildrenLimit {
				return Policy{}, nil, fmt.Errorf("%w: max_children must be between 0 and %d", ErrInvalid, policyMaxChildrenLimit)
			}
			if lead && n > cur.MaxChildren {
				ps, err := s.GetSettings(profileID, m.lead)
				if err != nil {
					return Policy{}, nil, err
				}
				if n > ps.MaxChildren {
					return Policy{}, nil, fmt.Errorf("%w: max_children %d is above the operator's limit of %d for this session", ErrLimit, n, ps.MaxChildren)
				}
			}
			if n != cur.MaxChildren {
				kind := "lower"
				if n > cur.MaxChildren {
					kind, raised = "raise", true
				}
				note(kind, fmt.Sprintf("max_children %d -> %d", cur.MaxChildren, n))
				next.MaxChildren = n
			}
		}
		if patch.SharePct != nil {
			n := *patch.SharePct
			if n < 1 || n > ShareHardMax {
				return Policy{}, nil, fmt.Errorf("%w: share_pct must be between 1 and %d; to stop spawning children use mode off", ErrLimit, ShareHardMax)
			}
			if n != cur.SharePct {
				kind := "lower"
				if n > cur.SharePct {
					kind, raised = "raise", true
				}
				note(kind, fmt.Sprintf("share_pct %d -> %d", cur.SharePct, n))
				next.SharePct = n
			}
		}
		if patch.DefaultBudgetUSD != nil {
			b := *patch.DefaultBudgetUSD
			if b < 0 || math.IsNaN(b) || math.IsInf(b, 0) {
				return Policy{}, nil, fmt.Errorf("%w: the default budget must be a number of at least 0", ErrInvalid)
			}
			if b != cur.DefaultBudgetUSD {
				note("budget", fmt.Sprintf("default_budget_usd %.2f -> %.2f", cur.DefaultBudgetUSD, b)) // a wish; the room check still bounds the carve
				next.DefaultBudgetUSD = b
			}
		}
		if raised {
			next.RaisedBy = who
		}
		return next, notes, nil
	}
	// Read, decide and write against the revision that was read: a concurrent writer (the operator, or the
	// lead) makes the write fail and the decision is made again from what it left, so neither can write back
	// a stale copy of a field it did not mean to change.
	tries := 8
	if !lead {
		tries = 64 // the operator's write is not starved by a lead that keeps writing
	}
	for attempt := 0; attempt < tries; attempt++ {
		cur, err := s.GetPolicy(profileID, sessionID)
		if err != nil {
			return Policy{}, err
		}
		next, notes, err := plan(cur)
		if err != nil {
			return Policy{}, err
		}
		if s.afterPolicyRead != nil {
			s.afterPolicyRead()
		}
		ok, err := s.writePolicy(cur, next, notes)
		if err != nil {
			return Policy{}, err
		}
		if ok {
			return next, nil
		}
	}
	return Policy{}, fmt.Errorf("%w: the policy kept changing under this write; try again", ErrInvalid)
}

// writePolicy stores next if the policy is still at the revision cur was read at (or still absent, when cur
// was not stored), with its audit rows, in one transaction. false means someone else wrote first.
func (s *Store) writePolicy(cur, next Policy, notes []MissionEvent) (bool, error) {
	now := s.stamp()
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var res sql.Result
	if cur.Stored {
		res, err = tx.Exec(`UPDATE coder_mission_policy SET lead_conversation_id = ?, mode = ?, max_children = ?, share_pct = ?, default_budget_usd = ?, raised_by = ?, updated_at = ?, rev = rev + 1
			WHERE profile_id = ? AND session_id = ? AND rev = ?`,
			next.LeadConversationID, next.Mode, next.MaxChildren, next.SharePct, next.DefaultBudgetUSD, next.RaisedBy, now, cur.ProfileID, cur.SessionID, cur.Rev)
	} else {
		res, err = tx.Exec(`INSERT OR IGNORE INTO coder_mission_policy (profile_id, session_id, lead_conversation_id, mode, max_children, share_pct, default_budget_usd, raised_by, updated_at, rev)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`,
			cur.ProfileID, cur.SessionID, next.LeadConversationID, next.Mode, next.MaxChildren, next.SharePct, next.DefaultBudgetUSD, next.RaisedBy, now)
	}
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	for _, e := range notes {
		if err := addMissionEvent(tx, cur.ProfileID, cur.SessionID, 0, e.Kind, e.Who, e.Note, now); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// addMissionEvent writes an audit row. Rows of the lead are bounded per kind: the newest MaxMissionEvents of
// each of mark, raise, lower, mode, budget and the rest, so a loop of one kind cannot push another out (a flood
// of raises can only push out earlier raises of the lead). The operator's and the host's rows are never rotated out (the operator is a person, and the host's
// are bounded by tasks times attempts).
func addMissionEvent(db execer, profileID string, sessionID, taskID int64, kind, who, note, at string) error {
	if len(note) > 300 {
		note = note[:300]
	}
	if _, err := db.Exec(`INSERT INTO coder_mission_events (profile_id, session_id, task_id, kind, who, note, at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		profileID, sessionID, taskID, kind, who, note, at); err != nil {
		return err
	}
	if !strings.HasPrefix(who, "lead:") {
		return nil
	}
	const group = `profile_id = ? AND session_id = ? AND who LIKE 'lead:%' AND kind = ?`
	_, err := db.Exec(`DELETE FROM coder_mission_events WHERE `+group+` AND id NOT IN
		(SELECT id FROM coder_mission_events WHERE `+group+` ORDER BY id DESC LIMIT ?)`,
		profileID, sessionID, kind, profileID, sessionID, kind, MaxMissionEvents)
	return err
}

// addMarkEvent writes the audit row of a mark, or, when the newest row of that task is an identical mark by
// the same lead (same size and budget), only refreshes its time: marking the same way again and again leaves
// one row, while a change of size or budget is a row of its own.
func addMarkEvent(db execer, profileID string, sessionID, taskID int64, who, note, at string) error {
	res, err := db.Exec(`UPDATE coder_mission_events SET at = ? WHERE kind = 'mark' AND who = ? AND note = ? AND id =
		(SELECT MAX(id) FROM coder_mission_events WHERE profile_id = ? AND session_id = ? AND task_id = ?)`,
		at, who, note, profileID, sessionID, taskID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	return addMissionEvent(db, profileID, sessionID, taskID, "mark", who, note, at)
}

// Events returns a task session's audit rows, newest first.
func (s *Store) Events(profileID string, sessionID int64, limit int) ([]MissionEvent, error) {
	if limit <= 0 || limit > 4*MaxMissionEvents {
		limit = 4 * MaxMissionEvents
	}
	rows, err := s.db.Query(`SELECT id, session_id, task_id, kind, who, note, at FROM coder_mission_events WHERE profile_id = ? AND session_id = ? ORDER BY id DESC LIMIT ?`,
		profileID, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MissionEvent
	for rows.Next() {
		var e MissionEvent
		if err := rows.Scan(&e.ID, &e.SessionID, &e.TaskID, &e.Kind, &e.Who, &e.Note, &e.At); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// taskMarkable checks, read-only, that the task is a plain task of this session that is still open to work.
// A task of another session or profile reads the same as one that does not exist.
func (s *Store) taskMarkable(profileID string, sessionID, taskID int64) error {
	var status, kind string
	err := s.db.QueryRow(`SELECT status, kind FROM tasks WHERE id = ? AND profile_id = ? AND session_id = ?`, taskID, profileID, sessionID).Scan(&status, &kind)
	if errors.Is(err, sql.ErrNoRows) {
		return errMissionRefused
	}
	if err != nil {
		return err
	}
	if kind != "task" || status == string(tasks.StatusDone) || status == string(tasks.StatusArchived) {
		return fmt.Errorf("%w: only a task that is not done or archived can run in its own session", ErrInvalid)
	}
	return nil
}

const childColumns = `id, profile_id, session_id, task_id, lead_conversation_id, child_conversation_id, origin, size, budget_usd, reserved_usd, state, end_reason, attempts, created_at, updated_at, spawned_at, ended_at`

func scanChild(r interface{ Scan(...any) error }) (ChildRow, error) {
	var c ChildRow
	err := r.Scan(&c.ID, &c.ProfileID, &c.SessionID, &c.TaskID, &c.LeadConversationID, &c.ChildConversationID, &c.Origin, &c.Size, &c.BudgetUSD, &c.ReservedUSD, &c.State, &c.EndReason, &c.Attempts, &c.CreatedAt, &c.UpdatedAt, &c.SpawnedAt, &c.EndedAt)
	return c, err
}

// Child returns the row of a task, if it has one.
func (s *Store) Child(profileID string, sessionID, taskID int64) (ChildRow, bool, error) {
	c, err := scanChild(s.db.QueryRow(`SELECT `+childColumns+` FROM coder_mission_children WHERE profile_id = ? AND session_id = ? AND task_id = ?`, profileID, sessionID, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return ChildRow{}, false, nil
	}
	return c, err == nil, err
}

func (s *Store) childrenWhere(where string, args ...any) ([]ChildRow, error) {
	rows, err := s.db.Query(`SELECT `+childColumns+` FROM coder_mission_children WHERE `+where+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChildRow
	for rows.Next() {
		c, err := scanChild(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ChildrenOfSession lists the rows of a task session, oldest first.
func (s *Store) ChildrenOfSession(profileID string, sessionID int64) ([]ChildRow, error) {
	return s.childrenWhere(`profile_id = ? AND session_id = ?`, profileID, sessionID)
}

// ChildrenOfLead lists the rows whose lead is the conversation.
func (s *Store) ChildrenOfLead(profileID, leadConversationID string) ([]ChildRow, error) {
	return s.childrenWhere(`profile_id = ? AND lead_conversation_id = ?`, profileID, leadConversationID)
}

// ChildCounts counts a task session's rows for the digest.
func (s *Store) ChildCounts(profileID string, sessionID int64) (ChildCounts, error) {
	var c ChildCounts
	err := s.db.QueryRow(`SELECT
		COALESCE(SUM(state IN ('spawning','spawned')), 0), COALESCE(SUM(state = 'marked'), 0), COALESCE(SUM(state = 'failed'), 0)
		FROM coder_mission_children WHERE profile_id = ? AND session_id = ?`, profileID, sessionID).Scan(&c.Live, &c.Queued, &c.Failed)
	return c, err
}

// Mark records that the lead wants a task to run in a session of its own. It is the lead's alone (see the
// file comment). A mark in a draft session is stored and starts nothing: Reserve refuses until the plan is
// approved. Marking again a task whose child ended or failed queues it for another try (attempts are kept);
// a task whose child is starting or running cannot be re-marked.
func (s *Store) Mark(sc CallerScope, profileID string, sessionID, taskID int64, opt MarkOptions) (ChildRow, error) {
	l, m, err := s.leadOf(sc, profileID, sessionID)
	if err != nil {
		return ChildRow{}, err
	}
	if err := s.taskMarkable(profileID, sessionID, taskID); err != nil {
		return ChildRow{}, err
	}
	if err := m.openForLead(); err != nil {
		return ChildRow{}, err
	}
	if opt.Size != "" && opt.Size != sizeLarge && opt.Size != sizeSmall {
		return ChildRow{}, fmt.Errorf("%w: size must be large or small", ErrInvalid)
	}
	if opt.BudgetUSD < 0 || math.IsNaN(opt.BudgetUSD) || math.IsInf(opt.BudgetUSD, 0) {
		return ChildRow{}, fmt.Errorf("%w: the budget must be a number of at least 0", ErrInvalid)
	}
	pol, err := s.GetPolicy(profileID, sessionID)
	if err != nil {
		return ChildRow{}, err
	}
	if pol.Mode == ModeOff {
		return ChildRow{}, fmt.Errorf("%w: child sessions are off for this mission", ErrInvalid)
	}
	who, now := "lead:"+l.Caller.ConversationID, s.stamp()
	tx, err := s.db.Begin()
	if err != nil {
		return ChildRow{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`INSERT OR IGNORE INTO coder_mission_policy (profile_id, session_id, lead_conversation_id, mode, max_children, share_pct, default_budget_usd, raised_by, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, '', ?)`, profileID, sessionID, m.lead, pol.Mode, pol.MaxChildren, pol.SharePct, pol.DefaultBudgetUSD, now); err != nil {
		return ChildRow{}, err
	}
	res, err := tx.Exec(`INSERT INTO coder_mission_children (profile_id, session_id, task_id, lead_conversation_id, origin, size, budget_usd, state, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'lead', ?, ?, 'marked', ?, ?)
		ON CONFLICT(profile_id, session_id, task_id) DO UPDATE SET size = excluded.size, budget_usd = excluded.budget_usd, state = 'marked',
			end_reason = '', ended_at = '', child_conversation_id = '', pending_conversation_id = '', reserved_usd = 0, lead_conversation_id = excluded.lead_conversation_id, updated_at = excluded.updated_at
		WHERE coder_mission_children.state IN ('marked', 'ended', 'failed', 'gone', 'released')`,
		profileID, sessionID, taskID, m.lead, opt.Size, opt.BudgetUSD, now, now)
	if err != nil {
		return ChildRow{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ChildRow{}, fmt.Errorf("%w: that task already runs in its own session", ErrInvalid)
	}
	if err := addMarkEvent(tx, profileID, sessionID, taskID, who, fmt.Sprintf("size=%s budget=%.2f", opt.Size, opt.BudgetUSD), now); err != nil {
		return ChildRow{}, err
	}
	if err := tx.Commit(); err != nil {
		return ChildRow{}, err
	}
	c, _, err := s.Child(profileID, sessionID, taskID)
	return c, err
}

// Reserve takes the task for starting: marked -> spawning, attempts + 1, in one statement under the
// (profile, session, task) key, so two callers racing for one task get one winner. It refuses unless the
// mission is running (a draft waits for the plan's approval), the policy allows children, there is room,
// the ceiling holds in the statement itself, and the task has attempts left. In mode "all" an unmarked task
// gets its row here (origin rule).
func (s *Store) Reserve(sc CallerScope, profileID string, sessionID, taskID int64) (ChildRow, error) {
	l, m, err := s.leadOf(sc, profileID, sessionID)
	if err != nil {
		return ChildRow{}, err
	}
	if err := s.taskMarkable(profileID, sessionID, taskID); err != nil {
		return ChildRow{}, err
	}
	if m.archived || m.state != "running" {
		return ChildRow{}, fmt.Errorf("%w: the mission is %s; no task starts in its own session until it is running", ErrInvalid, m.state)
	}
	pol, err := s.GetPolicy(profileID, sessionID)
	if err != nil {
		return ChildRow{}, err
	}
	cur, have, err := s.Child(profileID, sessionID, taskID)
	if err != nil {
		return ChildRow{}, err
	}
	switch {
	case pol.Mode == ModeOff:
		return ChildRow{}, fmt.Errorf("%w: child sessions are off for this mission", ErrInvalid)
	case !have && pol.Mode != ModeAll:
		return ChildRow{}, fmt.Errorf("%w: the task is not marked", ErrInvalid)
	case have && cur.Attempts >= MaxChildAttempts:
		return ChildRow{}, fmt.Errorf("%w: the task was started %d times already", ErrLimit, cur.Attempts)
	}
	carve := s.childBudget(pol, cur.BudgetUSD, m.lead)
	room, err := s.RoomFor(profileID, sessionID, carve)
	if err != nil {
		return ChildRow{}, err
	}
	if !room.OK {
		return ChildRow{}, fmt.Errorf("%w: %s", ErrLimit, room.Reason)
	}
	now := s.stamp()
	if !have { // mode all
		if _, err := s.db.Exec(`INSERT OR IGNORE INTO coder_mission_children (profile_id, session_id, task_id, lead_conversation_id, origin, state, created_at, updated_at)
			VALUES (?, ?, ?, ?, 'rule', 'marked', ?, ?)`, profileID, sessionID, taskID, m.lead, now, now); err != nil {
			return ChildRow{}, err
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return ChildRow{}, err
	}
	defer func() { _ = tx.Rollback() }()
	shareCap := -1.0
	if room.Capped {
		shareCap = room.AllowedUSD
	}
	won, err := takeReservation(tx, profileID, sessionID, taskID, m.lead, room.Ceiling, carve, shareCap, now)
	if err != nil {
		return ChildRow{}, err
	}
	if !won {
		return ChildRow{}, fmt.Errorf("%w: the task is already starting, or the child limit or the budget share was just reached", ErrLimit)
	}
	if err := addMissionEvent(tx, profileID, sessionID, taskID, "spawn", "lead:"+l.Caller.ConversationID, fmt.Sprintf("reserved $%.2f", carve), now); err != nil {
		return ChildRow{}, err
	}
	if err := tx.Commit(); err != nil {
		return ChildRow{}, err
	}
	c, _, err := s.Child(profileID, sessionID, taskID)
	return c, err
}

// takeReservation is the one statement that moves a marked task to spawning and stores its carve on the row.
// It holds the attempts limit, the lead's ceiling and, for a lead with a budget (shareCap >= 0), the share
// itself, and it needs the mode not off and the mission running: what the lead's children already hold (their sessions' caps) plus what reserved children will
// hold (reserved_usd of spawning rows, whose sessions do not exist yet) plus this carve must fit the cap.
// So callers racing for the last slot, or reserving twice before the first child is created, cannot both win.
// (A spawning row whose session already holds its carve is counted twice until it is linked: on the safe side.)
func takeReservation(db execer, profileID string, sessionID, taskID int64, lead string, ceiling int, carve, shareCap float64, now string) (bool, error) {
	res, err := db.Exec(`UPDATE coder_mission_children SET state = 'spawning', attempts = attempts + 1, reserved_usd = ?, updated_at = ?
		WHERE profile_id = ? AND session_id = ? AND task_id = ? AND lead_conversation_id = ? AND state = 'marked' AND attempts < ?
		AND (SELECT COUNT(*) FROM coder_mission_children WHERE profile_id = ? AND lead_conversation_id = ? AND state IN ('spawning', 'spawned')) < ?
		AND (? < 0 OR ? + (SELECT COALESCE(SUM(budget_usd), 0) FROM coder_sessions WHERE profile_id = ? AND parent_conversation_id = ?)
			+ (SELECT COALESCE(SUM(reserved_usd), 0) FROM coder_mission_children WHERE profile_id = ? AND lead_conversation_id = ? AND state = 'spawning') <= ? + 1e-9)
		AND NOT EXISTS (SELECT 1 FROM coder_mission_policy WHERE profile_id = ? AND session_id = ? AND mode = 'off')
		AND EXISTS (SELECT 1 FROM task_sessions WHERE profile_id = ? AND id = ? AND state = 'running' AND archived_at = '')`,
		carve, now, profileID, sessionID, taskID, lead, MaxChildAttempts,
		profileID, lead, ceiling,
		shareCap, carve, profileID, lead, profileID, lead, shareCap,
		profileID, sessionID, profileID, sessionID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SetSpawned links the child conversation to a reserved task: spawning -> spawned. The conversation must be
// a descendant of the lead and not linked to another task.
func (s *Store) SetSpawned(sc CallerScope, profileID string, sessionID, taskID int64, childConversationID string) (ChildRow, error) {
	l, _, err := s.leadOf(sc, profileID, sessionID)
	if err != nil {
		return ChildRow{}, err
	}
	if childConversationID == "" || !s.IsDescendant(profileID, l.Caller.ConversationID, childConversationID) {
		return ChildRow{}, fmt.Errorf("%w: %s", ErrNotYours, childConversationID)
	}
	now := s.stamp()
	res, err := s.db.Exec(`UPDATE coder_mission_children SET state = 'spawned', child_conversation_id = ?, spawned_at = ?, updated_at = ?
		WHERE profile_id = ? AND session_id = ? AND task_id = ? AND lead_conversation_id = ? AND state = 'spawning'`,
		childConversationID, now, now, profileID, sessionID, taskID, l.Caller.ConversationID)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return ChildRow{}, fmt.Errorf("%w: that conversation is linked to a task already", ErrInvalid)
		}
		return ChildRow{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ChildRow{}, fmt.Errorf("%w: the task is not waiting for its session", ErrInvalid)
	}
	c, _, err := s.Child(profileID, sessionID, taskID)
	return c, err
}

// NotePending records, as soon as the spawn path has created the child's conversation (the onCreated hook),
// which conversation the reserved task is about to own, and refreshes the row's clock. From then on
// ResetStaleSpawning ends the row instead of handing the task to a second child.
func (s *Store) NotePending(sc CallerScope, profileID string, sessionID, taskID int64, childConversationID string) error {
	l, _, err := s.leadOf(sc, profileID, sessionID)
	if err != nil {
		return err
	}
	if childConversationID == "" || !s.IsDescendant(profileID, l.Caller.ConversationID, childConversationID) {
		return fmt.Errorf("%w: %s", ErrNotYours, childConversationID)
	}
	return s.touchSpawning(profileID, sessionID, taskID, l.Caller.ConversationID, childConversationID)
}

// Touch refreshes the clock of a reserved task while its spawn is still running, so a slow spawn is not taken
// for a dead one. The spawn path calls it between its steps.
func (s *Store) Touch(sc CallerScope, profileID string, sessionID, taskID int64) error {
	l, _, err := s.leadOf(sc, profileID, sessionID)
	if err != nil {
		return err
	}
	return s.touchSpawning(profileID, sessionID, taskID, l.Caller.ConversationID, "")
}

func (s *Store) touchSpawning(profileID string, sessionID, taskID int64, lead, pending string) error {
	res, err := s.db.Exec(`UPDATE coder_mission_children SET updated_at = ?, pending_conversation_id = CASE WHEN ? <> '' THEN ? ELSE pending_conversation_id END
		WHERE profile_id = ? AND session_id = ? AND task_id = ? AND lead_conversation_id = ? AND state = 'spawning'`,
		s.stamp(), pending, pending, profileID, sessionID, taskID, lead)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: the task is not waiting for its session", ErrInvalid)
	}
	return nil
}

// ResetStaleSpawning handles reservations nobody has touched for maxAge (the process died in between). A row
// that never got a conversation goes back to marked (reset). A row whose conversation was created but never
// linked is ended as expired with that conversation attached (ended), so it is not started a second time and
// the spawn's orphan can be stopped; its carve is returned. It is the host's.
func (s *Store) ResetStaleSpawning(maxAge time.Duration) (reset int, ended []string, err error) {
	old := s.now().Add(-maxAge).UTC().Format(timeLayout)
	res, err := s.db.Exec(`UPDATE coder_mission_children SET state = 'marked', reserved_usd = 0, updated_at = ?
		WHERE state = 'spawning' AND child_conversation_id = '' AND pending_conversation_id = '' AND updated_at < ?`, s.stamp(), old)
	if err != nil {
		return 0, nil, err
	}
	n, _ := res.RowsAffected()
	reset = int(n)
	rows, err := s.db.Query(`SELECT profile_id, session_id, task_id, pending_conversation_id FROM coder_mission_children
		WHERE state = 'spawning' AND child_conversation_id = '' AND pending_conversation_id <> '' AND updated_at < ?`, old)
	if err != nil {
		return reset, nil, err
	}
	type orphan struct {
		profile, conv string
		sid, tid      int64
	}
	var list []orphan
	for rows.Next() {
		var o orphan
		if err := rows.Scan(&o.profile, &o.sid, &o.tid, &o.conv); err != nil {
			rows.Close()
			return reset, nil, err
		}
		list = append(list, o)
	}
	rows.Close()
	for _, o := range list {
		attached, err := s.expireOrphan(o.profile, o.sid, o.tid, o.conv)
		if err != nil {
			return reset, ended, err
		}
		if attached {
			ended = append(ended, o.conv)
		}
	}
	return reset, ended, nil
}

// expireOrphan fails one stale reservation whose spawn created a conversation but never linked it, in one
// transaction with its audit row and the lowering of the orphan's cap. The conversation is attached to the row
// (attached true: the host should stop it) unless another task already owns it, in which case the row still
// fails, with no conversation, and the other task's session is left alone. Either way the reservation stops
// holding a slot and its reserved carve.
func (s *Store) expireOrphan(profileID string, sid, tid int64, conv string) (attached bool, err error) {
	spent, err := s.Spent(profileID, conv)
	if err != nil {
		return false, err
	}
	now := s.stamp()
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	r, err := tx.Exec(`UPDATE coder_mission_children SET state = 'failed', end_reason = 'expired', child_conversation_id = pending_conversation_id, reserved_usd = 0, ended_at = ?, updated_at = ?
		WHERE state = 'spawning' AND profile_id = ? AND session_id = ? AND task_id = ? AND child_conversation_id = '' AND pending_conversation_id = ?
		AND NOT EXISTS (SELECT 1 FROM coder_mission_children o WHERE o.profile_id = ? AND o.child_conversation_id = ?)`, now, now, profileID, sid, tid, conv, profileID, conv)
	if err != nil {
		return false, err
	}
	if k, _ := r.RowsAffected(); k > 0 {
		attached = true
	} else {
		r, err = tx.Exec(`UPDATE coder_mission_children SET state = 'failed', end_reason = 'expired', reserved_usd = 0, ended_at = ?, updated_at = ?
			WHERE state = 'spawning' AND profile_id = ? AND session_id = ? AND task_id = ? AND child_conversation_id = '' AND pending_conversation_id = ?`, now, now, profileID, sid, tid, conv)
		if err != nil {
			return false, err
		}
		if k, _ := r.RowsAffected(); k == 0 {
			return false, nil
		}
	}
	if err := addMissionEvent(tx, profileID, sid, tid, "end", "host", "expired: the spawn never linked its conversation", now); err != nil {
		return false, err
	}
	if attached {
		if err := s.releaseCarveIn(tx, profileID, conv, spent); err != nil {
			return false, err
		}
	}
	return attached, tx.Commit()
}

// endState says which terminal state a reason leads to.
func endState(reason string) (string, bool) {
	switch reason {
	case EndDone, EndStopped, EndLeadStop, EndCascade:
		return ChildEnded, true
	case EndNoFinish, EndBudget, EndExpired:
		return ChildFailed, true
	case EndDeleted:
		return ChildGone, true
	}
	return "", false
}

// End closes the live row of a child conversation with a reason and returns its carve to the lead. It is
// the host's (it has no caller); ended is false when the conversation has no live row, so a second call
// does nothing.
func (s *Store) End(profileID, childConversationID, reason string) (bool, error) {
	state, ok := endState(reason)
	if !ok {
		return false, fmt.Errorf("%w: %q is not an end reason", ErrInvalid, reason)
	}
	const match = `profile_id = ? AND (child_conversation_id = ? OR pending_conversation_id = ?) AND state IN ('spawning', 'spawned')`
	var sid, tid int64
	err := s.db.QueryRow(`SELECT session_id, task_id FROM coder_mission_children WHERE `+match, profileID, childConversationID, childConversationID).Scan(&sid, &tid)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	spent := 0.0
	if state != ChildGone {
		if spent, err = s.Spent(profileID, childConversationID); err != nil {
			return false, err
		}
	}
	// The row's state, its audit row and the lowering of the child's cap stand or fall together: a failure
	// leaves the row live, and the host's next pass ends it again.
	now := s.stamp()
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.Exec(`UPDATE coder_mission_children SET state = ?, end_reason = ?, ended_at = ?, updated_at = ? WHERE `+match,
		state, reason, now, now, profileID, childConversationID, childConversationID)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	kind := "end"
	if reason == EndCascade || reason == EndLeadStop {
		kind = "cascade"
	}
	if err := addMissionEvent(tx, profileID, sid, tid, kind, "host", reason, now); err != nil {
		return false, err
	}
	if state != ChildGone {
		if err := s.releaseCarveIn(tx, profileID, childConversationID, spent); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// MarkGone records that a child's conversation was deleted.
func (s *Store) MarkGone(profileID, childConversationID string) (bool, error) {
	return s.End(profileID, childConversationID, EndDeleted)
}

// releaseCarve returns what a finished child did not spend to its lead by lowering the child's cap to what it
// spent. It only lowers (the statement needs the cap to be above the new one, so a cap is never raised and a
// child that spent more than its cap is left alone), never sets 0 (0 means uncapped; the floor is minCap),
// leaves an uncapped child alone, and touches only a child session of the row's lead. The lead's carved
// total drops by the same amount, because Carved sums the children's caps.
func (s *Store) releaseCarve(profileID, childConversationID string) error {
	spent, err := s.Spent(profileID, childConversationID)
	if err != nil {
		return err
	}
	return s.releaseCarveIn(s.db, profileID, childConversationID, spent)
}

// releaseCarveIn is releaseCarve with the spend already read, written through db (a transaction, when the
// lowering must stand or fall with the row's state change and its audit row).
func (s *Store) releaseCarveIn(db execer, profileID, childConversationID string, spent float64) error {
	newCap := math.Max(spent, minCap)
	_, err := db.Exec(`UPDATE coder_sessions SET budget_usd = ?, updated_at = ?
		WHERE profile_id = ? AND conversation_id = ? AND role = 'child' AND budget_usd > 0 AND budget_usd > ?
		AND parent_conversation_id IN (SELECT lead_conversation_id FROM coder_mission_children WHERE profile_id = ? AND (child_conversation_id = ? OR pending_conversation_id = ?))`,
		newCap, s.stamp(), profileID, childConversationID, newCap, profileID, childConversationID, childConversationID)
	return err
}

// childBudget is the carve to ask for: the task's wish, else the policy's default, else (for a lead with a
// cap) max(0.50, remaining x share / max_children / 100); 0 (no cap of its own) for a lead without one.
func (s *Store) childBudget(p Policy, wish float64, lead string) float64 {
	if wish > 0 {
		return wish
	}
	if p.DefaultBudgetUSD > 0 {
		return p.DefaultBudgetUSD
	}
	budget, err := s.Budget(p.ProfileID, lead)
	if err != nil || budget <= 0 {
		return 0
	}
	spent, _ := s.Spent(p.ProfileID, lead)
	n := p.MaxChildren
	if n < 1 {
		n = 1
	}
	return math.Max(minDerivedCarveUSD, (budget-spent)*float64(p.SharePct)/float64(n)/100)
}

// DefaultChildBudget is the carve a task without its own budget would get (see childBudget).
func (s *Store) DefaultChildBudget(profileID string, sessionID int64) (float64, error) {
	p, err := s.GetPolicy(profileID, sessionID)
	if err != nil {
		return 0, err
	}
	return s.childBudget(p, 0, p.LeadConversationID), nil
}

// RoomFor says whether one more child with a carve of newUSD fits now:
//   - live mission children (spawning or spawned) < min(policy.max_children, the lead's max_children setting);
//   - children of the lead running a turn < the lead's max_concurrent setting;
//   - for a lead with a cap: carved + newUSD <= share_pct% of (budget - spent), and the carve is positive.
//     A lead with no cap has no share to hold.
func (s *Store) RoomFor(profileID string, sessionID int64, newUSD float64) (Room, error) {
	var r Room
	pol, err := s.GetPolicy(profileID, sessionID)
	if err != nil {
		return r, err
	}
	if pol.LeadConversationID == "" {
		return r, fmt.Errorf("%w: the mission has no lead conversation", ErrInvalid)
	}
	if newUSD < 0 || math.IsNaN(newUSD) || math.IsInf(newUSD, 0) {
		return r, fmt.Errorf("%w: the budget must be a number of at least 0", ErrInvalid)
	}
	ps, err := s.GetSettings(profileID, pol.LeadConversationID)
	if err != nil {
		return r, err
	}
	r.Ceiling, r.MaxConcurrent = min(pol.MaxChildren, ps.MaxChildren), ps.MaxConcurrent
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM coder_mission_children WHERE profile_id = ? AND lead_conversation_id = ? AND state IN ('spawning', 'spawned')`,
		profileID, pol.LeadConversationID).Scan(&r.Live); err != nil {
		return r, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM coder_sessions WHERE profile_id = ? AND parent_conversation_id = ? AND role = 'child' AND state = 'running'`,
		profileID, pol.LeadConversationID).Scan(&r.Running); err != nil {
		return r, err
	}
	switch {
	case pol.Mode == ModeOff:
		r.Reason = "child sessions are off for this mission"
	case r.Live >= r.Ceiling:
		r.Reason = fmt.Sprintf("child sessions full (%d of %d)", r.Live, r.Ceiling)
	case r.Running >= r.MaxConcurrent:
		r.Reason = fmt.Sprintf("%d child turns running (max_concurrent is %d)", r.Running, r.MaxConcurrent)
	default:
		budget, err := s.Budget(profileID, pol.LeadConversationID)
		if err != nil {
			return r, err
		}
		if budget > 0 {
			spent, err := s.Spent(profileID, pol.LeadConversationID)
			if err != nil {
				return r, err
			}
			carved, err := s.Carved(profileID, pol.LeadConversationID)
			if err != nil {
				return r, err
			}
			var reserved float64 // reserved children have no session yet, so Carved does not hold their carve
			if err := s.db.QueryRow(`SELECT COALESCE(SUM(reserved_usd), 0) FROM coder_mission_children WHERE profile_id = ? AND lead_conversation_id = ? AND state = 'spawning'`,
				profileID, pol.LeadConversationID).Scan(&reserved); err != nil {
				return r, err
			}
			carved += reserved
			allowed := float64(pol.SharePct) / 100 * (budget - spent)
			r.Capped, r.AllowedUSD = true, allowed
			if newUSD <= 0 {
				r.Reason = "a lead with a budget carves a positive amount for each child"
			} else if carved+newUSD > allowed+1e-9 {
				r.Reason = fmt.Sprintf("a carve of $%.2f would pass %d%% of the lead's remaining budget ($%.2f carved, $%.2f allowed)", newUSD, pol.SharePct, carved, allowed)
			}
		}
	}
	r.OK = r.Reason == ""
	return r, nil
}

// ChildDisplay says how a child shows: a bubble when its size is large, when nobody said and its carve is
// at least LargeChildBudgetUSD, when it has an open question, or when it failed; else a node under the lead.
func ChildDisplay(size string, carveUSD float64, failed, openQuestion bool) string {
	switch {
	case failed || openQuestion || size == sizeLarge:
		return displayBubble
	case size == "" && carveUSD >= LargeChildBudgetUSD:
		return displayBubble
	}
	return displayChart
}

// DisplayOf reads the child's carve and open questions and applies ChildDisplay.
func (s *Store) DisplayOf(c ChildRow) (string, error) {
	var carve float64
	var open int
	if c.ChildConversationID != "" {
		if err := s.db.QueryRow(`SELECT COALESCE((SELECT budget_usd FROM coder_sessions WHERE profile_id = ? AND conversation_id = ?), 0),
			(SELECT COUNT(*) FROM coder_session_questions WHERE profile_id = ? AND conversation_id = ? AND status = 'open')`,
			c.ProfileID, c.ChildConversationID, c.ProfileID, c.ChildConversationID).Scan(&carve, &open); err != nil {
			return "", err
		}
	}
	return ChildDisplay(c.Size, carve, c.State == ChildFailed, open > 0), nil
}
