//go:build doomsday

package doomsday

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/store"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/epoch"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/sstore"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

// The finish-ready reading and its marker on the real stack (rig of strategye2e_test.go).

func TestStrategyRowsAndTheReadyMarkerAreWitnessed(t *testing.T) {
	r := newStratRig(t, finishCharterJSON, nil)
	for _, title := range []string{"index the corpus first", "write the migration script", "add a smoke test"} {
		if res, err := r.propose(t, title); err != nil || !res.Accepted {
			t.Fatalf("propose %q: %+v %v", title, res, err)
		}
	}
	r.witnessAccepts(t)
	r.judge.set(types.CritPlausiblyMet)
	tk := r.tickAfter(t, 31*time.Minute)
	if tk.Epoch == nil || tk.Epoch.Outcome != epoch.OutcomeApplied || tk.Decision == nil || tk.Decision.Action != "ready" {
		t.Fatalf("tick %+v epoch %+v decision %+v", tk, tk.Epoch, tk.Decision)
	}
	if n := r.strategyRows(t); n <= 5 {
		t.Fatalf("only %d strategy rows", n)
	}
	for _, kind := range []string{types.KindProposal, types.KindNovelty, types.KindEpochBegin, types.KindJudgment, types.KindFinishReady} {
		if r.count(t, `SELECT COUNT(*) FROM doomsday_audit WHERE profile='p1' AND generation=1 AND kind=?`, kind) == 0 {
			t.Errorf("no %s row", kind)
		}
	}
	r.notFinished(t)
	r.witnessAccepts(t)
	// the status verb and the boss's reading say ready, bound to the epoch
	v, err := r.wiring().StrategyReader(r.w.DB, r.w.Witness.Keys).ReadStatus(bg, 0)
	if err != nil || v.FinishReady == nil || !v.FinishReady.Ready || v.FinishReady.Epoch != 1 || !v.FinishReady.Recorded {
		t.Fatalf("status %+v %v", v.FinishReady, err)
	}
	if rd, err := r.wiring().FinishReady(bg, r.top); err != nil || !rd.Ready {
		t.Fatalf("the boss's reading %+v %v", rd, err)
	}
}

func TestTheReadyMarkerIsWrittenOncePerEpochAcrossTicksAndRestarts(t *testing.T) {
	r := newStratRig(t, finishCharterJSON, nil)
	r.judge.set(types.CritPlausiblyMet)
	r.tickAfter(t, 31*time.Minute)
	for i := 0; i < 5; i++ {
		r.tickAfter(t, time.Minute)
	}
	h2, err := NewStrategyHost(r.deps)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := h2.Tick(bg); err != nil {
			t.Fatal(err)
		}
	}
	if r.ready(t) != 1 {
		t.Fatalf("%d ready markers for one epoch", r.ready(t))
	}
	r.notFinished(t)
	r.witnessAccepts(t)
}

type countSeq struct {
	sstore.Sequencer
	n int
}

func (c *countSeq) SequenceRows(ctx context.Context, gen int64, kind string, rows int, fn func(context.Context) error) error {
	c.n++
	return c.Sequencer.SequenceRows(ctx, gen, kind, rows, fn)
}

