package tasks

import (
	"database/sql"
	"strings"
	"testing"
)

// The key of a boss session names its chat and an attempt number; the chat is read back from it exactly, whatever the chat
// id looks like (a uuid whose last group is all digits included).
func TestBossClientIDRoundTrips(t *testing.T) {
	for _, front := range []string{"11111111-2222-3333-4444-555555555555", "11111111-2222-3333-4444-123456789012", "chat", "a-b"} {
		key := BossClientID(front, 12)
		if len(key) > MaxClientIDLen {
			t.Fatalf("the key %q is longer than a client id may be", key)
		}
		got, n, ok := ParseBossClientID(key)
		if !ok || got != front || n != 12 {
			t.Fatalf("%q parsed as %q %d %v", key, got, n, ok)
		}
	}
	// the attempt number is small: a key an agent made with a huge number is no boss key (and would not fit a client id)
	if _, n, ok := ParseBossClientID(BossClientID("x", MaxBossAttempt)); !ok || n != MaxBossAttempt {
		t.Fatalf("the largest attempt does not parse: %d %v", n, ok)
	}
	for _, bad := range []string{BossClientID("x", MaxBossAttempt+1), "boss-for-x-123456789012345678", "", "boss-for-", "boss-for-x", "boss-for-x-", "boss-for-x-1a", "boss-for-x-01", "boss-for-x-+1", "other-x-1"} {
		if _, _, ok := ParseBossClientID(bad); ok {
			t.Fatalf("%q parsed as a boss key", bad)
		}
	}
}

func stampRun(t *testing.T, db *sql.DB, session int64, charter string, gen int64, state string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE task_sessions SET doomsday_charter_id = ?, doomsday_generation = ? WHERE id = ?`, charter, gen, session); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO doomsday_runs (profile, generation, charter_id, state, mac) VALUES ('default', ?, ?, ?, x'00')`, gen, charter, state); err != nil {
		t.Fatal(err)
	}
}

// The chat's boss is found only while it is the ACTIVE run's recorded boss: stamped with the running run's charter and
// generation, open, not archived, and carrying the chat's key. A stopped run, another run's stamp, a session that only
// carries the key, or another chat finds nothing.
func TestOpenBossConversationIsBoundToTheRunningRun(t *testing.T) {
	s, db, _ := newTestStore(t)
	front := "11111111-2222-3333-4444-555555555555"
	boss, err := s.CreateSession(bg, "default", SessionInput{Title: "boss", Kind: SessionKindMission, ConversationID: "boss-conv", ClientID: BossClientID(front, 1)}, human)
	if err != nil {
		t.Fatal(err)
	}
	find := func(chat string) (string, int64) {
		conv, id, err := OpenBossConversation(bg, db, "default", chat)
		if err != nil {
			t.Fatal(err)
		}
		return conv, id
	}
	if conv, id := find(front); conv != "" || id != 0 {
		t.Fatalf("an unstamped session is a boss: %q %d", conv, id)
	}
	stampRun(t, db, boss.ID, "c1", 1, "running")
	if conv, id := find(front); conv != "boss-conv" || id != boss.ID {
		t.Fatalf("the running run's boss is not found: %q %d", conv, id)
	}
	if conv, id := find("another-chat"); conv != "" || id != 0 {
		t.Fatalf("another chat got the boss: %q %d", conv, id)
	}
	for _, state := range []string{"stop-requested", "authority-revoked", "cleanup-pending", "quiescent"} {
		if _, err := db.Exec(`UPDATE doomsday_runs SET state = ? WHERE profile = 'default'`, state); err != nil {
			t.Fatal(err)
		}
		if conv, id := find(front); conv != "" || id != 0 {
			t.Fatalf("a run in %s still takes steering: %q %d", state, conv, id)
		}
	}
	// the stamp belongs to a run that is not the running one (same charter id, another generation)
	if _, err := db.Exec(`INSERT INTO doomsday_runs (profile, generation, charter_id, state, mac) VALUES ('default', 2, 'c1', 'running', x'00')`); err != nil {
		t.Fatal(err)
	}
	if conv, id := find(front); conv != "" || id != 0 {
		t.Fatalf("a finished run's boss answers for a new run: %q %d", conv, id)
	}
	// a boss of the chat "<front>-x" is not the boss of "<front>", though its key starts with the same text
	other, err := s.CreateSession(bg, "default", SessionInput{Title: "other", Kind: SessionKindMission, ConversationID: "other-conv", ClientID: BossClientID(front+"-x", 1)}, human)
	if err != nil {
		t.Fatal(err)
	}
	stampRun(t, db, other.ID, "c1", 2, "running")
	if conv, id := find(front); conv != "" || id != 0 {
		t.Fatalf("the boss of a chat whose id starts with this chat's id answers: %q %d", conv, id)
	}
	boss2, err := s.CreateSession(bg, "default", SessionInput{Title: "boss 2", Kind: SessionKindMission, ConversationID: "boss-conv-2", ClientID: BossClientID(front, 2)}, human)
	if err != nil {
		t.Fatal(err)
	}
	stampRun(t, db, boss2.ID, "c1", 2, "running")
	if conv, id := find(front); conv != "boss-conv-2" || id != boss2.ID {
		t.Fatalf("the new run's boss is not found: %q %d", conv, id)
	}
	if _, err := db.Exec(`UPDATE task_sessions SET archived_at = '2026-11-06T00:00:00Z' WHERE id = ?`, boss2.ID); err != nil {
		t.Fatal(err)
	}
	if conv, id := find(front); conv != "" || id != 0 {
		t.Fatalf("an archived boss answers: %q %d", conv, id)
	}
}

