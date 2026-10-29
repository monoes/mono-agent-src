package coderhost

import (
	"database/sql"
	"errors"
	"fmt"
)

// The reads the lead's tools and the conductor make over the mission child store (phase P-C of
// docs/mastermind/plans/2026-10-27-mission-child-sessions.md). Every read that names a mission takes the lead's
// scope and goes through leadOf, so a caller that is not the lead of that mission, holds a task-bound binding,
// or did not have its binding checked reads the same refusal (errMissionRefused) whether the mission exists or
// not. Nothing here writes: the writes stay in missionchild_store.go.

// LeadMission is the task session the lead is working: the newest draft or running, unarchived session whose
// conversation is the caller's. A caller that leads none reads the one refusal. It takes no session id, so a
// tool cannot be pointed at another mission.
func (s *Store) LeadMission(sc CallerScope, profileID string) (int64, error) {
	l, ok := sc.(LeadScope)
	if !ok || l.binding != BindingNone || l.Caller.ProfileID == "" || l.Caller.ConversationID == "" || l.Caller.ProfileID != profileID || profileID == "" {
		return 0, errMissionRefused
	}
	// the same session openMissionSession names (the newest open one); a paused newest is no session to write to
	var id int64
	var state string
	err := s.db.QueryRow(`SELECT id, state FROM task_sessions WHERE profile_id = ? AND conversation_id = ? AND archived_at = '' AND state IN ('draft', 'running', 'paused') AND `+LeadSessionSQL+` ORDER BY id DESC LIMIT 1`,
		profileID, l.Caller.ConversationID).Scan(&id, &state)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && state == "paused") {
		return 0, errMissionRefused
	}
	return id, err
}

// OperatorMissionOf is the newest unarchived task session of a lead conversation, for the operator's CLI. The
// person names the conversation, so a missing mission is a plain error.
func (s *Store) OperatorMissionOf(profileID, leadConversationID string) (int64, error) {
	var id int64
	err := s.db.QueryRow(`SELECT id FROM task_sessions WHERE profile_id = ? AND conversation_id = ? AND archived_at = '' AND `+LeadSessionSQL+` ORDER BY id DESC LIMIT 1`,
		profileID, leadConversationID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("%w: that conversation leads no mission", ErrInvalid)
	}
	return id, err
}

// LeadFacts is what the conductor needs to decide where one task runs.
type LeadFacts struct {
	Mode      string  `json:"mode"`
	Row       string  `json:"row,omitempty"` // the task's row state, "" when it has none
	EndReason string  `json:"endReason,omitempty"`
	Attempts  int     `json:"attempts"`
	Exhausted bool    `json:"exhausted"` // MaxChildAttempts reached
	Child     string  `json:"childConversationId,omitempty"`
	CarveUSD  float64 `json:"carveUsd"`
	Room      Room    `json:"room"`
	// MayHaveChildren is false for a lead that is itself a child or not a master: it falls back to in-turn
	// workers.
	MayHaveChildren bool `json:"mayHaveChildren"`
}

// LeadFacts reads the policy, the task's row and the room for the lead of a mission.
func (s *Store) LeadFacts(sc CallerScope, profileID string, sessionID, taskID int64) (LeadFacts, error) {
	l, _, err := s.leadOf(sc, profileID, sessionID)
	if err != nil {
		return LeadFacts{}, err
	}
	pol, err := s.GetPolicy(profileID, sessionID)
	if err != nil {
		return LeadFacts{}, err
	}
	f := LeadFacts{Mode: pol.Mode}
	row, have, err := s.Child(profileID, sessionID, taskID)
	if err != nil {
		return f, err
	}
	if have {
		f.Row, f.EndReason, f.Attempts, f.Child = row.State, row.EndReason, row.Attempts, row.ChildConversationID
		f.Exhausted = row.Attempts >= MaxChildAttempts
	}
	f.CarveUSD = s.childBudget(pol, row.BudgetUSD, pol.LeadConversationID)
	if f.Room, err = s.RoomFor(profileID, sessionID, f.CarveUSD); err != nil {
		return f, err
	}
	sess, ok, err := s.GetSession(profileID, l.Caller.ConversationID)
	if err != nil {
		return f, err
	}
	f.MayHaveChildren = ok && sess.Role == "master" && sess.Parent == "" && sess.State != StateStopped
	return f, nil
}

// LeadStatus is mission_child_status: the policy, the room, the rows (ids and numbers, never task text) and
// the newest audit rows of the lead's own mission.
type LeadStatus struct {
	SessionID int64          `json:"sessionId"`
	Policy    Policy         `json:"policy"`
	Room      Room           `json:"room"`
	Counts    ChildCounts    `json:"counts"`
	Children  []ChildRow     `json:"children"`
	Events    []MissionEvent `json:"events"`
}

// LeadStatusOf reads the lead's mission. The caller names no session.
func (s *Store) LeadStatusOf(sc CallerScope, profileID string) (LeadStatus, error) {
	sid, err := s.LeadMission(sc, profileID)
	if err != nil {
		return LeadStatus{}, err
	}
	if _, _, err := s.leadOf(sc, profileID, sid); err != nil {
		return LeadStatus{}, err
	}
	out := LeadStatus{SessionID: sid}
	if out.Policy, err = s.GetPolicy(profileID, sid); err != nil {
		return out, err
	}
	if out.Room, err = s.RoomFor(profileID, sid, s.childBudget(out.Policy, 0, out.Policy.LeadConversationID)); err != nil {
		return out, err
	}
	if out.Counts, err = s.ChildCounts(profileID, sid); err != nil {
		return out, err
	}
	if out.Children, err = s.ChildrenOfSession(profileID, sid); err != nil {
		return out, err
	}
	if out.Events, err = s.Events(profileID, sid, 20); err != nil {
		return out, err
	}
	return out, nil
}

// OperatorOverview is what the operator's `mission-children list` shows: every policy a profile has stored and
// every row of its mission children (numbers and states, never task text). It takes no caller: it is the
// operator CLI's read.
type OperatorOverview struct {
	Policies []Policy   `json:"policies"`
	Children []ChildRow `json:"children"`
}

// OverviewOf reads the profile's stored policies and child rows.
func (s *Store) OverviewOf(profileID string) (OperatorOverview, error) {
	out := OperatorOverview{Policies: []Policy{}, Children: []ChildRow{}}
	rows, err := s.db.Query(`SELECT session_id FROM coder_mission_policy WHERE profile_id = ? ORDER BY session_id`, profileID)
	if err != nil {
		return out, err
	}
	var sids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return out, err
		}
		sids = append(sids, id)
	}
	rows.Close()
	for _, id := range sids {
		p, err := s.GetPolicy(profileID, id)
		if err != nil {
			return out, err
		}
		out.Policies = append(out.Policies, p)
	}
	kids, err := s.childrenWhere(`profile_id = ?`, profileID)
	if err != nil {
		return out, err
	}
	if kids != nil {
		out.Children = kids
	}
	return out, nil
}