func TestAFailingMarkerWriteIsNotRetriedEveryTick(t *testing.T) {
	var cs *countSeq
	r := newStratRig(t, finishCharterJSON, func(d *StrategyHostDeps) {
		cs = &countSeq{Sequencer: d.Seq}
		d.Seq = cs
	})
	if _, err := r.w.DB.Exec(`CREATE TRIGGER fail_ready BEFORE INSERT ON doomsday_audit WHEN NEW.kind = 'strategy.finish.ready' BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	r.judge.set(types.CritPlausiblyMet)
	r.clock.Advance(31 * time.Minute)
	if _, err := r.host.Tick(bg); err == nil {
		t.Fatal("the failing marker write was not reported")
	}
	before := cs.n
	for i := 0; i < 20; i++ {
		r.clock.Advance(10 * time.Second)
		if tk, err := r.host.Tick(bg); err != nil {
			t.Fatalf("tick %d: %v %+v", i, err, tk)
		}
	}
	if cs.n != before {
		t.Fatalf("%d more witnessed transitions in 20 idle ticks: the failed write is being retried", cs.n-before)
	}
	r.notFinished(t)
}

func TestStrategyNeverMarksReadyWithoutAFreshAllMetJudgmentAndNeverBeginsAFinish(t *testing.T) {
	for _, c := range []struct {
		name   string
		status types.CriterionStatus
		want   int
	}{
		{"all met (control)", types.CritPlausiblyMet, 1},
		{"progressing", types.CritProgressing, 0},
		{"regressed", types.CritRegressed, 0},
		{"not started", types.CritNotStarted, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newStratRig(t, finishCharterJSON, nil)
			r.judge.set(c.status)
			r.tickAfter(t, 31*time.Minute)
			if got := r.ready(t); got != c.want {
				t.Fatalf("%d ready markers, want %d", got, c.want)
			}
			r.notFinished(t)
		})
	}
	t.Run("a charter with no criteria", func(t *testing.T) {
		r := newStratRig(t, goodCharter, nil)
		r.judge.set(types.CritPlausiblyMet)
		r.tickAfter(t, 31*time.Minute)
		if r.ready(t) != 0 {
			t.Fatal("a charter with no criteria was marked ready")
		}
		r.notFinished(t)
	})
	t.Run("an unknown subject time", func(t *testing.T) {
		r := newStratRig(t, finishCharterJSON, nil)
		r.judge.set(types.CritPlausiblyMet)
		r.deps.SubjectChangedAt = nil // an unknown subject time fails closed
		h, err := NewStrategyHost(r.deps)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = h.Tick(bg)
		r.clock.Advance(31 * time.Minute)
		if _, err := h.Tick(bg); err != nil {
			t.Fatal(err)
		}
		if r.ready(t) != 0 {
			t.Fatal("a judgment of unknown freshness was marked ready")
		}
		r.notFinished(t)
	})
}

func TestTheReadyReadingClearsOnStaleLaterBadLaterNotAllMetAndNewDirection(t *testing.T) {
	status := func(r *stratRig) FinishReadyView {
		t.Helper()
		v, err := r.wiring().StrategyReader(r.w.DB, r.w.Witness.Keys).ReadStatus(bg, 0)
		if err != nil || v.FinishReady == nil {
			t.Fatalf("%+v %v", v, err)
		}
		return *v.FinishReady
	}
	readyRig := func() *stratRig {
		r := newStratRig(t, finishCharterJSON, nil)
		r.judge.set(types.CritPlausiblyMet)
		r.tickAfter(t, 31*time.Minute)
		if v := status(r); !v.Ready {
			t.Fatalf("the control is not ready: %+v", v)
		}
		return r
	}
	t.Run("a later bad judgment", func(t *testing.T) {
		r := readyRig()
		r.judge.garbage = `{"criteria":[{"id":"c9","status":"plausibly-met"}],"directions":[],"next_actions":[]}`
		r.tickAfter(t, 31*time.Minute)
		if v := status(r); v.Ready || v.Reason != "latest-epoch-is-a-bad-judgment" {
			t.Fatalf("%+v", v)
		}
		if b, err := r.wiring().FinishReady(bg, r.top); err != nil || b.Ready {
			t.Fatalf("the boss still reads ready: %+v %v", b, err)
		}
	})
	t.Run("a later reading that is not all met", func(t *testing.T) {
		r := readyRig()
		r.judge.set(types.CritRegressed)
		r.tickAfter(t, 31*time.Minute)
		if v := status(r); v.Ready || v.Reason != "not-every-criterion-plausibly-met" {
			t.Fatalf("%+v", v)
		}
	})
	t.Run("a stale judgment", func(t *testing.T) {
		r := readyRig()
		r.clock.Advance(2*types.DefaultEpochInterval + time.Minute)
		// the reader's clock is the real one; the host's is the fake: read through the host's calculator
		v, err := r.host.ready().view(bg, 1)
		if err != nil || v.Ready || v.Reason != "judgment-stale" {
			t.Fatalf("%+v %v", v, err)
		}
	})
	t.Run("a direction proposed after the judgment", func(t *testing.T) {
		r := readyRig()
		if res, err := r.propose(t, "something nobody judged"); err != nil || !res.Accepted {
			t.Fatalf("%+v %v", res, err)
		}
		if v := status(r); v.Ready || v.Reason != "open-direction-not-in-the-judgment" {
			t.Fatalf("%+v", v)
		}
	})
}

func TestTheStrategyHostNeverCallsAnyFinishBegin(t *testing.T) {
	files, err := filepath.Glob("strategy*.go")
	if err != nil || len(files) == 0 {
		t.Fatal(files, err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		fs := token.NewFileSet()
		af, err := parser.ParseFile(fs, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(af, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok {
				switch id.Name {
				case "FinishBegin", "BeginFinish", "FinishEngine", "BeginReport", "FinishFromBegin":
					t.Errorf("%s: %s names %s", f, fs.Position(id.Pos()), id.Name)
				}
			}
			return true
		})
	}
	for _, v := range []any{StrategyHostDeps{}, StrategyExtras{}} {
		rt := reflect.TypeOf(v)
		for i := 0; i < rt.NumField(); i++ {
			ft := rt.Field(i).Type
			if ft.Kind() != reflect.Func {
				continue
			}
			for j := 0; j < ft.NumOut(); j++ {
				if ft.Out(j) == reflect.TypeOf(BeginReport{}) {
					t.Errorf("%s.%s can carry a finish begin", rt.Name(), rt.Field(i).Name)
				}
			}
		}
	}

	r := newStratRig(t, finishCharterJSON, nil)
	r.judge.set(types.CritPlausiblyMet)
	for i := 0; i < 8; i++ {
		r.tickAfter(t, 31*time.Minute)
	}
	h2, err := NewStrategyHost(r.deps)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := h2.Tick(bg); err != nil {
			t.Fatal(err)
		}
	}
	if r.ready(t) == 0 {
		t.Fatal("the positive control did not reach a ready marker")
	}
	r.notFinished(t)
	// the boss's own call is what begins one
	rep, err := r.fe.FinishBegin(bg)
	if err != nil || !rep.Accepted {
		t.Fatalf("the boss's finish: %+v %v", rep, err)
	}
}

func TestStrategyGivesUpOnceAfterFiveParkedRoundsAndARestartDoesNotRepeatIt(t *testing.T) {
	r := newStratRig(t, finishCharterJSON, nil)
	// an open person item makes every begin attempt of the boss a refusal that is recorded
	if _, err := r.w.DB.Exec(`INSERT INTO coder_session_questions (id, profile_id, conversation_id, class, status, created_at) VALUES (1,'p1','c','risky_action','open','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.w.DB.Exec(`INSERT INTO coder_session_person_items (profile_id, conversation_id, kind, ref_id, created_at) VALUES ('p1','c','parked_question',1,'2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	r.judge.set(types.CritPlausiblyMet)
	boss := 0
	for i := 0; i < 8; i++ {
		rep, err := r.fe.FinishBegin(bg) // the boss asks and is refused
		if err != nil || rep.Accepted {
			t.Fatalf("the boss's attempt %d: %+v %v", i, rep, err)
		}
		boss++
		r.tickAfter(t, 31*time.Minute)
	}
	if got := r.count(t, `SELECT COUNT(*) FROM doomsday_audit WHERE profile='p1' AND generation=1 AND kind=?`, KindFinishDenied); got != boss {
		t.Fatalf("%d refusals recorded for %d boss attempts: the strategy layer asked the engine too", got, boss)
	}
	if r.count(t, `SELECT COUNT(*) FROM doomsday_strategy_epochs WHERE trigger = 'finish-parked'`) != types.MaxParkedFinishRounds {
		t.Fatalf("parked-round epochs: %d", r.count(t, `SELECT COUNT(*) FROM doomsday_strategy_epochs WHERE trigger = 'finish-parked'`))
	}
	gaveUp := func() int {
		return r.count(t, `SELECT COUNT(*) FROM doomsday_audit WHERE profile='p1' AND generation=1 AND kind=?`, types.KindFinishGaveUp)
	}
	if gaveUp() != 1 {
		t.Fatalf("the operator was told %d times", gaveUp())
	}
	if run, _ := r.w.Store.GetRun(bg, "p1", 1); run.State != store.StateRunning {
		t.Fatalf("the run is %s", run.State)
	}
	if r.judge.n() < 8 { // epochs go on: the layer keeps judging while it no longer marks
		t.Fatalf("judge calls %d", r.judge.n())
	}
	v, err := r.wiring().StrategyReader(r.w.DB, r.w.Witness.Keys).ReadStatus(bg, 0)
	if err != nil || v.ParkedFinish != types.MaxParkedFinishRounds || v.FinishReady == nil || v.FinishReady.Ready || !v.FinishReady.GaveUp {
		t.Fatalf("%+v %v", v, err)
	}
	// a restart: the rounds and the give-up come from the tables, so nothing is told again and nothing is marked
	marks := r.ready(t)
	h2, err := NewStrategyHost(r.deps)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		r.clock.Advance(31 * time.Minute)
		if _, err := h2.Tick(bg); err != nil {
			t.Fatal(err)
		}
	}
	if gaveUp() != 1 || r.ready(t) != marks {
		t.Fatalf("after a restart: told %d times, markers %d -> %d", gaveUp(), marks, r.ready(t))
	}
	r.witnessAccepts(t)
}
