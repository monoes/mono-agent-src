package tasks

import (
	"encoding/json"
	"strings"
	"testing"
)

// A session the operator stamped as the boss of an open-ended run says so in its row, and only that one does: the
// app shows the boss apart from the chat's own sessions (a list row has no other marker).
func TestSessionRowSaysItIsTheBossOfARun(t *testing.T) {
	s, db, _ := newTestStore(t)
	plain, err := s.CreateSession(bg, "default", SessionInput{Title: "plain"}, human)
	if err != nil {
		t.Fatal(err)
	}
	boss, err := s.CreateSession(bg, "default", SessionInput{Title: "boss", Kind: SessionKindMission}, human)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE task_sessions SET doomsday_charter_id = 'c1', doomsday_generation = 1 WHERE id = ?`, boss.ID); err != nil {
		t.Fatal(err)
	}
	all, err := s.ListSessions(bg, "default", SessionFilter{}, human)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]bool{}
	for _, se := range all {
		got[se.ID] = se.Boss
	}
	if got[plain.ID] || !got[boss.ID] {
		t.Fatalf("boss markers: %v", got)
	}
	one, err := s.GetSession(bg, "default", boss.ID, human)
	if err != nil || !one.Boss {
		t.Fatalf("the single read lost the marker: %v %+v", err, one)
	}
	raw, _ := json.Marshal(one)
	plainRaw, _ := json.Marshal(all[len(all)-1])
	if !strings.Contains(string(raw), `"boss":true`) || strings.Contains(string(plainRaw), `"boss"`) {
		t.Fatalf("json: %s / %s", raw, plainRaw)
	}
}
