package epoch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/judge"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/sstore"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

func TestTimerRespectsInterval(t *testing.T) {
	h := newHarness(t, nil)
	if r := h.tick(); r.Outcome != OutcomeIdle {
		t.Fatalf("at start: %+v", r)
	}
	h.clk.Advance(29 * time.Minute)
	if r := h.tick(); r.Outcome != OutcomeIdle || h.jd.count() != 0 {
		t.Fatalf("before the interval: %+v", r)
	}
	h.clk.Advance(time.Minute)
	r := h.tick()
	if r.Outcome != OutcomeApplied || r.Trigger != types.TriggerTimer || r.EpochSeq != 1 || h.jd.count() != 1 {
		t.Fatalf("at the interval: %+v calls=%d", r, h.jd.count())
	}
	if h.st.epochs[1].State != types.EpochApplied || h.st.epochs[1].ModelPin != "fake/judge/1" {
		t.Fatalf("stored: %+v", h.st.epochs[1])
	}
}

func TestConfigClampedToCodeBounds(t *testing.T) {
	h := newHarness(t, func(c *Config) {
		c.Interval, c.StallWindow, c.MaxEpochsPerHour = time.Second, 1000*time.Hour, 100
	})
	if h.eng.cfg.Interval != types.MinEpochInterval || h.eng.cfg.StallWindow != MaxStallWindow || h.eng.cfg.MaxEpochsPerHour != MaxEpochsPerHour {
		t.Fatalf("not clamped: %+v", h.eng.cfg)
	}
	d := newHarness(t, nil)
	if d.eng.cfg.Interval != types.DefaultEpochInterval || d.eng.cfg.StallWindow != DefaultStallWindow {
		t.Fatalf("defaults: %+v", d.eng.cfg)
	}
}

func TestFloorHoldsEveryTrigger(t *testing.T) {
	h := newHarness(t, nil)
	h.clk.Advance(30 * time.Minute)
	if r := h.tick(); r.Outcome != OutcomeApplied {
		t.Fatalf("%+v", r)
	}
	h.in.set(func(s *Snapshot) { s.EvidenceSeq = 5 })
	h.clk.Advance(time.Minute)
	r := h.tick()
	if r.Outcome != OutcomeIdle || r.Held != HeldFloor || r.Trigger != types.TriggerNewEvidence || h.jd.count() != 1 {
		t.Fatalf("within the floor: %+v", r)
	}
	h.clk.Advance(4 * time.Minute)
	if r := h.tick(); r.Outcome != OutcomeApplied || r.Trigger != types.TriggerNewEvidence || r.EpochSeq != 2 {
		t.Fatalf("after the floor: %+v", r)
	}
}

func TestStalledDirectionTriggersWithinOneTick(t *testing.T) {
	h := newHarness(t, nil)
	h.st.addDir("d1", types.DirActive)
	h.clk.Advance(10 * time.Minute)
	h.in.set(func(s *Snapshot) {
		s.Activity = []DirectionActivity{{ID: "d1", LastProgress: h.clk.Now().Add(-61 * time.Minute)}}
	})
	r := h.tick()
	if r.Trigger != types.TriggerDirectionStalled || r.Outcome != OutcomeApplied {
		t.Fatalf("%+v", r)
	}
	if h.st.state("d1") != types.DirStalled || len(r.Observed) != 1 {
		t.Fatalf("direction %s observed %+v", h.st.state("d1"), r.Observed)
	}
}

func TestStallWindowIsBounded(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.StallWindow = 1000 * time.Hour })
	h.st.addDir("d1", types.DirActive)
	h.clk.Advance(25 * time.Hour)
	h.in.set(func(s *Snapshot) {
		s.Activity = []DirectionActivity{{ID: "d1", LastProgress: h.clk.Now().Add(-25 * time.Hour)}}
	})
	if r := h.tick(); r.Trigger != types.TriggerDirectionStalled {
		t.Fatalf("a window above the cap must read as the cap: %+v", r)
	}
}

func TestUnknownProgressIsNeverStalled(t *testing.T) {
	h := newHarness(t, nil)
	h.st.addDir("d1", types.DirActive)
	h.clk.Advance(10 * time.Minute)
	h.in.set(func(s *Snapshot) { s.Activity = []DirectionActivity{{ID: "d1"}} })
	if r := h.tick(); r.Outcome != OutcomeIdle {
		t.Fatalf("%+v", r)
	}
}

