package chat

import (
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
)

// The conversation of a run's recorded boss is not a chat of the person's: the history list leaves it out (it is
// shown as the boss in the orbit and on the agents page), while an id still reads it. A conversation whose session
// only carries the boss's client id, without the operator's stamp, stays in the list.
func TestHistoryAllLeavesOutTheConversationOfARecordedBoss(t *testing.T) {
	noOrgs(t)
	dbPath := newChatCLITestDB(t)
	store, db := openTestChatStore(t, dbPath), rawDB(t, dbPath)
	chat := seedConv(t, store, db, ai.AccessFull, "2026-10-22T10:00:03Z")
	boss := seedConv(t, store, db, ai.AccessFull, "2026-10-22T10:00:02Z")
	look := seedConv(t, store, db, ai.AccessFull, "2026-10-22T10:00:01Z")
	for _, c := range []struct {
		conv, key, charter string
	}{{boss.ID, "boss-for-" + chat.ID, "c1"}, {look.ID, "boss-for-x", ""}} {
		if _, err := db.Exec(`INSERT INTO task_sessions (profile_id, conversation_id, title, goal, access_level, max_tasks, max_depth, max_retries, created_by, created_at, updated_at, client_id, kind, doomsday_charter_id)
			VALUES ('default', ?, 't', '', 'full', 10, 3, 2, 'operator', 'x', 'x', ?, 'mission', ?)`, c.conv, c.key, c.charter); err != nil {
			t.Fatal(err)
		}
	}
	ids := map[string]bool{}
	for _, r := range listAll(t, dbPath).Items {
		ids[r.ID] = true
	}
	if !ids[chat.ID] || ids[boss.ID] || !ids[look.ID] {
		t.Fatalf("history ids: %v (chat %s boss %s look %s)", ids, chat.ID, boss.ID, look.ID)
	}
	if got := listAll(t, dbPath, "--id", boss.ID); len(got.Items) != 1 {
		t.Fatalf("the boss conversation cannot be read by id: %+v", got.Items)
	}
}
