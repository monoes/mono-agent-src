package chat

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/tasks"
)

// seedScale stores n conversations and m task sessions (a few of them stamped bosses) the way a long-used profile looks.
func seedScale(t *testing.T, dbPath string, n, m int) {
	t.Helper()
	db := rawDB(t, dbPath)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, err := tx.Exec(`INSERT INTO ai_chat_conversations (id, profile_id, backend, workflow_context, runtime_id, provider_id, model, session_id, mode, cwd, history_key, created_at, updated_at, effort, access)
			VALUES (?, 'default', 'agent', 'general', 'claude', '', 'sonnet', '', 'assistant', '', ?, '2026-10-22T10:00:00Z', ?, '', 'scoped')`,
			fmt.Sprintf("conv-%06d", i), fmt.Sprintf("h-%d", i), fmt.Sprintf("2026-10-22T10:%02d:%02dZ", (i/60)%60, i%60)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < m; i++ {
		charter, key := "", fmt.Sprintf("k-%d", i)
		if i%100 == 0 {
			charter, key = "c1", tasks.BossClientID(fmt.Sprintf("conv-%06d", i), 1)
		}
		conv := any(fmt.Sprintf("conv-%06d", i+1))
		if i%7 == 0 {
			conv = nil // a session of no chat: it must not hide every conversation from a NOT IN
		}
		if _, err := tx.Exec(`INSERT INTO task_sessions (profile_id, conversation_id, title, goal, access_level, max_tasks, max_depth, max_retries, created_by, created_at, updated_at, client_id, kind, doomsday_charter_id)
			VALUES ('default', ?, 't', '', 'full', 10, 3, 2, 'you', 'x', 'x', ?, 'mission', ?)`, conv, key, charter); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// The boss exclusion starts from the few stamped boss sessions and is evaluated once, not once per conversation: no
// correlated subquery in the plans, and a session without a chat does not empty the list. (task_sessions has no index on its
// conversation, and a migration is not to be added for this.)
func TestBossExclusionIsNotCorrelatedAndKeepsRowsOfSessionlessChats(t *testing.T) {
	noOrgs(t)
	dbPath := newChatCLITestDB(t)
	seedScale(t, dbPath, 3000, 400)
	db := rawDB(t, dbPath)
	for name, q := range map[string]string{
		"plain": `SELECT c.id FROM ai_chat_conversations c WHERE c.profile_id = 'default' AND NOT ` + tasks.BossConversationSQL("c") + ` ORDER BY c.updated_at DESC LIMIT 51`,
		"all":   fmt.Sprintf(histSQL, histPromptBytes, "", ""),
	} {
		args := []any{}
		if name == "all" {
			args = []any{"default", 51}
		}
		rows, err := db.Query("EXPLAIN QUERY PLAN "+q, args...)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(detail, "CORRELATED") {
				t.Errorf("%s: a correlated subquery in the plan: %s", name, detail)
			}
		}
		rows.Close()
	}
	var visible int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ai_chat_conversations c WHERE c.profile_id = 'default' AND NOT ` + tasks.BossConversationSQL("c")).Scan(&visible); err != nil {
		t.Fatal(err)
	}
	if visible != 3000-4 { // 400 sessions, every 100th stamped: 4 bosses
		t.Fatalf("%d conversations left after the exclusion, want %d", visible, 3000-4)
	}
}

// The numbers behind the report: the same fixture at the issue's size (30,000 conversations, 400 sessions).
func TestBossExclusionCostAtScale(t *testing.T) {
	if testing.Short() {
		t.Skip("scale fixture")
	}
	noOrgs(t)
	dbPath := newChatCLITestDB(t)
	seedScale(t, dbPath, 30000, 400)
	timeIt := func(args ...string) time.Duration {
		start := time.Now()
		if out, code := runChatHistory(t, dbPath, "default", args...); code != 0 {
			t.Fatalf("%v: %d %s", args, code, out)
		}
		return time.Since(start)
	}
	timeIt("list", "--all") // warm
	all, plain := timeIt("list", "--all"), timeIt("list")
	t.Logf("30000 conversations, 400 sessions: list --all %v, plain list %v", all, plain)
	if all > 3*time.Second || plain > 3*time.Second {
		t.Fatalf("a list takes seconds: --all %v plain %v", all, plain)
	}
}