func TestDirectionFinishedTrigger(t *testing.T) {
	h := newHarness(t, nil)
	h.st.addDir("d1", types.DirActive)
	h.clk.Advance(6 * time.Minute)
	h.in.set(func(s *Snapshot) { s.Activity = []DirectionActivity{{ID: "d1", Finished: true}} })
	r := h.tick()
	if r.Trigger != types.TriggerDirectionFinished || h.st.state("d1") != types.DirFinished {
		t.Fatalf("%+v state=%s", r, h.st.state("d1"))
	}
}

func TestBudgetShareFiresOncePerCrossing(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.BudgetShareThreshold = 0.8 })
	h.clk.Advance(6 * time.Minute)
	h.in.set(func(s *Snapshot) { s.Budget = BudgetShare{Known: false, Share: 0.99} })
	if r := h.tick(); r.Outcome != OutcomeIdle {
		t.Fatalf("unknown share must never fire: %+v", r)
	}
	h.in.set(func(s *Snapshot) { s.Budget = BudgetShare{Known: true, Share: 0.85} })
	if r := h.tick(); r.Trigger != types.TriggerBudgetShare || r.Outcome != OutcomeApplied {
		t.Fatalf("%+v", r)
	}
	h.clk.Advance(6 * time.Minute)
	if r := h.tick(); r.Outcome != OutcomeIdle {
		t.Fatalf("still above, same crossing: %+v", r)
	}
	h.in.set(func(s *Snapshot) { s.Budget.Share = 0.1 })
	h.tick()
	h.in.set(func(s *Snapshot) { s.Budget.Share = 0.9 })
	if r := h.tick(); r.Trigger != types.TriggerBudgetShare {
		t.Fatalf("new crossing: %+v", r)
	}
}

func TestNewEvidenceIsEdgeTriggered(t *testing.T) {
	h := newHarness(t, nil)
	h.in.set(func(s *Snapshot) { s.EvidenceSeq = 7 })
	h.clk.Advance(6 * time.Minute)
	if r := h.tick(); r.Outcome != OutcomeIdle {
		t.Fatalf("evidence that predates the engine is not news: %+v", r)
	}
	h.in.set(func(s *Snapshot) { s.EvidenceSeq = 8 })
	if r := h.tick(); r.Trigger != types.TriggerNewEvidence {
		t.Fatalf("%+v", r)
	}
	h.clk.Advance(6 * time.Minute)
	if r := h.tick(); r.Outcome != OutcomeIdle {
		t.Fatalf("consumed: %+v", r)
	}
}

func TestFinishParkedTriggerIsBoundedAndRestartSafe(t *testing.T) {
	h := newHarness(t, nil)
	h.clk.Advance(6 * time.Minute)
	h.in.set(func(s *Snapshot) { s.FinishParks = 1 })
	if r := h.tick(); r.Trigger != types.TriggerFinishParked {
		t.Fatalf("%+v", r)
	}
	h.clk.Advance(6 * time.Minute)
	h.eng = h.newEngine() // restart: the count comes from the store
	if r := h.tick(); r.Outcome != OutcomeIdle {
		t.Fatalf("one park, one epoch: %+v", r)
	}
	for i := 2; i <= types.MaxParkedFinishRounds+3; i++ {
		h.in.set(func(s *Snapshot) { s.FinishParks = i })
		h.clk.Advance(6 * time.Minute)
		if r := h.tick(); r.Trigger == types.TriggerFinishParked {
			continue
		}
	}
	n, _ := h.st.ParkedFinishRounds(context.Background(), "p", 1)
	if n != types.MaxParkedFinishRounds {
		t.Fatalf("parked rounds %d, want the cap %d", n, types.MaxParkedFinishRounds)
	}
}

