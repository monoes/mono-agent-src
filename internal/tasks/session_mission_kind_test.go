package tasks

import (
	"errors"
	"testing"
)

func chatRow(t *testing.T, s *Store, id string) {
	t.Helper()
	if _, err := s.db.Exec(`INSERT INTO ai_chat_conversations (id, profile_id, backend, created_at, updated_at) VALUES (?, 'default', 'x', '', '')`, id); err != nil {
		t.Fatal(err)
	}
}

// Review finding 1: only the chat's mission planner starts a session of kind mission; a prompt-injected
// agent that names itself like the planner is refused too (the legacy rule keys on that name).
func TestOnlyTheMissionPlannerStartsAMissionSession(t *testing.T) {
	s, _, _ := newTestStore(t)
	chatRow(t, s, "c1")
	agent := Actor{Kind: Agent, Name: "worker", Conversation: "c1"}
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: "m", Kind: SessionKindMission, ConversationID: "c1"}, agent); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("an agent starting kind mission: %v", err)
	}
	forged := Actor{Kind: Agent, Name: "mission-lead", Conversation: "c1"}
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: "m", ConversationID: "c1"}, forged); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("an agent named like the planner: %v", err)
	}
	planner := Actor{Kind: Agent, Name: "mission-lead", Conversation: "c1", MissionPlanner: true}
	se, err := s.CreateSession(bg, "default", SessionInput{Title: "m", Kind: SessionKindMission, ConversationID: "c1"}, planner)
	if err != nil || se.Kind != SessionKindMission {
		t.Fatalf("the planner: %v %+v", err, se)
	}
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: "m2", Kind: SessionKindMission, ConversationID: "c1"}, human); err != nil {
		t.Fatalf("the operator: %v", err)
	}
}
