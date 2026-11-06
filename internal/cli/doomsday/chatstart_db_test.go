package doomsday

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/tasks"
)

func (r *bossRig) starter() DBChatStart {
	return DBChatStart{Open: func() (*sql.DB, func(), error) { return r.db.DB, func() {}, nil }, Profile: func() string { return "p1" }}
}

func (r *bossRig) session(t *testing.T, id int64) tasks.Session {
	t.Helper()
	se, err := tasks.NewStore(r.db.DB).GetSession(context.Background(), "p1", id, tasks.Actor{Kind: tasks.Human})
	if err != nil {
		t.Fatal(err)
	}
	return se
}

// The boss's budget is the exact value asked for: a fixed amount, then no ceiling at all on a reused boss.
func TestSetBossBudgetIsExactIncludingNoCeiling(t *testing.T) {
	r := newBossRig(t)
	s := r.starter()
	ref, err := s.MakeBoss(context.Background(), r.chat(t, ai.AccessFull).ID, "g")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetBossBudget(context.Background(), ref.SessionID, 1.5); err != nil || r.session(t, ref.SessionID).BudgetUSD != 1.5 {
		t.Fatalf("fixed: %v %v", err, r.session(t, ref.SessionID).BudgetUSD)
	}
	if err := s.SetBossBudget(context.Background(), ref.SessionID, 0); err != nil || r.session(t, ref.SessionID).BudgetUSD != 0 {
		t.Fatalf("unlimited kept the old ceiling: %v %v", err, r.session(t, ref.SessionID).BudgetUSD)
	}
}

// Ending a boss stops then archives it through the store, twice is fine, and the chat's next attempt makes a fresh boss.
func TestEndBossStopsAndArchivesIdempotentlyAndTheChatCanTryAgain(t *testing.T) {
	r := newBossRig(t)
	s := r.starter()
	front := r.chat(t, ai.AccessFull).ID
	ctx := context.Background()
	ref, err := s.MakeBoss(ctx, front, "g")
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.MakeBoss(ctx, front, "g") // an unstamped boss left by a dead start: the same one
	if err != nil || again.SessionID != ref.SessionID || !again.Reused {
		t.Fatalf("reuse: %+v %v", again, err)
	}
	for i := 0; i < 2; i++ {
		if err := s.EndBoss(ctx, ref.SessionID); err != nil {
			t.Fatalf("end #%d: %v", i, err)
		}
	}
	if se := r.session(t, ref.SessionID); !se.Archived || se.State != tasks.SessionStopped {
		t.Fatalf("not stopped and archived: %+v", se)
	}
	next, err := s.MakeBoss(ctx, front, "g")
	if err != nil || next.SessionID == ref.SessionID || next.Reused {
		t.Fatalf("the next attempt: %+v %v", next, err)
	}
	var open int
	if err := r.db.DB.QueryRow(`SELECT COUNT(*) FROM task_sessions WHERE profile_id='p1' AND client_id LIKE 'boss-for-%' AND archived_at = ''`).Scan(&open); err != nil || open != 1 {
		t.Fatalf("open bosses = %d %v", open, err)
	}
}

func TestEndBossRefusesAnUnknownSessionAndQueueNeedsItsWire(t *testing.T) {
	r := newBossRig(t)
	s := r.starter()
	if err := s.EndBoss(context.Background(), 9999); err == nil {
		t.Fatal("ended a session that does not exist")
	}
	if err := s.QueueFirstTurn(context.Background(), "c", FirstTurn{}); err == nil || !strings.Contains(err.Error(), "cannot queue") {
		t.Fatalf("queue without a wire: %v", err)
	}
}
