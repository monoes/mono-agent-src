package ai

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

// Dynamic-org workers kept per conversation (#230, migration 060): the
// turn process saves each worker after its runs, and a later turn loads
// the latest ones as idle veterans its lead can message.

// OrgWorker is one worker kept for a conversation's later turns.
type OrgWorker struct {
	AgentID    string   `json:"agent_id"`
	ParentID   string   `json:"parent_id,omitempty"`
	TurnID     string   `json:"turn_id"`
	Role       string   `json:"role"`
	AgentType  string   `json:"agent_type,omitempty"`
	Category   string   `json:"category,omitempty"`
	Access     string   `json:"access"`
	Skills     []string `json:"skills,omitempty"`
	Runtime    string   `json:"runtime"`
	Model      string   `json:"model,omitempty"`
	Effort     string   `json:"effort,omitempty"`
	SessionID  string   `json:"session_id,omitempty"`
	Cwd        string   `json:"cwd,omitempty"`
	Report     string   `json:"report,omitempty"`
	Outcome    string   `json:"outcome,omitempty"`
	AllowSpawn bool     `json:"allow_spawn,omitempty"`
	UpdatedAt  string   `json:"updated_at"`
	// MissionSession and MissionTask are the mission task the worker worked (0: none), so the
	// operator can retire it (tasks.RetireWorker, migration 147).
	MissionSession int64 `json:"mission_session,omitempty"`
	MissionTask    int64 `json:"mission_task,omitempty"`
}

// OrgWorkerRetired is the outcome of a worker the operator retired: its session is dropped and it
// is never given one again, so the lead cannot resume it. Its report stays.
const OrgWorkerRetired = "retired"

// orgWorkerTime sorts as text: fixed width, to the nanosecond, so the
// workers of one turn keep their order.
const orgWorkerTime = "2006-01-02T15:04:05.000000000Z"

// SaveOrgWorker stores (or replaces) a conversation's worker. An empty
// report or session keeps the stored one: a run that failed before it
// reported still leaves the worker something to continue from.
func (s *AIStore) SaveOrgWorker(profileID, conversationID string, w OrgWorker) error {
	if profileID == "" {
		return ErrProfileRequired
	}
	skills, _ := json.Marshal(orEmptyList(w.Skills))
	allow := 0
	if w.AllowSpawn {
		allow = 1
	}
	_, err := s.db.Exec(`INSERT INTO ai_chat_org_workers
		(profile_id, conversation_id, agent_id, parent_id, turn_id, role, agent_type, category, access, skills,
		 runtime, model, effort, session_id, cwd, report, outcome, allow_spawn, updated_at, mission_session, mission_task)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(conversation_id, agent_id) DO UPDATE SET
		parent_id = excluded.parent_id, turn_id = excluded.turn_id, role = excluded.role, agent_type = excluded.agent_type,
		category = excluded.category, access = excluded.access, skills = excluded.skills, runtime = excluded.runtime,
		model = excluded.model, effort = excluded.effort,
		session_id = CASE WHEN ai_chat_org_workers.outcome = 'retired' THEN '' WHEN excluded.session_id = '' THEN ai_chat_org_workers.session_id ELSE excluded.session_id END,
		cwd = excluded.cwd,
		report = CASE WHEN excluded.report = '' THEN ai_chat_org_workers.report ELSE excluded.report END,
		outcome = CASE WHEN ai_chat_org_workers.outcome = 'retired' THEN 'retired' ELSE excluded.outcome END,
		allow_spawn = excluded.allow_spawn, updated_at = excluded.updated_at,
		mission_session = excluded.mission_session, mission_task = excluded.mission_task
		WHERE profile_id = excluded.profile_id`,
		profileID, conversationID, w.AgentID, w.ParentID, w.TurnID, w.Role, w.AgentType, w.Category, w.Access, string(skills),
		w.Runtime, w.Model, w.Effort, w.SessionID, w.Cwd, w.Report, w.Outcome, allow,
		time.Now().UTC().Format(orgWorkerTime), w.MissionSession, w.MissionTask)
	if err != nil {
		return fmt.Errorf("save org worker %s: %w", w.AgentID, err)
	}
	return nil
}

// ListOrgWorkers returns a conversation's latest limit workers (0 = all),
// oldest first.
func (s *AIStore) ListOrgWorkers(profileID, conversationID string, limit int) ([]OrgWorker, error) {
	if profileID == "" {
		return nil, ErrProfileRequired
	}
	if limit <= 0 {
		limit = -1
	}
	rows, err := s.db.Query(`SELECT agent_id, parent_id, turn_id, role, agent_type, category, access, skills,
		runtime, model, effort, session_id, cwd, report, outcome, allow_spawn, updated_at, mission_session, mission_task
		FROM ai_chat_org_workers WHERE profile_id = ? AND conversation_id = ?
		ORDER BY updated_at DESC, agent_id DESC LIMIT ?`, profileID, conversationID, limit)
	if err != nil {
		return nil, fmt.Errorf("list org workers: %w", err)
	}
	defer rows.Close()
	var out []OrgWorker
	for rows.Next() {
		var w OrgWorker
		var skills string
		var allow int
		if err := rows.Scan(&w.AgentID, &w.ParentID, &w.TurnID, &w.Role, &w.AgentType, &w.Category, &w.Access, &skills,
			&w.Runtime, &w.Model, &w.Effort, &w.SessionID, &w.Cwd, &w.Report, &w.Outcome, &allow, &w.UpdatedAt, &w.MissionSession, &w.MissionTask); err != nil {
			return nil, fmt.Errorf("read org worker: %w", err)
		}
		_ = json.Unmarshal([]byte(skills), &w.Skills)
		w.AllowSpawn = allow != 0
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list org workers: %w", err)
	}
	slices.Reverse(out)
	return out, nil
}

func orEmptyList(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
