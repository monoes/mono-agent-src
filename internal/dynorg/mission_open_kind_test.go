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
	if id, err := m.OpenSessionID(ctx); m.HasOpenSession(ctx) || id != 0 || err != nil {
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
	if id, _ := m.OpenSessionID(ctx); !m.HasOpenSession(ctx) || id != se.ID {
		t.Fatal("a session the planner started before the kind column no longer counts")
	}
}

// Review finding 5: a store that cannot say whether the chat has an open mission is an error, not "none";
// a corrupt row of another chat does not stop the answer.
func TestOpenSessionIDReportsAnUnreadableMissionAndSurvivesAnUnrelatedCorruptRow(t *testing.T) {
	db := openMissionDB(t)
	st := tasks.NewStore(db)
	ctx := context.Background()
	m := &MissionConfig{Store: st, ProfileID: missionProfile, ConversationID: "conv", Actor: "mission-lead"}
	mission, err := st.CreateSession(ctx, missionProfile, tasks.SessionInput{Title: "M", ConversationID: "conv", Kind: tasks.SessionKindMission}, missionHuman)
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateSession(ctx, missionProfile, tasks.SessionInput{Title: "other", ConversationID: "conv-2"}, missionHuman)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE task_sessions SET parent_session_id = id WHERE id = ?`, other.ID); err != nil {
		t.Fatal(err)
	}
	if id, err := m.OpenSessionID(ctx); err != nil || id != mission.ID {
		t.Fatalf("an unrelated corrupt row: %d %v", id, err)
	}
	if _, err := db.Exec(`UPDATE task_sessions SET parent_session_id = id WHERE id = ?`, mission.ID); err != nil {
		t.Fatal(err)
	}
	if id, err := m.OpenSessionID(ctx); err == nil {
		t.Fatalf("a corrupt mission session read as %d with no error", id)
	}
}