// Only a stamped mission session that carries a boss key marks its conversation as a boss's: a stamped session without
// the key (the chat's own lead named at start) does not, and neither does a key without the stamp.
func TestBossConversationMarkNeedsTheStampTheKeyAndTheKind(t *testing.T) {
	s, db, _ := newTestStore(t)
	mk := func(conv, key, kind string) int64 {
		se, err := s.CreateSession(bg, "default", SessionInput{Title: conv, Kind: kind, ConversationID: conv, ClientID: key}, human)
		if err != nil {
			t.Fatal(err)
		}
		return se.ID
	}
	both := mk("both", BossClientID("x", 1), SessionKindMission)
	noStamp := mk("nostamp", BossClientID("y", 1), SessionKindMission)
	noKey := mk("nokey", "", SessionKindMission)
	wrongKind := mk("wrongkind", BossClientID("z", 1), SessionKindTask)
	for _, id := range []int64{both, noKey, wrongKind} {
		if _, err := db.Exec(`UPDATE task_sessions SET doomsday_charter_id = 'c', doomsday_generation = 1 WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
	}
	_ = noStamp
	for conv, want := range map[string]bool{"both": true, "nostamp": false, "nokey": false, "wrongkind": false} {
		got, err := IsBossConversation(bg, db, "default", conv)
		if err != nil || got != want {
			t.Fatalf("%s: marked %v (want %v) %v", conv, got, want, err)
		}
	}
}

// "Is this the conversation of a boss" has one meaning wherever it is asked: the listings that must not offer it (the SQL form,
// BossAnyConversationSQL / IsBossAnyConversation) and the guards that refuse a turn into it (BossFrontDesk) agree on every
// kind of session, including a key whose number is past the cap.
func TestTheTwoFormsOfTheBossConversationAgree(t *testing.T) {
	s, db, _ := newTestStore(t)
	front := "11111111-2222-3333-4444-555555555555"
	mk := func(conv, key, kind, by string, parent int64) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO ai_chat_conversations (id, profile_id, backend, workflow_context, runtime_id, provider_id, model, session_id, mode, cwd, history_key, created_at, updated_at, effort, access)
			VALUES (?, 'default', 'agent', 'general', 'claude', '', '', '', 'assistant', '', ?, 'x', 'x', '', 'scoped')`, conv, "h-"+conv); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO task_sessions (profile_id, conversation_id, title, goal, access_level, max_tasks, max_depth, max_retries, created_by, created_at, updated_at, client_id, kind, parent_session_id)
			VALUES ('default', ?, 't', '', 'full', 10, 3, 2, ?, 'x', 'x', NULLIF(?, ''), ?, NULLIF(?, 0))`, conv, by, key, kind, parent); err != nil {
			t.Fatal(err)
		}
	}
	root, err := s.CreateSession(bg, "default", SessionInput{Title: "root", Kind: SessionKindMission, ConversationID: "root-conv"}, human)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]bool{ // conversation -> is a boss's
		"c-boss":      true,
		"c-agent":     false,
		"c-ownchat":   false,
		"c-huge":      false,
		"c-task":      false,
		"c-child":     false,
		"c-nokey":     false,
		"c-cap":       true,
		"c-plainchat": false,
		"c-upper":     false, // SQL LIKE would match a differently cased prefix; the parser does not: neither does a boss key
	}
	mk("c-boss", BossClientID(front, 1), SessionKindMission, "you", 0)
	mk("c-agent", BossClientID(front, 2), SessionKindMission, "agent-x", 0)
	mk("c-ownchat", BossClientID("c-ownchat", 1), SessionKindMission, "you", 0)
	mk("c-huge", "boss-for-"+front+"-1000", SessionKindMission, "you", 0)
	mk("c-task", BossClientID(front, 3), SessionKindTask, "you", 0)
	mk("c-child", BossClientID(front, 4), SessionKindMission, "you", root.ID)
	mk("c-nokey", "", SessionKindMission, "you", 0)
	mk("c-cap", BossClientID(front, MaxBossAttempt), SessionKindMission, "you", 0)
	mk("c-plainchat", "other-key", SessionKindMission, "you", 0)
	mk("c-upper", "BOSS-FOR-"+front+"-1", SessionKindMission, "you", 0)
	for conv, want := range cases {
		_, _, goForm, err := BossFrontDesk(bg, db, "default", conv)
		if err != nil {
			t.Fatal(err)
		}
		sqlForm, err := IsBossAnyConversation(bg, db, "default", conv)
		if err != nil {
			t.Fatal(err)
		}
		var viaSQL int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ai_chat_conversations c WHERE c.id = ? AND `+BossAnyConversationSQL("c"), conv).Scan(&viaSQL); err != nil {
			t.Fatal(err)
		}
		if goForm != want || sqlForm != want || (viaSQL == 1) != want {
			t.Errorf("%s: want %v; BossFrontDesk %v, IsBossAnyConversation %v, SQL %v", conv, want, goForm, sqlForm, viaSQL == 1)
		}
	}
}

// The SQL form of a boss key and the Go form share their cap: the largest attempt matches, the next one does not.
func TestBossKeySQLMatchesMaxBossAttempt(t *testing.T) {
	_, db, _ := newTestStore(t)
	match := func(key string) bool {
		var n int
		if err := db.QueryRow(`SELECT 1 WHERE ` + strings.ReplaceAll(BossKeySQL("?"), "?", "'"+key+"'")).Scan(&n); err != nil {
			return false
		}
		return n == 1
	}
	if !match(BossClientID("x", 1)) || !match(BossClientID("x", MaxBossAttempt)) || match(BossClientID("x", MaxBossAttempt+1)) || match("boss-for-x-0") || match("boss-for-x-01") || match("other-x-1") {
		t.Fatal("the SQL form of the key disagrees with the cap")
	}
}
