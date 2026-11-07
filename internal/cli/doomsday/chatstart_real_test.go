package doomsday

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/tasks"
)

// The start from a chat over the real engine and the real database: what is stored is what the cleanup and the repeat rules
// promise. Only the model-facing first turn is a fake.

type failEnter struct{ *StoreEngine }

func (failEnter) Enter(context.Context, Canon, string, Ack) (Entry, error) {
	return Entry{}, errors.New("the entry is refused before anything was created")
}

type realChat struct {
	*wireRig
	db    *sql.DB
	sdb   *storage.Database
	chat  string
	turns []FirstTurn
	lock  string
	queue func(FirstTurn) error // nil: the first turn queues
}

func newRealChat(t *testing.T) *realChat {
	t.Helper()
	w, db := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	w.w.DB = db.DB
	was := coderhost.OpenModeAvailability
	coderhost.OpenModeAvailability = func() (bool, string) { return true, "" }
	t.Cleanup(func() { coderhost.OpenModeAvailability = was })
	if _, err := db.DB.Exec(`INSERT OR IGNORE INTO profiles (id, name) VALUES ('p1', 'P1')`); err != nil {
		t.Fatal(err)
	}
	st, err := ai.NewAIStore(db.DB)
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.CreateConversationAccess("p1", "agent", "general", "claude", "", "sonnet", ai.AccessFull, t.TempDir(), "high")
	if err != nil {
		t.Fatal(err)
	}
	if err := coderhost.NewStore(db.DB).SetChatMode("p1", c.ID, coderhost.ChatModeOpen); err != nil {
		t.Fatal(err)
	}
	r := &realChat{wireRig: w, db: db.DB, sdb: db, chat: c.ID, lock: filepath.Join(t.TempDir(), "start.lock")}
	e := w.w.Engine()
	w.deps.Engine = func() Engine { return e }
	w.deps.JSONOutput = func() bool { return true }
	w.deps.ChatStart = func() ChatStarter {
		return DBChatStart{Open: func() (*sql.DB, func(), error) { return db.DB, func() {}, nil }, Profile: func() string { return "p1" },
			LockPath: func() string { return r.lock },
			Queue: func(_ context.Context, _ string, in FirstTurn) error {
				if r.queue != nil {
					return r.queue(in)
				}
				r.turns = append(r.turns, in)
				return nil
			}}
	}
	w.charter = writeCharter(t, finishCharterJSON)
	return r
}

func (r *realChat) start(t *testing.T) (string, error) {
	t.Helper()
	h := fullHash(t, r.rig, r.charter)
	return run(t, r.deps, "", "start", "--charter", r.charter, "--gui-phrase", Phrase(0, h), "--chat", r.chat)
}

func (r *realChat) bosses(t *testing.T, where string) int {
	t.Helper()
	return count(t, r.sdb, `SELECT COUNT(*) FROM task_sessions WHERE profile_id='p1' AND client_id LIKE 'boss-for-%' AND `+where)
}

func TestChatStartOverTheRealEngineStampsTheBossFundsItAndSecondStartIsRefused(t *testing.T) {
	r := newRealChat(t)
	if out, err := r.start(t); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if n := r.bosses(t, `archived_at = '' AND doomsday_charter_id <> '' AND budget_usd = 5`); n != 1 {
		t.Fatalf("stamped and funded bosses = %d", n)
	}
	if len(r.turns) != 1 || r.turns[0].Goal != "ship it" || !strings.Contains(r.turns[0].RunID, "p1/") {
		t.Fatalf("first turn = %+v", r.turns)
	}
	before := r.bosses(t, `1=1`)
	out, err := r.start(t)
	if err == nil || !strings.Contains(err.Error(), "a run already exists for this profile") {
		t.Fatalf("second start: %v %s", err, out)
	}
	if r.bosses(t, `1=1`) != before || len(r.turns) != 1 || count(t, r.sdb, `SELECT COUNT(*) FROM doomsday_runs`) != 1 {
		t.Fatal("a second start made a boss, a run or a turn")
	}
}

