package coderhost

import (
	"errors"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/dbconn/dbconntest"
)

// Money class: the spawn carve takes a budget out of the master's. When its ROLLBACK fails the connection may still
// be inside the transaction; it must be dropped, not handed to the next caller of the pool.
func TestCarveDropsAConnectionWhoseRollbackFails(t *testing.T) {
	db, trip := dbconntest.Open(t)
	for _, q := range []string{
		`CREATE TABLE coder_session_turn_caps (profile_id TEXT, conversation_id TEXT, turn_id TEXT, cap_usd REAL, started INTEGER)`,
		`CREATE TABLE ai_chat_events (profile_id TEXT, conversation_id TEXT, turn_id TEXT, type TEXT, payload TEXT)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	s := &Store{db: db, now: time.Now}
	trip.RollbackErr = errors.New("disk I/O error")
	boom := errors.New("refused")
	err := s.carveBudgetAdmit("p", "m", "k", 4, func(querier, float64, bool) (string, error) { return "", boom })
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the admission's error", err)
	}
	trip.RollbackErr = nil
	dbconntest.RequireClean(t, db)
	if n := trip.Opened.Load(); n != 2 {
		t.Fatalf("connections opened = %d, want a fresh one after the drop", n)
	}
}
