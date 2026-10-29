package dynorg

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
)

// Review finding 1: a session an agent links to its own chat is not the chat's mission session, so it
// neither confirms the mission nor locks the toggle. The planner's own session, and an old planner
// session of kind board, are.
func TestOnlyAMissionSessionCountsAsTheChatsOpenMission(t *testing.T) {
	st := missionStore(t)
	ctx := context.Background()
	m := &MissionConfig{Store: st, ProfileID: missionProfile, ConversationID: "conv", Actor: "mission-lead"}
	agent := tasks.Actor{Kind: tasks.Agent, Name: "worker", Conversation: "conv"}
	if _, err := st.CreateSession(ctx, missionProfile, tasks.SessionInput{Title: "sneaky", ConversationID: "conv"}, agent); err != nil {
		t.Fatal(err)
	}
	if m.HasOpenSession(ctx) || m.OpenSessionID(ctx) != 0 {
		t.Fatal("an agent's board session linked to the chat counts as the mission")
	}
	if _, err := st.CreateSession(ctx, missionProfile, tasks.SessionInput{Title: "op", ConversationID: "conv", Kind: tasks.SessionKindMission}, missionHuman); err != nil {
		t.Fatal(err)
	}
	if !m.HasOpenSession(ctx) {
		t.Fatal("a mission session of the chat is not open")
	}
}

func TestAnOldPlannerSessionOfKindBoardStillCountsAsTheMission(t *testing.T) {
	db := openMissionDB(t)
	st := tasks.NewStore(db)
	ctx := context.Background()
	se, err := st.CreateSession(ctx, missionProfile, tasks.SessionInput{Title: "old", ConversationID: "conv"}, missionHuman)
	if err != nil {
		t.Fatal(err)
	}
	m := &MissionConfig{Store: st, ProfileID: missionProfile, ConversationID: "conv", Actor: "mission-lead"}
	if m.HasOpenSession(ctx) {
		t.Fatal("an operator board session counts")
	}
	if _, err := db.Exec(`UPDATE task_sessions SET created_by = 'mission-lead' WHERE id = ?`, se.ID); err != nil {
		t.Fatal(err)
	}
	if !m.HasOpenSession(ctx) || m.OpenSessionID(ctx) != se.ID {
		t.Fatal("a session the planner started before the kind column no longer counts")
	}
}