func TestOpenEpochIsResumedNotDuplicated(t *testing.T) {
	h := newHarness(t, nil)
	pin, _ := h.jd.CheckPin()
	h.st.epochs[1] = types.Epoch{Profile: "p", Generation: 1, Seq: 1, Trigger: types.TriggerTimer, StartedAt: h.clk.Now().Unix(), ModelPin: pin, State: types.EpochOpen}
	h.clk.Advance(2 * time.Hour) // a timer would also be due
	r := h.tick()
	if !r.Resumed || r.EpochSeq != 1 || r.Outcome != OutcomeApplied {
		t.Fatalf("%+v", r)
	}
	if h.st.inserts != 0 || len(h.st.epochs) != 1 || h.jd.count() != 1 {
		t.Fatalf("inserts=%d epochs=%d judge=%d", h.st.inserts, len(h.st.epochs), h.jd.count())
	}
}

func TestOnlyOneEpochAtATimeUnderConcurrentTicks(t *testing.T) {
	h := newHarness(t, nil)
	h.clk.Advance(time.Hour)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = h.eng.Tick(context.Background()) }()
	}
	wg.Wait()
	if len(h.st.epochs) != 1 || h.jd.count() != 1 {
		t.Fatalf("epochs=%d judge=%d", len(h.st.epochs), h.jd.count())
	}
}

func TestBadJudgmentChangesNothingElse(t *testing.T) {
	for name, cause := range map[string]error{
		"invalid": fmt.Errorf("%w: x", types.ErrBadJudgment), "model-failed": fmt.Errorf("%w: x", judge.ErrModelFailed), "input": judge.ErrBadInput,
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, nil)
			h.st.addDir("d1", types.DirProposed)
			h.st.addDir("d2", types.DirActive)
			h.jd.fn = func(_ context.Context, in judge.Input) (judge.Result, error) {
				// a misbehaving judge hands back a harmful verdict set AND an error: only the error may count
				return resultFor(in, map[string]types.Verdict{"d1": types.VerdictPrune, "d2": types.VerdictPrune}), cause
			}
			h.clk.Advance(30 * time.Minute)
			r := h.tick()
			if r.Outcome != OutcomeBadJudgment || r.BadReason != name || len(r.Moves) != 0 || r.Judgment != nil {
				t.Fatalf("%+v", r)
			}
			if h.st.state("d1") != types.DirProposed || h.st.state("d2") != types.DirActive {
				t.Fatalf("directions moved: %s %s", h.st.state("d1"), h.st.state("d2"))
			}
			if h.st.epochs[1].State != types.EpochBadJudgment || h.st.recorded != 0 {
				t.Fatalf("epoch %+v recorded=%d", h.st.epochs[1], h.st.recorded)
			}
			h.jd.fn = nil
			h.clk.Advance(30 * time.Minute)
			if r := h.tick(); r.Outcome != OutcomeApplied || r.EpochSeq != 2 {
				t.Fatalf("a bad epoch must not block the next: %+v", r)
			}
		})
	}
}

func TestVerdictsMoveDirections(t *testing.T) {
	h := newHarness(t, nil)
	h.st.addDir("keep", types.DirActive)
	h.st.addDir("prune", types.DirActive)
	h.st.addDir("boost", types.DirProposed)
	h.jd.fn = func(_ context.Context, in judge.Input) (judge.Result, error) {
		return resultFor(in, map[string]types.Verdict{"keep": types.VerdictKeep, "prune": types.VerdictPrune, "boost": types.VerdictBoost}), nil
	}
	h.clk.Advance(30 * time.Minute)
	r := h.tick()
	if r.Outcome != OutcomeApplied || len(r.Moves) != 2 {
		t.Fatalf("%+v", r)
	}
	if h.st.state("keep") != types.DirActive || h.st.state("prune") != types.DirPruned || h.st.state("boost") != types.DirActive {
		t.Fatalf("%s %s %s", h.st.state("keep"), h.st.state("prune"), h.st.state("boost"))
	}
	if r.Judgment == nil {
		t.Fatal("the result carries the judgment for the audit layer")
	}
}

func TestAllMetIsReportedNeverActedOn(t *testing.T) {
	h := newHarness(t, nil)
	h.jd.fn = func(_ context.Context, in judge.Input) (judge.Result, error) {
		r := resultFor(in, nil)
		for i := range r.Judgment.Criteria {
			r.Judgment.Criteria[i].Status = types.CritPlausiblyMet
		}
		r.Body, _ = marshal(r.Judgment)
		return r, nil
	}
	h.clk.Advance(30 * time.Minute)
	if r := h.tick(); !r.AllPlausiblyMet {
		t.Fatalf("%+v", r)
	}
	// no criteria: never "met"
	h2 := newHarness(t, nil)
	h2.in.set(func(s *Snapshot) { s.Criteria = nil })
	h2.clk.Advance(30 * time.Minute)
	if r := h2.tick(); r.AllPlausiblyMet {
		t.Fatalf("a run with no criteria must never read as met: %+v", r)
	}
}

