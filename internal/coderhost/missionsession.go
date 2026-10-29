package coderhost

import (
	"fmt"

	"github.com/monoes/mono-agent/internal/tasks"
)

// errMissionChanged is why a confirmed send is failed instead of started when
// the mission state it was confirmed in is no longer the conversation's.
const errMissionChanged = "mission confirmation no longer matches the open mission: send again"

// openMissionSession is the id of the conversation's open mission session (the
// newest one that is draft, running or paused and not archived), or 0 when it
// has none. It is the rule the chat child applies (dynorg MissionConfig.active),
// read straight from the profile's own rows so the host needs no task store.
// LeadSessionSQL selects, in a WHERE over task_sessions, the sessions that can be a chat's own lead session: the
// mission (kind mission, or a legacy board session the mission planner started: tasks.IsMissionSession) and the
// direction, research and task sessions a lead starts for the chat of its child. A plain board session is not one: an
// agent can link one to its own chat, and it must neither shadow the chat's mission nor hold the chat open.
const LeadSessionSQL = `(kind IN ('mission', 'direction', 'research', 'task') OR (kind = 'board' AND created_by = '` + tasks.MissionPlannerName + `'))`

func (s *Store) openMissionSession(profileID, conversationID string) (int64, error) {
	var id int64
	err := s.db.QueryRow(`SELECT COALESCE(MAX(id), 0) FROM task_sessions
		WHERE profile_id = ? AND conversation_id = ? AND archived_at = '' AND state IN ('draft','running','paused') AND `+LeadSessionSQL,
		profileID, conversationID).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("coder host: reading the open mission session: %w", err)
	}
	return id, nil
}

// confirmMatchesMission is the starting side's re-check of what the confirming
// side bound: the record names the mission session the operator saw (0 when
// there was none), and it holds only while the conversation is still in exactly
// that state. A record bound to no session is therefore valid only while the
// conversation still has no open session; one bound to a session is valid only
// while that session is the open one. Anything else is refused, never adopted.
func (s *Store) confirmMatchesMission(mc MissionConfirm) (bool, error) {
	cur, err := s.openMissionSession(mc.ProfileID, mc.ConversationID)
	if err != nil {
		return false, err
	}
	return cur == mc.Session, nil
}
