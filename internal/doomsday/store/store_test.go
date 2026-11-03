package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/storage"
)

var ctx = context.Background()

func testKeys() Keyer { return NewHMACKeyer([]byte("test-vault-derived-key")) }

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	keyring.MockInit()
	db, err := storage.NewDatabase(t.TempDir() + "/dd.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.DB.Close() })
	db.DB.SetMaxOpenConns(1)
	return db.DB
}

func newStore(t *testing.T) (*SQLStore, *sql.DB) {
	db := openDB(t)
	return New(db, testKeys()), db
}

func charter(profile string, gen int64) Charter {
	return Charter{
		Profile: profile, Generation: gen, Goal: "ship it",
		Criteria: []Criterion{{Kind: "command", Spec: "go test ./...", Expected: "ok"}},
		Limits:   Limits{TreeDepth: 4, LivePerTree: 30, SpawnRate: 6, OpenSessions: 50, MaxConcurrent: 30, MaxTotal: 100, BudgetMicro: 5_000_000},
		Lines:    []string{LineSpending, LineInstall},
		BossPin:  Pin{"anthropic", "m1", "v1", "judging-v1"}, VerifierPin: Pin{"anthropic", "m2", "v1", "judging-v1"},
		PerAsk: PerAsk{Declared: true, SpendMicro: 1_000_000, Tasks: 5, Access: "edit", Depth: 2, RunSpendMicro: 10_000_000, RunTasks: 50, AnswerClasses: []string{"context", "clarify"}},
	}
}

