package task

import (
	"os"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/storage"
)

// mintSessionToken makes a conversation in a profile and mints the session token the host
// would give that chat's turn, bound to this test process (as the coderhost tests do).
func mintSessionToken(t *testing.T, dbPath, profile, conv string) string {
	t.Helper()
	d, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.DB.Exec(`INSERT INTO ai_chat_conversations (id, profile_id, backend, created_at, updated_at) VALUES (?, ?, 'x', '', '')`, conv, profile); err != nil {
		t.Fatal(err)
	}
	st := coderhost.NewStore(d.DB)
	x, err := st.EnsureSession(profile, conv)
	if err != nil {
		t.Fatal(err)
	}
	x.State, x.TurnID = coderhost.StateRunning, "turn-1"
	if err := st.SaveSession(x); err != nil {
		t.Fatal(err)
	}
	tok, err := st.IssueSessionToken(profile, conv, "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.BindSessionToken(profile, conv, "turn-1", os.Getpid(), coderhost.ProcStamp(os.Getpid())); err != nil {
		t.Fatal(err)
	}
	return tok
}

func sessionCount(t *testing.T, db string) int {
	t.Helper()
	var list struct{ Sessions []map[string]any }
	mustTaskJSON(t, db, "default", &list, "", "session", "list")
	return len(list.Sessions)
}

func TestAgentSessionLinkWithARealToken(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	addTaskProfile(t, db, "p-b", "Other")
	tok := mintSessionToken(t, db, "default", "conv-1")
	mintSessionToken(t, db, "default", "conv-2") // a second chat of the same profile
	foreign := mintSessionToken(t, db, "p-b", "conv-b")
	setenv(t, "MONOAGENT_ACTOR", "bot")

	t.Run("its own chat", func(t *testing.T) {
		setenv(t, "MONOAGENT_SESSION_TOKEN", tok)
		doc := sessionNewDoc(t, db, "Goal", "--conversation", "conv-1")
		if doc["conversation_id"] != "conv-1" {
			t.Fatalf("%v", doc)
		}
	})
	t.Run("a different chat", func(t *testing.T) {
		setenv(t, "MONOAGENT_SESSION_TOKEN", tok)
		before := sessionCount(t, db)
		doc := failedTaskJSON(t, db, "default", 3, "session", "new", "Goal2", "--conversation", "conv-2")
		if doc["code"] != "invalid_input" || !strings.Contains(doc["error"].(string), "own chat") {
			t.Fatalf("%v", doc)
		}
		if n := sessionCount(t, db); n != before {
			t.Fatalf("a refused link left %d sessions", n-before)
		}
	})
	t.Run("a token of another profile", func(t *testing.T) {
		setenv(t, "MONOAGENT_SESSION_TOKEN", foreign)
		doc := failedTaskJSON(t, db, "default", 3, "session", "new", "Goal3", "--conversation", "conv-b")
		msg, _ := doc["error"].(string)
		if doc["code"] != "invalid_input" || !strings.Contains(msg, "session token is not valid for this profile") {
			t.Fatalf("a token failure must say so, not blame ownership: %v", doc)
		}
	})
	t.Run("an environment label does not choose the chat", func(t *testing.T) {
		// The token is conv-1's; a label naming conv-2 must change nothing.
		setenv(t, "MONOAGENT_SESSION_TOKEN", tok)
		setenv(t, "MONOAGENT_CONVERSATION", "conv-2")
		doc := failedTaskJSON(t, db, "default", 3, "session", "new", "Goal4", "--conversation", "conv-2")
		if doc["code"] != "invalid_input" {
			t.Fatalf("%v", doc)
		}
		if se := sessionNewDoc(t, db, "Goal5", "--conversation", "conv-1"); se["conversation_id"] != "conv-1" {
			t.Fatalf("%v", se)
		}
	})
}

// --profile may give a name; the token names the profile by id.
func TestOwnChatResolvesAProfileNameToItsID(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	addTaskProfile(t, db, "work-id", "Work")
	tok := mintSessionToken(t, db, "work-id", "conv-w")
	setenv(t, "MONOAGENT_SESSION_TOKEN", tok)
	d := testDeps(t, db, "default", true)
	d.ProfileID = func() string { return "Work" } // unresolved, and OpenDB does not resolve it
	chat, err := d.ownChat()
	if err != nil || chat != "conv-w" {
		t.Fatalf("ownChat = %q, %v", chat, err)
	}
}
