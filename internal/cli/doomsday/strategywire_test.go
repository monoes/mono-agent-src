//go:build doomsday

package doomsday

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/doomsday/store"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/epoch"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/judge"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/novelty"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/policy"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
	"github.com/monoes/mono-agent/internal/storage"
)

var bg = context.Background()

// scriptedModel answers every judging prompt with a valid judgment: all criteria at crit, all directions kept.
type scriptedModel struct {
	mu    sync.Mutex
	crit  types.CriterionStatus
	calls int
}

func (m *scriptedModel) Pin() string { return "fake/judge/v1" }
func (m *scriptedModel) Complete(_ context.Context, req judge.Request) ([]byte, error) {
	var doc struct {
		Criteria   []struct{ ID string } `json:"criteria"`
		Directions []struct{ ID string } `json:"directions"`
	}
	if err := json.Unmarshal([]byte(req.User), &doc); err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.calls++
	st := m.crit
	m.mu.Unlock()
	j := types.Judgment{Criteria: []types.CriterionProgress{}, Directions: []types.DirectionAssessment{}, NextActions: []types.NextAction{}}
	for _, c := range doc.Criteria {
		j.Criteria = append(j.Criteria, types.CriterionProgress{ID: c.ID, Status: st})
	}
	for _, d := range doc.Directions {
		j.Directions = append(j.Directions, types.DirectionAssessment{ID: d.ID, Verdict: types.VerdictKeep})
	}
	return json.Marshal(j)
}

func (m *scriptedModel) set(s types.CriterionStatus) { m.mu.Lock(); m.crit = s; m.mu.Unlock() }

// fixedRuns is a StrategyRunSource the test controls.
type fixedRuns struct {
	mu  sync.Mutex
	run StrategyRun
	ok  bool
}

func (f *fixedRuns) Current(context.Context) (StrategyRun, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.run, f.ok, nil
}
func (f *fixedRuns) set(fn func(*StrategyRun)) { f.mu.Lock(); fn(&f.run); f.mu.Unlock() }

// passSeq is a sequencer with no witness behind it: it runs fn and counts the transitions. The tests that need the
// witness itself build the real one (strategywitness_test.go).
type passSeq struct {
	mu    sync.Mutex
	calls int
	rows  int
}

func (p *passSeq) SequenceRows(ctx context.Context, _ int64, _ string, rows int, fn func(context.Context) error) error {
	p.mu.Lock()
	p.calls++
	p.rows += rows
	p.mu.Unlock()
	return fn(ctx)
}

type fenv struct {
	db     *sql.DB
	core   *store.SQLStore
	keys   store.Keyer
	prof   string
	gen    int64
	clock  *epoch.FakeClock
	model  *scriptedModel
	runs   *fixedRuns
	host   *StrategyHost
	finish *finishRecorder
	subj   time.Time
}

type finishRecorder struct {
	mu    sync.Mutex
	calls int
	rep   BeginReport
}

func (f *finishRecorder) begin(context.Context) (BeginReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.rep, nil
}
func (f *finishRecorder) n() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

func newFenv(t *testing.T, mut func(*StrategyHostDeps)) *fenv {
	t.Helper()
	keyring.MockInit()
	d, err := storage.NewDatabase(t.TempDir() + "/dd.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.DB.Close() })
	d.DB.SetMaxOpenConns(1)
	keys := store.NewHMACKeyer([]byte("test-vault-derived-key"))
	core := store.New(d.DB, keys)
	e := &fenv{db: d.DB, core: core, keys: keys, prof: "p1", model: &scriptedModel{crit: types.CritProgressing}}
	g, err := core.NextGeneration(bg, e.prof)
	if err != nil {
		t.Fatal(err)
	}
	c := store.Charter{
		Profile: e.prof, Generation: g, Goal: "ship it",
		Criteria: []store.Criterion{{Kind: "command", Spec: "go test ./...", Expected: "ok"}},
		Limits:   store.Limits{TreeDepth: 4, LivePerTree: 30, SpawnRate: 6, OpenSessions: 50, MaxConcurrent: 30, MaxTotal: 100, BudgetMicro: 5_000_000},
		Lines:    []string{store.LineSpending, store.LineInstall},
		BossPin:  store.Pin{Provider: "anthropic", Model: "m1", Version: "v1", PurposePolicy: "judging-v1"}, VerifierPin: store.Pin{Provider: "anthropic", Model: "m2", Version: "v1", PurposePolicy: "judging-v1"},
		PerAsk: store.PerAsk{Declared: true, SpendMicro: 1_000_000, Tasks: 5, Access: "edit", Depth: 2, RunSpendMicro: 10_000_000, RunTasks: 50, AnswerClasses: []string{"context", "clarify"}},
	}
	if _, err := core.Enter(bg, c, c.Hash(), "operator"); err != nil {
		t.Fatal(err)
	}
	e.gen = g
	e.clock = epoch.NewFakeClock(time.Unix(1_800_000_000, 0))
	e.subj = e.clock.Now().Add(-24 * time.Hour)
	e.finish = &finishRecorder{rep: BeginReport{Refused: "not yet"}}
	e.runs = &fixedRuns{ok: true, run: StrategyRun{Generation: g, CharterID: c.ID(), Running: true, Boss: 7, HasBoss: true,
		Criteria: []StrategyCriterion{{ID: store.CriterionID(0), Text: "go test ./..."}}}}
	deps := StrategyHostDeps{DB: d.DB, Keys: keys, Audit: NewStoreTxAppender(core), Seq: &passSeq{}, Profile: e.prof, Runs: e.runs,
		Model: e.model, Refuse: func(string) bool { return false }, Timeout: time.Second, Clock: e.clock,
		Finish:           e.finish.begin,
		SubjectChangedAt: func(context.Context, int64) (time.Time, error) { return e.subj, nil },
		Similar:          func(context.Context, string) ([]novelty.Hit, error) { return nil, nil }}
	if mut != nil {
		mut(&deps)
	}
	e.host, err = NewStrategyHost(deps)
	if err != nil {
		t.Fatal(err)
	}
	// The first tick builds the engine, whose timer counts from then: nothing is due yet.
	if tk, err := e.host.Tick(bg); err != nil || tk.Epoch == nil || tk.Epoch.Outcome != epoch.OutcomeIdle {
		t.Fatalf("warm-up tick %+v err %v", tk, err)
	}
	return e
}

func (e *fenv) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := e.db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *fenv) auditKinds(t *testing.T) []string {
	t.Helper()
	rows, err := e.db.Query(`SELECT kind FROM doomsday_audit WHERE profile=? AND generation=? ORDER BY seq`, e.prof, e.gen)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		_ = rows.Scan(&k)
		out = append(out, k)
	}
	return out
}

