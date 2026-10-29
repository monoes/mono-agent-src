package chat

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/dynorg"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/tasks"
	"github.com/monoes/mono-agent/internal/testdb"
)

func TestMissionConfigGates(t *testing.T) {
	db := testdb.Open(t).DB
	ctx := context.Background()
	for _, lvl := range []string{"scoped", "read"} {
		if m := missionConfig(ctx, db, "default", "c1", coderTurn{access: lvl, missionConfirmed: true}); m != nil {
			t.Errorf("access %q got a mission setup", lvl)
		}
	}
	if m := missionConfig(ctx, db, "default", "c1", coderTurn{access: "coding"}); m == nil || m.Confirmed || m.Planning() {
		t.Errorf("unconfirmed coding: %+v", m)
	}
	if m := missionConfig(ctx, db, "default", "c1", coderTurn{access: "workspace", missionConfirmed: true}); m == nil || !m.Planning() {
		t.Errorf("confirmed workspace: %+v", m)
	}
	// An open session of the conversation keeps it confirmed on a later turn.
	if _, err := tasks.NewStore(db).CreateSession(ctx, "default", tasks.SessionInput{Title: "M", ConversationID: "c1", Kind: tasks.SessionKindMission}, tasks.Actor{Kind: tasks.Human}); err != nil {
		t.Fatal(err)
	}
	if m := missionConfig(ctx, db, "default", "c1", coderTurn{access: "coding"}); m == nil || !m.Planning() {
		t.Errorf("open session did not confirm: %+v", m)
	}
	if m := missionConfig(ctx, db, "default", "c2", coderTurn{access: "coding"}); m == nil || m.Planning() {
		t.Errorf("another conversation confirmed: %+v", m)
	}
}

func TestMissionRunOnlyForAConfirmedLead(t *testing.T) {
	db := testdb.Open(t).DB
	ctx := context.Background()
	if mr := missionRun(nil, db, "t1"); mr != nil {
		t.Errorf("no mission setup: %+v", mr)
	}
	if mr := missionRun(missionConfig(ctx, db, "default", "c1", coderTurn{access: "coding"}), db, "t1"); mr != nil {
		t.Errorf("unconfirmed lead got the mission engine: %+v", mr)
	}
	m := missionConfig(ctx, db, "default", "c1", coderTurn{access: "coding", missionConfirmed: true})
	mr := missionRun(m, db, "t1")
	if mr == nil || mr.Store == nil || mr.Tokens == nil || mr.ProfileID != "default" || mr.Session != 0 {
		t.Fatalf("confirmed lead: %+v (the session is the conversation's, never preset)", mr)
	}
	// The tokens it mints are owned by this turn of this conversation, and a sweep can ask whether
	// another turn still runs: without these a revoke by owner would never reach a crashed run.
	if want := dynorg.TurnOwner("default", "c1", "t1"); mr.Owner != want || mr.Live == nil {
		t.Errorf("owner %q (want %q), live check set: %v", mr.Owner, want, mr.Live != nil)
	}
}

func TestTurnRunningReadsTheTurnTable(t *testing.T) {
	db := testdb.Open(t).DB
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO ai_chat_conversations (id, profile_id, backend, created_at, updated_at) VALUES ('c1','default','test','','')`); err != nil {
		t.Fatal(err)
	}
	for id, status := range map[string]string{"run": "active", "over": "interrupted"} {
		if _, err := db.Exec(`INSERT INTO ai_chat_turns (id, conversation_id, profile_id, prompt, status, created_at, updated_at) VALUES (?, 'c1', 'default', 'p', ?, 'x', 'x')`, id, status); err != nil {
			t.Fatal(err)
		}
	}
	live := turnRunning(db, "default")
	for turn, want := range map[string]bool{"run": true, "over": false, "unknown": false} {
		if got := live(ctx, dynorg.TurnOwner("default", "c1", turn)); got != want {
			t.Errorf("turn %s running = %v, want %v", turn, got, want)
		}
	}
	if live(ctx, "run-abc") {
		t.Error("an owner with no turn is running")
	}
	if turnRunning(db, "other")(ctx, dynorg.TurnOwner("default", "c1", "run")) {
		t.Error("another profile's turn id counted")
	}
}

// A session of a closed (stopped) mission, or of a conversation that is not this one, confirms
// nothing: only an open session of this conversation keeps the lead confirmed.
func TestMissionConfigClosedSessionDoesNotConfirm(t *testing.T) {
	db := testdb.Open(t).DB
	ctx := context.Background()
	st := tasks.NewStore(db)
	op := tasks.Actor{Kind: tasks.Human}
	se, err := st.CreateSession(ctx, "default", tasks.SessionInput{Title: "M", ConversationID: "c1", Kind: tasks.SessionKindMission}, op)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.StopSession(ctx, "default", se.ID, op); err != nil {
		t.Fatal(err)
	}
	if m := missionConfig(ctx, db, "default", "c1", coderTurn{access: "coding"}); m == nil || m.Planning() {
		t.Errorf("a stopped session confirmed a mission: %+v", m)
	}
}

// The host's conversations are ai_chat_conversations rows (coder sessions key on the
// same ids), so the store's check of a session's conversation accepts them and still
// refuses an id that is no conversation of the profile.
func TestSessionConversationValidationAcceptsHostCoderConversations(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	conv, err := openTestChatStore(t, dbPath).CreateConversationMode("default", "agent", "general", "claude", "", "", ai.ModeCoder, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st := tasks.NewStore(db.DB)
	ctx := context.Background()
	op := tasks.Actor{Kind: tasks.Agent, Name: missionActor, Conversation: conv.ID, MissionPlanner: true} // the lead links its own chat
	opOther := tasks.Actor{Kind: tasks.Agent, Name: missionActor, Conversation: "no-such-chat", MissionPlanner: true}
	if _, err := st.CreateSession(ctx, "default", tasks.SessionInput{Title: "M", ConversationID: conv.ID}, op); err != nil {
		t.Fatalf("a coder conversation was refused: %v", err)
	}
	if _, err := st.CreateSession(ctx, "default", tasks.SessionInput{Title: "M2", ConversationID: "no-such-chat"}, opOther); err == nil {
		t.Fatal("an unknown conversation was accepted")
	}
	if _, err := st.CreateSession(ctx, "other-profile", tasks.SessionInput{Title: "M3", ConversationID: conv.ID}, op); err == nil {
		t.Fatal("another profile's conversation was accepted")
	}
}
