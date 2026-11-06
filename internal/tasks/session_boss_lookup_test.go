package tasks

import (
	"database/sql"
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
	for _, bad := range []string{"", "boss-for-", "boss-for-x", "boss-for-x-", "boss-for-x-1a", "boss-for-x-01", "boss-for-x-+1", "other-x-1"} {
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
