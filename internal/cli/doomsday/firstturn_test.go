package doomsday

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/tasks"
)

// openRunWithBoss is a profile with an open run whose top-level lead (a chat's conversation) carries the run's stamp.
func openRunWithBoss(t *testing.T, state string) (Wiring, *bossRig, string) {
	t.Helper()
	r := newBossRig(t)
	conv := r.chat(t, ai.AccessFull).ID
	w := Wiring{DB: r.db.DB, Profile: "p1"}
	se, err := tasks.NewStore(r.db.DB).CreateSession(context.Background(), "p1", tasks.SessionInput{Title: "lead", Goal: "g", ConversationID: conv,
		Kind: tasks.SessionKindMission, BudgetUSD: 10, MaxTasks: 5}, tasks.Actor{Kind: tasks.Human, Name: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.DB.Exec(`INSERT INTO doomsday_runs (profile, generation, charter_id, state, mac) VALUES ('p1', 1, 'c1', ?, x'01')`, state); err != nil {
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
	w, r, conv := openRunWithBoss(t, "running")
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
}

// A stopped run's boss has no first turn to report.
func TestFirstTurnOfIgnoresAStoppedRun(t *testing.T) {
	w, _, _ := openRunWithBoss(t, "quiescent")
	if st, err := w.FirstTurnOf(context.Background()); err != nil || st.State != "" {
		t.Fatalf("stopped run: %+v %v", st, err)
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

// The host's reason is shown to a person and sent to the GUI: it can carry paths and raw bytes, so control characters and escape
// sequences are gone and it is cut. Mutants: no clean (red on the control check), no cut (red on the length check).
func TestFirstTurnReasonIsCleanedAndCut(t *testing.T) {
	w, r, conv := openRunWithBoss(t, "running")
	queueFirst(t, r.db.DB, conv)
	raw := "launcher: \x1b[31mred\x1b[0m\x1b]0;title\x07 bad\x00byte\r\nnext\u0085line " + strings.Repeat("é", 1000)
	if _, err := r.db.DB.Exec(`UPDATE coder_session_commands SET status = 'failed', error = ? WHERE conversation_id = ?`, raw, conv); err != nil {
		t.Fatal(err)
	}
	st, err := w.FirstTurnOf(context.Background())
	if err != nil || st.State != "failed" {
		t.Fatalf("%+v %v", st, err)
	}
	for _, c := range st.Error {
		if unicode.IsControl(c) {
			t.Fatalf("control character %U in %q", c, st.Error)
		}
	}
	if strings.Contains(st.Error, "[31m") || strings.Contains(st.Error, "title") || !strings.HasPrefix(st.Error, "launcher:") {
		t.Fatalf("escape sequence text left or the reason lost: %q", st.Error)
	}
	if n := utf8.RuneCountInString(st.Error); n > 301 {
		t.Fatalf("reason is %d runes", n)
	}
	b, _ := json.Marshal(st)
	if strings.Contains(string(b), "\\u001b") || strings.Contains(string(b), "\\u0000") {
		t.Fatalf("control bytes in the JSON: %s", b)
	}
}

// `status` with an OPEN run, through the read engine the GUI polls: it pins no connection and writes nothing.
// Mutant: status through the full engine pins (red on the pin count).
func TestStatusWithAnOpenRunPinsNothingAndWritesNothing(t *testing.T) {
	r := newFinRig(t, finishCharterJSON)
	r.start(t)
	pins := countingPins(t)
	before := allRows(t, r.w.DB)
	st, err := r.w.ReadEngine().Status(context.Background())
	if err != nil || !st.Active {
		t.Fatalf("status of the open run: %+v %v", st, err)
	}
	if pins.Load() != 0 || allRows(t, r.w.DB) != before {
		t.Fatalf("pins %d, rows %d -> %d", pins.Load(), before, allRows(t, r.w.DB))
	}
	out, err := run(t, Deps{Engine: func() Engine { return noStatusEngine{} }, ReadEngine: func() Engine { return r.w.ReadEngine() }}, "", "status")
	if err != nil || !strings.Contains(out, "run ") || pins.Load() != 0 {
		t.Fatalf("status verb: %q %v pins=%d", out, err, pins.Load())
	}
}

func allRows(t *testing.T, db *sql.DB) int {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		names = append(names, n)
	}
	rows.Close()
	total := 0
	for _, n := range names {
		var c int
		if err := db.QueryRow(`SELECT COUNT(*) FROM "` + n + `"`).Scan(&c); err != nil {
			t.Fatal(err)
		}
		total += c
	}
	return total
}

// noStatusEngine fails the test if `status` asks the full engine instead of the read engine.
type noStatusEngine struct{ Off }

func (noStatusEngine) Status(context.Context) (Status, error) {
	return Status{}, errors.New("status went through the full engine, not the read engine")
}
