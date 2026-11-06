package chat

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/tasks"
)

// The conversation of a run's recorded boss is hidden from the plain `chat history list` only. Every other listing shows it
// with the boss mark: `list --all` (history, search by --query) includes it with boss true, `coder sessions list` marks
// its session row, and the queue's coder chats mark it. A conversation whose session only carries the boss's client id,
// without the operator's stamp, is no boss anywhere.
type bossListing struct {
	dbPath                 string
	chat, boss, fake, lead ai.Conversation
}

func seedBossListing(t *testing.T) bossListing {
	t.Helper()
	dbPath := newChatCLITestDB(t)
	store, db := openTestChatStore(t, dbPath), rawDB(t, dbPath)
	b := bossListing{dbPath: dbPath}
	b.chat = seedConv(t, store, db, ai.AccessFull, "2026-10-22T10:00:03Z")
	b.boss = seedConv(t, store, db, ai.AccessFull, "2026-10-22T10:00:02Z")
	b.fake = seedConv(t, store, db, ai.AccessFull, "2026-10-22T10:00:01Z")
	b.lead = seedConv(t, store, db, ai.AccessFull, "2026-10-22T10:00:00Z") // a chat's own lead mission named at start: stamped, but no boss key
	for _, c := range []struct{ conv, key, charter string }{{b.boss.ID, tasks.BossClientID(b.chat.ID, 1), "c1"}, {b.fake.ID, tasks.BossClientID("x", 1), ""}, {b.lead.ID, "", "c9"}} {
		if _, err := db.Exec(`INSERT INTO task_sessions (profile_id, conversation_id, title, goal, access_level, max_tasks, max_depth, max_retries, created_by, created_at, updated_at, client_id, kind, doomsday_charter_id)
			VALUES ('default', ?, 't', '', 'full', 10, 3, 2, 'operator', 'x', 'x', NULLIF(?, ''), 'mission', ?)`, c.conv, c.key, c.charter); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{b.boss.ID, b.fake.ID} { // the host knows both as sessions
		if err := coderhost.NewStore(db).SetChatMode("default", id, coderhost.ChatModeOff); err != nil {
			t.Fatal(err)
		}
	}
	return b
}

func TestPlainHistoryListLeavesOutARecordedBossAndNothingElse(t *testing.T) {
	b := seedBossListing(t)
	out, code := runChatHistory(t, b.dbPath, "default", "list")
	if code != 0 {
		t.Fatalf("list: %d %s", code, out)
	}
	var page chatConversationPage
	decodeChatJSON(t, out, &page)
	ids := map[string]bool{}
	for _, c := range page.Items {
		ids[c.ID] = true
	}
	if !ids[b.chat.ID] || ids[b.boss.ID] || !ids[b.fake.ID] || !ids[b.lead.ID] {
		t.Fatalf("plain list ids: %v", ids)
	}
	if out, code := runChatHistory(t, b.dbPath, "default", "show", b.boss.ID); code != 0 {
		t.Fatalf("the boss conversation cannot be read by id: %d %s", code, out)
	}
}

func TestHistoryAllShowsARecordedBossWithTheBossMark(t *testing.T) {
	noOrgs(t)
	b := seedBossListing(t)
	marks := map[string]bool{}
	for _, r := range listAll(t, b.dbPath).Items {
		marks[r.ID] = r.Boss
	}
	if len(marks) != 4 || marks[b.chat.ID] || !marks[b.boss.ID] || marks[b.fake.ID] || marks[b.lead.ID] {
		t.Fatalf("--all marks: %v", marks)
	}
	if got := listAll(t, b.dbPath, "--id", b.boss.ID); len(got.Items) != 1 || !got.Items[0].Boss {
		t.Fatalf("--id: %+v", got.Items)
	}
}

func TestCoderSessionsListMarksTheBossSession(t *testing.T) {
	b := seedBossListing(t)
	cfg := testDeps(&testConfig{DBPath: b.dbPath, ProfileID: "default", JSONOutput: true})
	out, code := runSessionsCLI(t, cfg, "list")
	if code != 0 {
		t.Fatalf("list: %d %s", code, out)
	}
	var doc struct {
		Sessions []struct {
			ConversationID string `json:"conversationId"`
			Boss           bool   `json:"boss"`
		} `json:"sessions"`
	}
	decodeChatJSON(t, out, &doc)
	marks := map[string]bool{}
	for _, s := range doc.Sessions {
		marks[s.ConversationID] = s.Boss
	}
	if len(marks) != 2 || !marks[b.boss.ID] || marks[b.fake.ID] {
		t.Fatalf("session marks: %v", marks)
	}
}

func TestQueueCoderChatsMarkTheBoss(t *testing.T) {
	b := seedBossListing(t)
	store, db := openTestChatStore(t, b.dbPath), rawDB(t, b.dbPath)
	seedTurn(t, store, db, b.boss.ID, "bt1", "steer me", "completed", "2026-10-22T09:00:00Z")
	seedTurn(t, store, db, b.chat.ID, "ct1", "hello", "completed", "2026-10-22T09:00:00Z")
	rows, err := coderChats(store, "default", 10, func(id string) bool {
		b, _ := tasks.IsBossConversation(context.Background(), db, "default", id)
		return b
	})
	if err != nil {
		t.Fatal(err)
	}
	marks := map[string]bool{}
	for _, r := range rows {
		marks[r.ID] = r.Boss
	}
	if len(marks) != 2 || !marks[b.boss.ID] || marks[b.chat.ID] {
		t.Fatalf("queue chat marks: %v", marks)
	}
}