func enter(t *testing.T, s *SQLStore, profile string) Run {
	t.Helper()
	g, err := s.NextGeneration(ctx, profile)
	if err != nil {
		t.Fatal(err)
	}
	c := charter(profile, g)
	r, err := s.Enter(ctx, c, c.Hash(), "operator")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func walk(t *testing.T, s *SQLStore, r Run, to ...RunState) {
	t.Helper()
	for _, st := range to {
		if _, err := s.Advance(ctx, r.Profile, r.Generation, st, "op", "test"); err != nil {
			t.Fatalf("advance to %s: %v", st, err)
		}
	}
}

func TestHashBindsEveryField(t *testing.T) {
	base := charter("p", 1)
	h := base.Hash()
	muts := map[string]func(c *Charter){
		"profile":    func(c *Charter) { c.Profile = "q" },
		"generation": func(c *Charter) { c.Generation = 2 },
		"goal":       func(c *Charter) { c.Goal = "other" },
		"criteria":   func(c *Charter) { c.Criteria[0].Expected = "fail" },
		"depth":      func(c *Charter) { c.Limits.TreeDepth = 5 },
		"budget":     func(c *Charter) { c.Limits.BudgetMicro++ },
		"line":       func(c *Charter) { c.Lines = append(c.Lines, LineVault) },
		"boss":       func(c *Charter) { c.BossPin.Version = "v2" },
		"verifier":   func(c *Charter) { c.VerifierPin.Model = "m3" },
		"perask":     func(c *Charter) { c.PerAsk.Tasks++ },
		"access":     func(c *Charter) { c.PerAsk.Access = "plan" },
	}
	for name, m := range muts {
		c := charter("p", 1)
		m(&c)
		if c.Hash() == h {
			t.Errorf("changing %s did not change the hash", name)
		}
	}
	// A field boundary shift must not collide (length delimiting).
	a, b := charter("p", 1), charter("p", 1)
	a.Goal, a.Criteria[0].Kind = "ab", "command"
	b.Goal = "a"
	b.Criteria = []Criterion{{Kind: "command", Spec: "go test ./...", Expected: "ok"}}
	if a.Hash() == b.Hash() {
		t.Error("collision")
	}
	// Lines are a set.
	x, y := charter("p", 1), charter("p", 1)
	x.Lines = []string{LineInstall, LineSpending, LineInstall}
	if x.Hash() != y.Hash() {
		t.Error("line order or duplicates changed the hash")
	}
}

func TestEverythingExcludesPlatformCaps(t *testing.T) {
	for _, l := range EverythingLines() {
		if l == LinePlatformCaps {
			t.Fatal("everything ticks the platform caps line")
		}
	}
	if len(EverythingLines()) != 6 {
		t.Fatal("everything must tick the six other lines")
	}
}

func TestValidate(t *testing.T) {
	ok := charter("p", 1)
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(c *Charter){
		"depth 13":          func(c *Charter) { c.Limits.TreeDepth = 13 },
		"depth 0":           func(c *Charter) { c.Limits.TreeDepth = 0 },
		"depth 12 is ok":    nil,
		"no budget choice":  func(c *Charter) { c.Limits.BudgetMicro = 0 },
		"both budget forms": func(c *Charter) { c.Limits.NoCeiling = true },
		"unknown line":      func(c *Charter) { c.Lines = []string{"everything"} },
		"verifier = boss":   func(c *Charter) { c.VerifierPin = c.BossPin },
		"unpinned boss":     func(c *Charter) { c.BossPin.Version = "" },
		"ask access full":   func(c *Charter) { c.PerAsk.Access = "full" },
		"undeclared values": func(c *Charter) { c.PerAsk.Declared = false },
		"bad criterion":     func(c *Charter) { c.Criteria[0].Kind = "vibes" },
		"no profile":        func(c *Charter) { c.Profile = "" },
	}
	for name, m := range cases {
		c := charter("p", 1)
		if m == nil {
			c.Limits.TreeDepth = 12
			if err := c.Validate(); err != nil {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		m(&c)
		if err := c.Validate(); !errors.Is(err, ErrInvalidCharter) {
			t.Errorf("%s: got %v", name, err)
		}
	}
	d := Charter{}.WithDefaults()
	if d.Limits.TreeDepth != 4 || d.Limits.LivePerTree != 30 || d.Limits.MaxConcurrent != 30 {
		t.Errorf("defaults %+v", d.Limits)
	}
	// no per-ask limits: nothing auto-approved, and that is valid.
	n := charter("p", 1)
	n.PerAsk = PerAsk{}
	if err := n.Validate(); err != nil {
		t.Error(err)
	}
}

func TestEnter(t *testing.T) {
	s, _ := newStore(t)
	c := charter("p", 1)
	if _, err := s.Enter(ctx, c, [32]byte{1}, "op"); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("wrong confirmed hash: %v", err)
	}
	if _, err := s.Enter(ctx, charter("p", 3), charter("p", 3).Hash(), "op"); !errors.Is(err, ErrGenerationMoved) {
		t.Fatalf("skipped generation: %v", err)
	}
	// The operator confirmed the hash of generation 1; entering a charter that differs from it refuses.
	other := charter("p", 1)
	other.Goal = "something else"
	if _, err := s.Enter(ctx, other, c.Hash(), "op"); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("charter differing from the confirmed hash: %v", err)
	}
	r, err := s.Enter(ctx, c, c.Hash(), "op")
	if err != nil || r.State != StateRunning || r.CharterID != c.ID() {
		t.Fatalf("%+v %v", r, err)
	}
	got, err := s.LoadCharter(ctx, "p", 1)
	if err != nil || got.Hash() != c.Hash() {
		t.Fatalf("load: %v", err)
	}
	// One active run per profile; the generation moved, so a second entry for generation 1 or 2 is refused.
	c2 := charter("p", 2)
	if _, err := s.Enter(ctx, c2, c2.Hash(), "op"); !errors.Is(err, ErrActiveRun) {
		t.Fatalf("second active run: %v", err)
	}
	// Another profile is independent.
	enter(t, s, "other")
	if err := s.VerifyAudit(ctx, "p", 1); err != nil {
		t.Fatal(err)
	}
}

func TestOneActiveRunIndexAlone(t *testing.T) {
	s, db := newStore(t)
	enter(t, s, "p")
	// Bypass the Go checks: the partial unique index is the backstop.
	_, err := db.Exec(`INSERT INTO doomsday_runs(profile, generation, charter_id, state, mac) VALUES ('p', 9, 'x', 'running', x'00')`)
	if !isUnique(err) {
		t.Fatalf("index did not refuse: %v", err)
	}
}

func TestGenerationNeverReusedAndRunsNeedQuiescence(t *testing.T) {
	s, _ := newStore(t)
	r := enter(t, s, "p")
	walk(t, s, r, StateStopRequested, StateAuthorityRevoked, StateCleanupPending, StateQuiescent)
	r2 := enter(t, s, "p")
	if r2.Generation != 2 {
		t.Fatalf("generation %d", r2.Generation)
	}
}

func TestStateMachineForwardOnly(t *testing.T) {
	s, db := newStore(t)
	r := enter(t, s, "p")
	for _, bad := range []RunState{StateAuthorityRevoked, StateCleanupPending, StateQuiescent, StateRunning} {
		if _, err := s.Advance(ctx, "p", r.Generation, bad, "op", ""); !errors.Is(err, ErrBadTransition) {
			t.Errorf("skip to %s: %v", bad, err)
		}
	}
	walk(t, s, r, StateStopRequested)
	if _, err := s.Advance(ctx, "p", 1, StateRunning, "op", ""); !errors.Is(err, ErrBadTransition) {
		t.Errorf("backward: %v", err)
	}
	// Raw SQL cannot go backward or skip either (trigger backstop).
	if _, err := db.Exec(`UPDATE doomsday_runs SET state = 'running'`); err == nil {
		t.Error("raw backward update accepted")
	}
	if _, err := db.Exec(`UPDATE doomsday_runs SET state = 'quiescent'`); err == nil {
		t.Error("raw skip accepted")
	}
	if _, err := db.Exec(`DELETE FROM doomsday_runs`); err == nil {
		t.Error("raw delete accepted")
	}
	g, _ := s.GetRun(ctx, "p", 1)
	if g.State != StateStopRequested || g.IntentBy != "op" || g.IntentAt == 0 {
		t.Errorf("%+v", g)
	}
}

// 13.3 P1: a live member blocks quiescence and a new run whatever the repair state is.
func TestLiveMemberBlocksQuiescentAndNewRun(t *testing.T) {
	s, db := newStore(t)
	r := enter(t, s, "p")
	if err := s.Register(ctx, Member{Profile: "p", Generation: 1, ID: "proc-1", Kind: "process", Pid: 42, Stamp: "s"}); err != nil {
		t.Fatal(err)
	}
	walk(t, s, r, StateStopRequested, StateAuthorityRevoked, StateCleanupPending)
	if _, err := s.Advance(ctx, "p", 1, StateQuiescent, "op", ""); !errors.Is(err, ErrLiveMembers) {
		t.Fatalf("quiescent with a live member: %v", err)
	}
	if _, err := db.Exec(`UPDATE doomsday_runs SET state = 'quiescent' WHERE profile = 'p'`); err == nil {
		t.Fatal("raw quiescent with a live member accepted")
	}
	// The live member also blocks a new run even if the run row were forced forward by other means.
	c := charter("p", 2)
	if _, err := s.Enter(ctx, c, c.Hash(), "op"); !errors.Is(err, ErrLiveMembers) && !errors.Is(err, ErrActiveRun) {
		t.Fatalf("new run with live member: %v", err)
	}
	if err := s.Release(ctx, "p", 1, "proc-1"); err != nil {
		t.Fatal(err)
	}
	if ms, _ := s.LiveMembers(ctx, "p", 1); len(ms) != 0 {
		t.Fatal("still live")
	}
	if _, err := s.Advance(ctx, "p", 1, StateQuiescent, "op", ""); err != nil {
		t.Fatal(err)
	}
	// A released member never becomes live again.
	if _, err := db.Exec(`UPDATE doomsday_members SET live = 1`); err == nil {
		t.Fatal("revived member")
	}
	if _, err := db.Exec(`DELETE FROM doomsday_members`); err == nil {
		t.Fatal("deleted member")
	}
}

func TestLiveMemberOfQuiescentRunBlocksEnter(t *testing.T) {
	s, db := newStore(t)
	r := enter(t, s, "p")
	walk(t, s, r, StateStopRequested, StateAuthorityRevoked, StateCleanupPending, StateQuiescent)
	// Simulate a member that appears after quiescence (a repair gone wrong): the entry transaction still refuses.
	if _, err := db.Exec(`INSERT INTO doomsday_members(profile, generation, member_id, kind) VALUES ('p', 1, 'late', 'process')`); err != nil {
		t.Fatal(err)
	}
	c := charter("p", 2)
	if _, err := s.Enter(ctx, c, c.Hash(), "op"); !errors.Is(err, ErrLiveMembers) {
		t.Fatalf("got %v", err)
	}
}

func TestAdmitAndRegisterRefuse(t *testing.T) {
	s, _ := newStore(t)
	r := enter(t, s, "p")
	if err := s.Admit(ctx, s.db, "p", 1); err != nil {
		t.Fatal(err)
	}
	if err := s.Admit(ctx, s.db, "p", 7); err == nil {
		t.Fatal("unknown generation admitted")
	}
	if err := s.Admit(ctx, s.db, "nobody", 1); err == nil {
		t.Fatal("unknown profile admitted")
	}
	walk(t, s, r, StateStopRequested)
	var ref *Refusal
	if err := s.Admit(ctx, s.db, "p", 1); !errors.As(err, &ref) {
		t.Fatalf("admitted after stop intent: %v", err)
	}
	if err := s.Register(ctx, Member{Profile: "p", Generation: 1, ID: "m", Kind: "session"}); !errors.As(err, &ref) {
		t.Fatalf("registered after stop intent: %v", err)
	}
	walk(t, s, r, StateAuthorityRevoked, StateCleanupPending, StateQuiescent)
	enter(t, s, "p")
	if err := s.Admit(ctx, s.db, "p", 1); !errors.As(err, &ref) {
		t.Fatalf("old generation admitted: %v", err)
	}
	if err := s.Admit(ctx, s.db, "p", 2); err != nil {
		t.Fatal(err)
	}
}

func TestTamperFailsClosed(t *testing.T) {
	s, db := newStore(t)
	enter(t, s, "p")
	// Remove the triggers to simulate an editor of the file, then edit rows.
	for _, tr := range []string{"doomsday_runs_forward_only", "doomsday_charters_no_update", "doomsday_runs_quiescent_needs_no_live"} {
		if _, err := db.Exec("DROP TRIGGER " + tr); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE doomsday_runs SET cause = 'forged'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetRun(ctx, "p", 1); !errors.Is(err, ErrUnverifiable) {
		t.Fatalf("forged run row: %v", err)
	}
	var ref *Refusal
	if err := s.Admit(ctx, s.db, "p", 1); !errors.As(err, &ref) {
		t.Fatalf("forged run admitted: %v", err)
	}
	if _, err := s.Advance(ctx, "p", 1, StateStopRequested, "op", ""); !errors.Is(err, ErrUnverifiable) {
		t.Fatalf("advanced a forged row: %v", err)
	}
	// Edited charter body: hash no longer matches.
	if _, err := db.Exec(`UPDATE doomsday_charters SET body = replace(body, 'ship it', 'ship more')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadCharter(ctx, "p", 1); !errors.Is(err, ErrUnverifiable) {
		t.Fatalf("edited charter: %v", err)
	}
	// Edited charter with a fixed-up hash but without the key: MAC fails.
	c := charter("p", 1)
	c.Goal = "ship more"
	h := c.Hash()
	if _, err := db.Exec(`UPDATE doomsday_charters SET body = ?, charter_hash = ?, charter_id = ?`, mustJSON(c), h[:], c.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadCharter(ctx, "p", 1); !errors.Is(err, ErrUnverifiable) {
		t.Fatalf("hash fixed up without the key: %v", err)
	}
	// An unknown schema is refused, not guessed at.
	if _, err := db.Exec(`UPDATE doomsday_charters SET canon_version = 99`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadCharter(ctx, "p", 1); !errors.Is(err, ErrUnknownSchema) {
		t.Fatalf("unknown schema: %v", err)
	}
}

func TestWrongKeyFailsClosed(t *testing.T) {
	s, db := newStore(t)
	enter(t, s, "p")
	rotated := New(db, NewHMACKeyer([]byte("another key")))
	if _, err := rotated.GetRun(ctx, "p", 1); !errors.Is(err, ErrUnverifiable) {
		t.Fatalf("rotated key: %v", err)
	}
	if _, err := rotated.LoadCharter(ctx, "p", 1); !errors.Is(err, ErrUnverifiable) {
		t.Fatalf("rotated key charter: %v", err)
	}
	if err := rotated.VerifyAudit(ctx, "p", 1); !errors.Is(err, ErrUnverifiable) {
		t.Fatalf("rotated key audit: %v", err)
	}
	noKey := New(db, NewHMACKeyer(nil))
	if err := noKey.Admit(ctx, db, "p", 1); err == nil {
		t.Fatal("admitted without a key")
	}
}

func TestInsertOnlyTables(t *testing.T) {
	s, db := newStore(t)
	enter(t, s, "p")
	if err := s.ConsumeCumulative(ctx, "p", 1, "spend", "a1", 5, 10); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`UPDATE doomsday_charters SET body = 'x'`, `DELETE FROM doomsday_charters`,
		`UPDATE doomsday_audit SET payload = 'x'`, `DELETE FROM doomsday_audit`,
		`UPDATE doomsday_counter_deltas SET n = 1`, `DELETE FROM doomsday_counter_deltas`,
	} {
		if _, err := db.Exec(q); err == nil {
			t.Errorf("accepted: %s", q)
		}
	}
}

func TestAuditChain(t *testing.T) {
	s, db := newStore(t)
	r := enter(t, s, "p")
	for i := 0; i < 4; i++ {
		if _, err := s.AppendAudit(ctx, "p", 1, "note", fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	walk(t, s, r, StateStopRequested)
	if err := s.VerifyAudit(ctx, "p", 1); err != nil {
		t.Fatal(err)
	}
	drop := func(tr string) {
		if _, err := db.Exec("DROP TRIGGER " + tr); err != nil {
			t.Fatal(err)
		}
	}
	drop("doomsday_audit_no_update")
	drop("doomsday_audit_no_delete")
	// An edited row.
	if _, err := db.Exec(`UPDATE doomsday_audit SET payload = 'edited' WHERE seq = 3`); err != nil {
		t.Fatal(err)
	}
	if err := s.VerifyAudit(ctx, "p", 1); !errors.Is(err, ErrUnverifiable) {
		t.Fatalf("edited row: %v", err)
	}
	if _, err := db.Exec(`UPDATE doomsday_audit SET payload = '1' WHERE seq = 3`); err != nil {
		t.Fatal(err)
	}
	if err := s.VerifyAudit(ctx, "p", 1); err != nil {
		t.Fatal(err)
	}
	// A removed middle row, then a re-sequenced one.
	if _, err := db.Exec(`DELETE FROM doomsday_audit WHERE seq = 3`); err != nil {
		t.Fatal(err)
	}
	if err := s.VerifyAudit(ctx, "p", 1); !errors.Is(err, ErrUnverifiable) {
		t.Fatalf("removed row: %v", err)
	}
	if _, err := db.Exec(`UPDATE doomsday_audit SET seq = seq - 1 WHERE seq > 3`); err != nil {
		t.Fatal(err)
	}
	if err := s.VerifyAudit(ctx, "p", 1); !errors.Is(err, ErrUnverifiable) {
		t.Fatalf("resequenced rows: %v", err)
	}
}

func TestAuditHeadIsUniquePerSeq(t *testing.T) {
	s, db := newStore(t)
	enter(t, s, "p")
	if _, err := s.AppendAudit(ctx, "p", 1, "k", "v"); err != nil {
		t.Fatal(err)
	}
	// A second row at an existing seq (a fork) is refused by the key.
	_, err := db.Exec(`INSERT INTO doomsday_audit(profile, generation, seq, kind, payload, prev, mac) VALUES ('p', 1, 2, 'k', 'fork', x'00', x'00')`)
	if !isUnique(err) {
		t.Fatalf("fork accepted: %v", err)
	}
}

func TestCumulativeCounters(t *testing.T) {
	s, _ := newStore(t)
	r := enter(t, s, "p")
	if err := s.ConsumeCumulative(ctx, "p", 1, "spend", "a1", 6, 10); err != nil {
		t.Fatal(err)
	}
	// Many small asks cross the limit at the crossing.
	if err := s.ConsumeCumulative(ctx, "p", 1, "spend", "a2", 5, 10); !errors.Is(err, ErrOverLimit) {
		t.Fatalf("crossing: %v", err)
	}
	if err := s.ConsumeCumulative(ctx, "p", 1, "spend", "a3", 4, 10); err != nil {
		t.Fatalf("exact fit: %v", err)
	}
	if err := s.ConsumeCumulative(ctx, "p", 1, "spend", "a4", 1, 10); !errors.Is(err, ErrOverLimit) {
		t.Fatalf("full: %v", err)
	}
	// A different kind counts separately; the same approval id cannot be spent twice.
	if err := s.ConsumeCumulative(ctx, "p", 1, "tasks", "t1", 3, 5); err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumeCumulative(ctx, "p", 1, "tasks", "t1", 1, 5); !errors.Is(err, ErrAlreadyConsumed) {
		t.Fatalf("replay: %v", err)
	}
	if err := s.ConsumeCumulative(ctx, "p", 1, "spend2", "t1", 1, 5); !errors.Is(err, ErrAlreadyConsumed) {
		t.Fatalf("approval id reused for another kind: %v", err)
	}
	// Overflow arithmetic: a huge n must not wrap around.
	if err := s.ConsumeCumulative(ctx, "p", 1, "big", "b1", 1<<62, 1<<62); err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumeCumulative(ctx, "p", 1, "big", "b2", 1<<62, 1<<62); !errors.Is(err, ErrOverLimit) {
		t.Fatalf("overflow: %v", err)
	}
	if err := s.ConsumeCumulative(ctx, "p", 1, "x", "z0", 0, 10); !errors.Is(err, ErrOverLimit) {
		t.Fatalf("zero: %v", err)
	}
	if err := s.ConsumeCumulative(ctx, "p", 1, "x", "z1", -1, 10); !errors.Is(err, ErrOverLimit) {
		t.Fatalf("negative: %v", err)
	}
	// Not after Stop.
	walk(t, s, r, StateStopRequested)
	var ref *Refusal
	if err := s.ConsumeCumulative(ctx, "p", 1, "tasks", "t9", 1, 100); !errors.As(err, &ref) {
		t.Fatalf("consumed after stop intent: %v", err)
	}
	if err := s.VerifyAudit(ctx, "p", 1); err != nil {
		t.Fatal(err)
	}
}

func TestCumulativeIsAtomicUnderContention(t *testing.T) {
	keyring.MockInit()
	st, err := storage.NewDatabase(t.TempDir() + "/c.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.DB.Close()
	if err := st.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	st.DB.SetMaxOpenConns(8)
	s := New(st.DB, testKeys())
	enter(t, s, "p")
	var ok atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			switch err := s.ConsumeCumulative(ctx, "p", 1, "spend", fmt.Sprint("a", i), 1, 10); {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, ErrOverLimit):
			default:
				// contention errors (busy) are not successes either
			}
		}(i)
	}
	wg.Wait()
	var sum int
	if err := st.DB.QueryRow(`SELECT COALESCE(SUM(n),0) FROM doomsday_counter_deltas`).Scan(&sum); err != nil {
		t.Fatal(err)
	}
	if sum > 10 || int(ok.Load()) != sum {
		t.Fatalf("sum %d successes %d", sum, ok.Load())
	}
}

func TestSessionColumns(t *testing.T) {
	_, db := newStore(t)
	for _, tbl := range []string{"coder_sessions", "task_sessions"} {
		rows, err := db.Query(`SELECT name, dflt_value FROM pragma_table_info('` + tbl + `') WHERE name LIKE 'doomsday_%' ORDER BY name`)
		if err != nil {
			t.Fatal(err)
		}
		var n int
		for rows.Next() {
			var name string
			var d sql.NullString
			rows.Scan(&name, &d)
			n++
		}
		rows.Close()
		if n != 2 {
			t.Errorf("%s: %d doomsday columns", tbl, n)
		}
	}
}

func TestOffByDefault(t *testing.T) {
	_, db := newStore(t)
	for _, tbl := range []string{"doomsday_charters", "doomsday_runs", "doomsday_members", "doomsday_audit", "doomsday_counter_deltas"} {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + tbl).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s: %d %v", tbl, n, err)
		}
	}
}

func mustJSON(c Charter) string {
	b, _ := json.Marshal(c.normalized())
	return string(b)
}
