package chat

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/coderhost"
)

func TestChatHistoryRenameNamesTheChatAndClearsIt(t *testing.T) {
	noOrgs(t)
	dbPath := newChatCLITestDB(t)
	store, db := openTestChatStore(t, dbPath), rawDB(t, dbPath)
	c := seedConv(t, store, db, ai.AccessFull, "2026-10-22T10:00:00Z")
	seedTurn(t, store, db, c.ID, "t1", "first message wins nothing", "completed", "2026-10-22T09:00:00Z")

	out, code := runChatHistory(t, dbPath, "default", "rename", c.ID, "  meta \n challenge ")
	if code != 0 {
		t.Fatalf("rename: exit %d: %s", code, out)
	}
	var got struct{ ID, Title string }
	decodeChatJSON(t, out, &got)
	if got.ID != c.ID || got.Title != "meta challenge" {
		t.Fatalf("rename answered %+v", got)
	}
	r := rowByID(t, listAll(t, dbPath), c.ID)
	if r.Title != "meta challenge" || !r.Renamed {
		t.Fatalf("history row = %q renamed %v, want the typed name", r.Title, r.Renamed)
	}
	if r.UpdatedAt != "2026-10-22T10:00:00Z" {
		t.Fatalf("a rename moved the chat in the history: %s", r.UpdatedAt)
	}
	// The name can be searched.
	if p := listAll(t, dbPath, "--query", "challenge"); len(p.Items) != 1 {
		t.Fatalf("search found %d rows", len(p.Items))
	}

	// No title takes it back to the derived one.
	if out, code := runChatHistory(t, dbPath, "default", "rename", c.ID); code != 0 {
		t.Fatalf("clear: exit %d: %s", code, out)
	}
	r = rowByID(t, listAll(t, dbPath), c.ID)
	if r.Title != "first message wins nothing" || r.Renamed {
		t.Fatalf("after clearing: %q renamed %v", r.Title, r.Renamed)
	}
}

func TestChatHistoryRenameRedactsBoundsAndRefusesTheUnknown(t *testing.T) {
	noOrgs(t)
	dbPath := newChatCLITestDB(t)
	store, db := openTestChatStore(t, dbPath), rawDB(t, dbPath)
	c := seedConv(t, store, db, ai.AccessScoped, "2026-10-22T10:00:00Z")

	runChatHistory(t, dbPath, "default", "rename", c.ID, "deploy with password: hunter2xyz now")
	r := rowByID(t, listAll(t, dbPath), c.ID)
	if strings.Contains(r.Title, "hunter2xyz") {
		t.Fatalf("a typed secret became a title: %q", r.Title)
	}
	var stored string
	if err := db.QueryRow(`SELECT title FROM ai_chat_conversations WHERE id = ?`, c.ID).Scan(&stored); err != nil || strings.Contains(stored, "hunter2xyz") {
		t.Fatalf("the secret was stored: %q (%v)", stored, err)
	}

	runChatHistory(t, dbPath, "default", "rename", c.ID, strings.Repeat("x", 300)+"\x07")
	db.QueryRow(`SELECT title FROM ai_chat_conversations WHERE id = ?`, c.ID).Scan(&stored)
	if len([]rune(stored)) != ai.MaxConversationTitle {
		t.Fatalf("stored %d characters, want %d", len([]rune(stored)), ai.MaxConversationTitle)
	}

	if _, code := runChatHistory(t, dbPath, "default", "rename", "no-such-id", "x"); code == 0 {
		t.Fatal("renaming an unknown conversation succeeded")
	}
	// Another profile's chat is not found either.
	if _, code := runChatHistory(t, dbPath, "other", "rename", c.ID, "mine now"); code == 0 {
		t.Fatal("a chat was renamed from another profile")
	}
}

// The pane shows the mesh name a session takes while it has none of its own: the chat's name with
// dashes for spaces, the same one the host registers under.
func TestSessionSettingsShowTheDefaultMeshName(t *testing.T) {
	noOrgs(t)
	dbPath := newChatCLITestDB(t)
	store, db := openTestChatStore(t, dbPath), rawDB(t, dbPath)
	c := seedConv(t, store, db, ai.AccessFull, "2026-10-22T10:00:00Z")
	view := func() struct {
		DefaultAlias string `json:"defaultAlias"`
	} {
		t.Helper()
		out, code := runCoderCLI(t, dbPath, "sessions", "settings", "get", c.ID)
		if code != 0 {
			t.Fatalf("settings get: exit %d: %s", code, out)
		}
		var v struct {
			DefaultAlias string `json:"defaultAlias"`
		}
		decodeChatJSON(t, out, &v)
		return v
	}
	conv, _ := store.GetConversation(c.ID, "default")
	if want := coderhost.AliasFromTitle(filepath.Base(conv.Cwd)); view().DefaultAlias != want || want == "" {
		t.Fatalf("an unnamed chat in a folder has default %q, want the folder's name %q", view().DefaultAlias, want)
	}
	runChatHistory(t, dbPath, "default", "rename", c.ID, "meta challenge")
	if got := view().DefaultAlias; got != "meta-challenge" {
		t.Fatalf("default = %q, want meta-challenge", got)
	}
}