func (e *fenv) stopRun(t *testing.T) {
	t.Helper()
	if _, err := e.core.Advance(bg, e.prof, e.gen, store.StateStopRequested, "operator", "test"); err != nil {
		t.Fatal(err)
	}
	e.runs.set(func(r *StrategyRun) { r.Running = false })
}

func (e *fenv) tickAfter(t *testing.T, d time.Duration) HostTick {
	t.Helper()
	e.clock.Advance(d)
	tk, err := e.host.Tick(bg)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	return tk
}

func TestNewStrategyHostRequiresItsDependencies(t *testing.T) {
	if _, err := NewStrategyHost(StrategyHostDeps{}); err == nil {
		t.Fatal("an empty configuration must be refused")
	}
}

func TestHostTickWithNoRunWritesNothing(t *testing.T) {
	e := newFenv(t, nil)
	e.runs.set(func(*StrategyRun) {})
	e.runs.mu.Lock()
	e.runs.ok = false
	e.runs.mu.Unlock()
	before := e.count(t, `SELECT count(*) FROM doomsday_audit`)
	tk, err := e.host.Tick(bg)
	if err != nil || tk.Idle != "no-run" || tk.Epoch != nil {
		t.Fatalf("tick %+v err %v", tk, err)
	}
	if e.model.calls != 0 || e.count(t, `SELECT count(*) FROM doomsday_audit`) != before {
		t.Fatal("an idle tick judged or wrote")
	}
}

// The timer opens an epoch, the judge runs once, the verdicts apply, and every strategy audit line is in the store's own
// MAC chain (store.VerifyAudit accepts it).
func TestHostEpochJudgesAndChainsItsAudit(t *testing.T) {
	e := newFenv(t, nil)
	tk := e.tickAfter(t, 31*time.Minute)
	if tk.Epoch == nil || tk.Epoch.Outcome != epoch.OutcomeApplied || tk.Epoch.Trigger != types.TriggerTimer {
		t.Fatalf("%+v", tk.Epoch)
	}
	if e.model.calls != 1 {
		t.Fatalf("judge calls %d", e.model.calls)
	}
	if err := e.core.VerifyAudit(bg, e.prof, e.gen); err != nil {
		t.Fatalf("the chain does not verify: %v", err)
	}
	if again := e.tickAfter(t, time.Minute); again.Epoch.Outcome != epoch.OutcomeIdle || e.model.calls != 1 {
		t.Fatalf("a second epoch inside the floor: %+v", again.Epoch)
	}
}

func TestChainTxAppenderWritesStrategyKindsOnly(t *testing.T) {
	e := newFenv(t, nil)
	tx, err := e.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	app := NewStoreTxAppender(e.core)
	for _, k := range []string{"ops.plan.approve.approved", "finish.commit", "counter.consume", ""} {
		if err := app.AppendAuditTx(bg, tx, e.prof, e.gen, k, "x"); err == nil {
			t.Fatalf("kind %q was appended by the strategy writer", k)
		}
	}
}

func TestFinishFromBeginMapsOnlyAnAcceptedReport(t *testing.T) {
	cases := []struct {
		rep  BeginReport
		err  error
		want policy.Outcome
	}{
		{BeginReport{Accepted: true, Refused: "ignored"}, nil, policy.Outcome{Accepted: true}},
		{BeginReport{Refused: "no criteria"}, nil, policy.Outcome{Refused: "no criteria"}},
		{BeginReport{}, nil, policy.Outcome{Refused: "not accepted"}},
	}
	for _, c := range cases {
		got, err := FinishFromBegin(func(context.Context) (BeginReport, error) { return c.rep, c.err })(bg)
		if err != nil || got != c.want {
			t.Errorf("%+v: got %+v err %v, want %+v", c.rep, got, err, c.want)
		}
	}
	boom := errors.New("boom")
	if _, err := FinishFromBegin(func(context.Context) (BeginReport, error) { return BeginReport{Accepted: true}, boom })(bg); !errors.Is(err, boom) {
		t.Fatalf("an error must not become an accepted finish: %v", err)
	}
}