func TestNothingNewAfterStop(t *testing.T) {
	h := newHarness(t, nil)
	h.run.set(false)
	h.in.set(func(s *Snapshot) { s.EvidenceSeq = 9; s.FinishParks = 3 })
	h.clk.Advance(48 * time.Hour)
	r := h.tick()
	if r.Outcome != OutcomeInert || h.st.inserts != 0 || h.jd.count() != 0 || len(h.st.epochs) != 0 {
		t.Fatalf("%+v inserts=%d judge=%d", r, h.st.inserts, h.jd.count())
	}
}

func TestStopAbandonsOpenAndJudgedEpochs(t *testing.T) {
	for _, st := range []types.EpochState{types.EpochOpen, types.EpochJudged} {
		t.Run(string(st), func(t *testing.T) {
			h := newHarness(t, nil)
			h.st.epochs[1] = types.Epoch{Profile: "p", Generation: 1, Seq: 1, Trigger: types.TriggerTimer, StartedAt: 1, ModelPin: "x", State: st}
			h.run.set(false)
			r := h.tick()
			if !r.Abandoned || h.st.epochs[1].State != types.EpochAbandoned || h.jd.count() != 0 || h.st.inserts != 0 {
				t.Fatalf("%+v %+v", r, h.st.epochs[1])
			}
		})
	}
}

func TestStopDuringJudgeWritesNothing(t *testing.T) {
	h := newHarness(t, nil)
	h.st.addDir("d1", types.DirActive)
	h.jd.fn = func(_ context.Context, in judge.Input) (judge.Result, error) {
		h.run.set(false) // Stop lands while the model is thinking
		return resultFor(in, map[string]types.Verdict{"d1": types.VerdictPrune}), nil
	}
	h.clk.Advance(30 * time.Minute)
	r := h.tick()
	if !r.Abandoned || h.st.recorded != 0 || h.st.state("d1") != types.DirActive || h.st.epochs[1].State != types.EpochAbandoned {
		t.Fatalf("%+v %+v recorded=%d", r, h.st.epochs[1], h.st.recorded)
	}
}