// A refused entry ends the boss it made (stopped and archived, never deleted), every time, and leaves no run.
func TestChatStartRefusedEntryEndsTheBossAndTwiceLeavesNoPile(t *testing.T) {
	r := newRealChat(t)
	e := failEnter{r.w.Engine()}
	r.deps.Engine = func() Engine { return e }
	for i := 0; i < 2; i++ {
		if out, err := r.start(t); err == nil || !strings.Contains(err.Error(), "refused") {
			t.Fatalf("#%d: %v %s", i, err, out)
		}
	}
	if open := r.bosses(t, `archived_at = ''`); open != 0 {
		t.Fatalf("%d open boss(es) left", open)
	}
	if n := r.bosses(t, `archived_at <> '' AND state = 'stopped'`); n != 2 {
		t.Fatalf("ended bosses = %d, want 2 (one per attempt)", n)
	}
	if count(t, r.sdb, `SELECT COUNT(*) FROM doomsday_runs`) != 0 || len(r.turns) != 0 {
		t.Fatal("a refused start left a run or a turn")
	}
	// the chat can still start afterwards
	r.deps.Engine = func() Engine { return r.w.Engine() }
	if out, err := r.start(t); err != nil {
		t.Fatalf("start after refusals: %v %s", err, out)
	}
}

// A boss left unstamped by a start that died (the budget of a fixed attempt on it) is taken again, and an unlimited charter
// clears its ceiling.
func TestChatStartTakesAgainTheBossAStartLeftBehind(t *testing.T) {
	r := newRealChat(t)
	left, err := DBChatStart{Open: func() (*sql.DB, func(), error) { return r.db, func() {}, nil }, Profile: func() string { return "p1" }}.MakeBoss(context.Background(), r.chat, "g")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.NewStore(r.db).UpdateSession(context.Background(), "p1", left.SessionID, tasks.SessionUpdate{BudgetUSD: ptr(9.0)}, bossActor); err != nil {
		t.Fatal(err)
	}
	r.charter = writeCharter(t, strings.Replace(finishCharterJSON, `"budget_micro":5000000`, `"no_ceiling":true`, 1))
	if out, err := r.start(t); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if r.bosses(t, `1=1`) != 1 || r.bosses(t, `id = `+itoa(left.SessionID)+` AND doomsday_charter_id <> '' AND budget_usd = 0`) != 1 {
		t.Fatal("the left-behind boss was not reused and cleared of its old ceiling")
	}
}

func ptr(f float64) *float64 { return &f }

// lateFailEnter really enters the run and then reports a failure, as an entry does when its last step (the anchor) fails.
type lateFailEnter struct{ *StoreEngine }

func (l lateFailEnter) Enter(ctx context.Context, c Canon, h string, a Ack) (Entry, error) {
	if _, err := l.StoreEngine.Enter(ctx, c, h, a); err != nil {
		return Entry{}, err
	}
	return Entry{}, recorded(errors.New("run p1/1 started but its anchor could not be recorded (run `doomsday stop`)"))
}

// An Enter that fails AFTER the run exists keeps the boss (open, unstamped, found again by the next start) and the run.
func TestChatStartKeepsTheBossWhenTheRealEntryFailsAfterTheRunExists(t *testing.T) {
	r := newRealChat(t)
	e := lateFailEnter{r.w.Engine()}
	r.deps.Engine = func() Engine { return e }
	out, err := r.start(t)
	if err == nil || !strings.Contains(err.Error(), " started but ") {
		t.Fatalf("%v %s", err, out)
	}
	if count(t, r.sdb, `SELECT COUNT(*) FROM doomsday_runs WHERE state = 'running'`) != 1 {
		t.Fatal("the run does not exist")
	}
	if r.bosses(t, `archived_at = '' AND state <> 'stopped'`) != 1 || r.bosses(t, `archived_at <> ''`) != 0 {
		t.Fatal("the boss of a run that exists was ended")
	}
}

// The real first-turn queue refusing leaves a started run with its stamped, open boss, and the answer says so.
func TestChatStartRealFirstTurnRefusalIsAVisibleStateNotAFailure(t *testing.T) {
	r := newRealChat(t)
	r.queue = func(FirstTurn) error { return errors.New("the host would not queue it") }
	out, err := r.start(t)
	if err != nil || !strings.Contains(out, `"first_turn":"failed"`) || !strings.Contains(out, "would not queue") {
		t.Fatalf("%v %s", err, out)
	}
	if r.bosses(t, `archived_at = '' AND doomsday_charter_id <> ''`) != 1 {
		t.Fatal("the started run lost its boss")
	}
}

// Two starts at once: the one that holds the profile's lock goes on, the other is refused as busy and makes nothing.
func TestChatStartLockAdmitsOneStartAtATime(t *testing.T) {
	r := newRealChat(t)
	s := DBChatStart{LockPath: func() string { return r.lock }}
	release, err := s.Lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.start(t)
	if err == nil || !strings.Contains(err.Error(), "a start is already in progress") {
		t.Fatalf("%v %s", err, out)
	}
	if r.bosses(t, `1=1`) != 0 {
		t.Fatal("a refused start made a boss")
	}
	release()
	if out, err := r.start(t); err != nil {
		t.Fatalf("after release: %v %s", err, out)
	}
}
