package task

import (
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
)

// A bare environment label never proves which chat an agent is in: with a made-up token
// or a conversation variable the link is refused and no session exists.
func TestAgentSessionNewConversationNeedsVerifiedToken(t *testing.T) {
	db := newTaskTestDB(t)
	d, err := storage.NewDatabase(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`INSERT INTO ai_chat_conversations (id, profile_id, backend, created_at, updated_at) VALUES ('conv-1', 'default', 'x', '', '')`); err != nil {
		t.Fatal(err)
	}
	d.Close()
	setenv(t, "MONOAGENT_ACTOR", "bot")
	setenv(t, "MONOAGENT_CONVERSATION", "conv-1")
	setenv(t, "MONOAGENT_SESSION_TOKEN", "mst-notarealtoken")
	doc := failedTaskJSON(t, db, "default", 3, "session", "new", "Goal", "--conversation", "conv-1")
	if doc["code"] != "invalid_input" {
		t.Fatalf("%v", doc)
	}
	var list struct{ Sessions []map[string]any }
	mustTaskJSON(t, db, "default", &list, "", "session", "list")
	if len(list.Sessions) != 0 {
		t.Fatalf("a refused link left %d sessions", len(list.Sessions))
	}
	if se := sessionNewDoc(t, db, "Unlinked"); se["id"] == nil {
		t.Fatalf("an agent may still plan without a chat link: %v", se)
	}
}
