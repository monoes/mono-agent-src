package doomsday

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/tasks"
)

// openRunWithBoss is a profile with an open run whose top-level lead (a chat's conversation) carries the run's stamp.
func openRunWithBoss(t *testing.T) (Wiring, *bossRig, string) {
	t.Helper()
	r := newBossRig(t)
	conv := r.chat(t, ai.AccessFull).ID
	w := Wiring{DB: r.db.DB, Profile: "p1"}
	se, err := tasks.NewStore(r.db.DB).CreateSession(context.Background(), "p1", tasks.SessionInput{Title: "lead", Goal: "g", ConversationID: conv,
		Kind: tasks.SessionKindMission, BudgetUSD: 10, MaxTasks: 5}, tasks.Actor{Kind: tasks.Human, Name: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.DB.Exec(`INSERT INTO doomsday_runs (profile, generation, charter_id, state, mac) VALUES ('p1', 1, 'c1', 'running', x'01')`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.DB.Exec(`UPDATE task_sessions SET doomsday_charter_id = 'c1', doomsday_generation = 1 WHERE id = ?`, se.ID); err != nil {
		t.Fatal(err)
	}
	return w, r, conv
}

func queueFirst(t *testing.T, db *sql.DB, conv string) {
	t.Helper()
	if _, err := coderhost.NewStore(db).Enqueue(coderhost.Command{ProfileID: "p1", ConversationID: conv, Kind: coderhost.KindSend, TurnID: "t-first", Message: "go"}); err != nil {
		t.Fatal(err)
	}
}

// The boss's first turn is read from the host's queue: pending, failed with the host's reason (#1521: dispatch refused it
// after the start had succeeded), or started once a turn exists. A profile with no run has none to report.
func TestFirstTurnOfReadsTheHostsAnswer(t *testing.T) {
	ctx := context.Background()
	w, r, conv := openRunWithBoss(t)
	db := r.db.DB
	if st, err := (Wiring{DB: db, Profile: "nobody"}).FirstTurnOf(ctx); err != nil || st.State != "" {
		t.Fatalf("no run: %+v %v", st, err)
	}
	if st, err := w.FirstTurnOf(ctx); err != nil || st.State != "none" {
		t.Fatalf("nothing queued: %+v %v", st, err)
	}
	queueFirst(t, db, conv)
	if st, _ := w.FirstTurnOf(ctx); st.State != "pending" {
		t.Fatalf("queued: %+v", st)
	}
	const why = "no run without binding: peer-credential binding is off; switch it on (coder set --peercred-binding on)"
	if _, err := db.Exec(`UPDATE coder_session_commands SET status = 'failed', error = ? WHERE conversation_id = ?`, why, conv); err != nil {
		t.Fatal(err)
	}
	if st, _ := w.FirstTurnOf(ctx); st.State != "failed" || !strings.Contains(st.Error, "no run without binding") {
		t.Fatalf("failed: %+v", st)
	}
	queueFirst(t, db, conv) // a retry
	if st, _ := w.FirstTurnOf(ctx); st.State != "pending" {
		t.Fatalf("retry: %+v", st)
	}
	if _, err := db.Exec(`INSERT INTO ai_chat_turns (id, conversation_id, profile_id, owner_instance_id, prompt, status, reason, last_committed_seq, created_at, updated_at)
		VALUES ('t-first', ?, 'p1', '', 'go', 'completed', '', 0, '', '')`, conv); err != nil {
		t.Fatal(err)
	}
	if st, _ := w.FirstTurnOf(ctx); st.State != "started" {
		t.Fatalf("turn exists: %+v", st)
	}
	// a stopped run's boss is nobody's first turn to report
	if _, err := db.Exec(`UPDATE doomsday_runs SET state = 'quiescent'`); err != nil {
		t.Fatal(err)
	}
	if st, _ := w.FirstTurnOf(ctx); st.State != "" {
		t.Fatalf("stopped run: %+v", st)
	}
}

// `status` says plainly when the first turn failed, in words and in JSON, and nothing about a first turn that is fine.
func TestStatusSaysWhenTheFirstTurnFailed(t *testing.T) {
	for _, c := range []struct {
		st   FirstTurnState
		want string
	}{
		{FirstTurnState{State: "failed", Error: "no run without binding: x"}, "first turn failed: no run without binding: x"},
		{FirstTurnState{State: "none"}, "first turn: not queued"},
		{FirstTurnState{State: "pending"}, ""},
		{FirstTurnState{State: "started"}, ""},
	} {
		r := newRig()
		r.deps.FirstTurn = func(context.Context) (FirstTurnState, error) { return c.st, nil }
		out, err := run(t, r.deps, "", "status")
		if err != nil || (c.want != "" && !strings.Contains(out, c.want)) || (c.want == "" && strings.Contains(out, "first turn")) {
			t.Fatalf("%+v: %q %v", c.st, out, err)
		}
		r.deps.JSONOutput = func() bool { return true }
		out, err = run(t, r.deps, "", "status")
		var got Status
		if err != nil || json.Unmarshal([]byte(out), &got) != nil || got.FirstTurn != c.st.State {
			t.Fatalf("%+v json: %q %v", c.st, out, err)
		}
	}
}