func TestRunStateErrorDoesNothing(t *testing.T) {
	h := newHarness(t, nil)
	h.run.err = errBoom
	h.clk.Advance(time.Hour)
	if r, err := h.eng.Tick(context.Background()); err == nil || r.Held != HeldRunUnknown || h.st.inserts != 0 {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestRestartAfterJudgmentDoesNotJudgeAgain(t *testing.T) {
	h := newHarness(t, nil)
	h.st.addDir("d1", types.DirActive)
	h.jd.fn = func(_ context.Context, in judge.Input) (judge.Result, error) {
		return resultFor(in, map[string]types.Verdict{"d1": types.VerdictPrune}), nil
	}
	h.st.moveDirFn = func(string, types.DirectionState) error { return errBoom } // host dies while applying
	h.clk.Advance(30 * time.Minute)
	if _, err := h.eng.Tick(context.Background()); err == nil {
		t.Fatal("want the injected failure")
	}
	if h.st.epochs[1].State != types.EpochJudged || h.jd.count() != 1 {
		t.Fatalf("%+v judge=%d", h.st.epochs[1], h.jd.count())
	}
	h.st.moveDirFn = nil
	h.eng = h.newEngine() // restart
	r := h.tick()
	if !r.Resumed || r.Outcome != OutcomeApplied || h.st.state("d1") != types.DirPruned {
		t.Fatalf("%+v", r)
	}
	if h.jd.count() != 1 || len(h.st.epochs) != 1 || h.st.inserts != 1 {
		t.Fatalf("judge=%d epochs=%d inserts=%d", h.jd.count(), len(h.st.epochs), h.st.inserts)
	}
}

func TestRestartBeforeJudgmentResumesSameEpoch(t *testing.T) {
	h := newHarness(t, nil)
	h.jd.fn = func(ctx context.Context, in judge.Input) (judge.Result, error) { return judge.Result{}, ctx.Err() }
	h.clk.Advance(30 * time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.eng.Tick(ctx); err == nil {
		t.Fatal("a cancelled tick reports the cancellation")
	}
	if h.st.epochs[1].State != types.EpochOpen {
		t.Fatalf("a cancelled call leaves the epoch open: %+v", h.st.epochs[1])
	}
	h.jd.fn = nil
	h.eng = h.newEngine()
	r := h.tick()
	if !r.Resumed || r.EpochSeq != 1 || r.Outcome != OutcomeApplied || h.st.inserts != 1 {
		t.Fatalf("%+v inserts=%d", r, h.st.inserts)
	}
}

func TestHourlyCap(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.MaxEpochsPerHour = 2 })
	h.clk.Advance(6 * time.Minute)
	for i := int64(1); i <= 3; i++ { // the first value is only the baseline
		h.in.set(func(s *Snapshot) { s.EvidenceSeq = i })
		if r := h.tick(); r.Outcome != OutcomeApplied && i > 1 {
			t.Fatalf("%d: %+v", i, r)
		}
		h.clk.Advance(6 * time.Minute)
	}
	h.in.set(func(s *Snapshot) { s.EvidenceSeq = 4 })
	if r := h.tick(); r.Held != HeldHourlyCap {
		t.Fatalf("%+v epochs=%d", r, len(h.st.epochs))
	}
}

func TestSnapshotBounds(t *testing.T) {
	h := newHarness(t, nil)
	h.in.set(func(s *Snapshot) { s.Criteria = make([]judge.CriterionInput, types.MaxCriteria+1) })
	h.clk.Advance(time.Hour)
	if _, err := h.eng.Tick(context.Background()); !errors.Is(err, ErrInputTooLarge) || h.st.inserts != 0 {
		t.Fatalf("%v inserts=%d", err, h.st.inserts)
	}
}

func TestJudgePinRefusalOpensNoEpoch(t *testing.T) {
	h := newHarness(t, nil)
	h.jd.pinErr = judge.ErrNotIndependent
	h.clk.Advance(time.Hour)
	if r, err := h.eng.Tick(context.Background()); !errors.Is(err, judge.ErrNotIndependent) || r.Held != HeldJudgePin || h.st.inserts != 0 {
		t.Fatalf("%+v %v", r, err)
	}
	if r := h.tick(); r.Held != HeldBackoff {
		t.Fatalf("a refused pin backs off: %+v", r)
	}
}

func TestTamperedRowParksStrategyNotRun(t *testing.T) {
	h := newHarness(t, nil)
	h.st.listErr = sstore.ErrTampered
	h.clk.Advance(time.Hour)
	if _, err := h.eng.Tick(context.Background()); !errors.Is(err, sstore.ErrTampered) {
		t.Fatalf("%v", err)
	}
	h.st.listErr = nil
	if r := h.tick(); r.Outcome != OutcomeInert || r.Held != HeldParked || h.jd.count() != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestNewRefusesBadConfig(t *testing.T) {
	h := newHarness(t, nil)
	for name, f := range map[string]func(*Config, *Deps){
		"profile":   func(c *Config, _ *Deps) { c.Profile = "" },
		"gen":       func(c *Config, _ *Deps) { c.Generation = 0 },
		"threshold": func(c *Config, _ *Deps) { c.BudgetShareThreshold = 1.5 },
		"clock":     func(_ *Config, d *Deps) { d.Clock = nil },
		"judge":     func(_ *Config, d *Deps) { d.Judge = nil },
	} {
		c, d := h.cfg, h.deps
		f(&c, &d)
		if _, err := New(c, d); !errors.Is(err, ErrBadConfig) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestNoGoroutinesNoInitNoForbiddenImports(t *testing.T) {
	ents, _ := os.ReadDir(".")
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, _ := os.ReadFile(e.Name())
		s := string(b)
		for _, bad := range []string{"go func", "\ngo ", "func init(", "/vault", "finish/subject", "/store\""} {
			if strings.Contains(s, bad) {
				t.Errorf("%s contains %q", e.Name(), bad)
			}
		}
	}
}
