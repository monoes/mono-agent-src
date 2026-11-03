//go:build doomsday

package doomsday

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/ops"
	"github.com/monoes/mono-agent/internal/doomsday/stop"
	"github.com/monoes/mono-agent/internal/doomsday/store"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/epoch"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/judge"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/novelty"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
	"github.com/monoes/mono-agent/internal/monomind"
)

// The strategy layer on the real stack: the real SQLite store, the real witness (sequencer, pinned connection, anchor in a
// file secret store), the real finish engine, the boss registry of the database, and the ops package's enforcement. Fakes
// are the model (monomind.Exec), the clock, and the kernel/service manager of wire_test.go.

// fakeJudge answers the judging turns of the runner.
type fakeJudge struct {
	mu      sync.Mutex
	status  types.CriterionStatus
	garbage string // when set, the raw text the model answers instead of a judgment
	calls   int
}

func (f *fakeJudge) set(s types.CriterionStatus) { f.mu.Lock(); f.status = s; f.mu.Unlock() }
func (f *fakeJudge) n() int                      { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

func (f *fakeJudge) exec(_ context.Context, opts monomind.ExecOptions, _ func(monomind.Event)) (*monomind.TurnResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.garbage != "" {
		return &monomind.TurnResult{SawDone: true, ResultText: f.garbage}, nil
	}
	var doc struct {
		Criteria   []struct{ ID string } `json:"criteria"`
		Directions []struct{ ID string } `json:"directions"`
	}
	if err := json.Unmarshal([]byte(opts.Prompt), &doc); err != nil {
		return nil, err
	}
	j := types.Judgment{Criteria: []types.CriterionProgress{}, Directions: []types.DirectionAssessment{}, NextActions: []types.NextAction{}}
	for _, c := range doc.Criteria {
		j.Criteria = append(j.Criteria, types.CriterionProgress{ID: c.ID, Status: f.status})
	}
	for _, d := range doc.Directions {
		j.Directions = append(j.Directions, types.DirectionAssessment{ID: d.ID, Verdict: types.VerdictKeep})
	}
	b, _ := json.Marshal(j)
	return &monomind.TurnResult{SawDone: true, ResultText: string(b)}, nil
}

type stratRig struct {
	*finRig
	top    int64
	clock  *epoch.FakeClock
	judge  *fakeJudge
	host   *StrategyHost
	deps   StrategyHostDeps
	begins int
}

func noHits(context.Context, string) ([]novelty.Hit, error) { return nil, nil }

// newStratRig starts a run with a boss on the real stack and builds the strategy host over it. The first tick builds the
// epoch engine, whose timer counts from then.
func newStratRig(t *testing.T, charter string, mut func(*StrategyHostDeps)) *stratRig {
	t.Helper()
	fr := newFinRig(t, charter)
	r := &stratRig{finRig: fr, clock: epoch.NewFakeClock(time.Unix(1_800_000_000, 0)), judge: &fakeJudge{status: types.CritProgressing}}
	r.top = fr.topLead(t, "conv-boss")
	fr.start(t)
	if err := fr.boss(t, r.top); err != nil {
		t.Fatal(err)
	}
	d, err := fr.w.StrategyHostDeps(bg, StrategyExtras{JudgeExec: r.judge.exec, Clock: r.clock, Similar: noHits,
		Finish: func(ctx context.Context) (BeginReport, error) { r.begins++; return fr.fe.FinishBegin(ctx) }})
	if err != nil {
		t.Fatal(err)
	}
	if mut != nil {
		mut(&d)
	}
	r.deps = d
	if r.host, err = NewStrategyHost(d); err != nil {
		t.Fatal(err)
	}
	if tk, err := r.host.Tick(bg); err != nil || tk.Epoch == nil || tk.Epoch.Outcome != epoch.OutcomeIdle {
		t.Fatalf("warm-up tick %+v err %v", tk, err)
	}
	return r
}

func (r *stratRig) tickAfter(t *testing.T, d time.Duration) HostTick {
	t.Helper()
	r.clock.Advance(d)
	tk, err := r.host.Tick(bg)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	return tk
}

func (r *stratRig) propose(t *testing.T, title string) (ops.StrategyResult, error) {
	t.Helper()
	args, _ := json.Marshal(ops.ProposeArgs{Title: title, Rationale: "because", Criteria: []string{"c0"}})
	return r.w.BossStrategyWith(bg, StrategyExtras{Similar: noHits}, r.top, ops.OpStrategyPropose, args)
}

func (r *stratRig) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := r.w.DB.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (r *stratRig) strategyRows(t *testing.T) int {
	return r.count(t, `SELECT COUNT(*) FROM doomsday_audit WHERE profile = 'p1' AND generation = 1 AND kind LIKE 'strategy.%'`)
}

// witnessAccepts is the whole of "the witness accepts the chain": the chain verifies, the freshness check of the
// generation passes, and every pending line has its committed line, the last of which names the database's head.
func (r *stratRig) witnessAccepts(t *testing.T) {
	t.Helper()
	if err := r.w.Store.VerifyAudit(bg, "p1", 1); err != nil {
		t.Fatalf("the chain does not verify: %v", err)
	}
	seq, err := r.w.Sequencer(bg)
	if err != nil {
		t.Fatal(err)
	}
	if err := seq.Check(bg, 1); err != nil {
		t.Fatalf("the witness does not accept the generation: %v", err)
	}
	recs, err := witnessOf(r.wireRig).Records()
	if err != nil {
		t.Fatal(err)
	}
	pend, comm, lastTo := 0, 0, int64(0)
	for _, x := range recs {
		switch x.T {
		case stop.RecPending:
			pend++
		case stop.RecCommitted:
			comm++
			lastTo = x.To
		}
	}
	head, err := stop.ReadDBHead(bg, r.w.DB, "p1", 1)
	if err != nil || pend != comm || lastTo != head.Seq {
		t.Fatalf("pending %d committed %d, last committed to %d, database head %d (%v)", pend, comm, lastTo, head.Seq, err)
	}
}

// More than five strategy rows and a finish request on a live witness: the chain verifies, the witness accepts every
// transition, and a writer that bypassed the witness (the mutant: the strategy store run without its sequencer) is caught
// at the next admission as row 5. A proposal is two rows in one transition, so the pending range covers both.
func TestStrategyRowsAndAFinishRequestAreWitnessed(t *testing.T) {
	r := newStratRig(t, finishCharterJSON, nil)
	for _, title := range []string{"index the corpus first", "write the migration script", "add a smoke test"} {
		if res, err := r.propose(t, title); err != nil || !res.Accepted {
			t.Fatalf("propose %q: %+v %v", title, res, err)
		}
	}
	r.witnessAccepts(t)
	r.judge.set(types.CritPlausiblyMet)
	tk := r.tickAfter(t, 31*time.Minute)
	if tk.Epoch == nil || tk.Epoch.Outcome != epoch.OutcomeApplied || tk.Decision == nil || tk.Decision.Action != "request" || tk.Finish == nil || !tk.Finish.Accepted {
		t.Fatalf("tick %+v epoch %+v decision %+v finish %+v", tk, tk.Epoch, tk.Decision, tk.Finish)
	}
	if n := r.strategyRows(t); n <= 5 {
		t.Fatalf("only %d strategy rows", n)
	}
	for _, kind := range []string{types.KindProposal, types.KindNovelty, types.KindEpochBegin, types.KindJudgment, types.KindFinishRequested} {
		if r.count(t, `SELECT COUNT(*) FROM doomsday_audit WHERE profile='p1' AND generation=1 AND kind=?`, kind) == 0 {
			t.Errorf("no %s row", kind)
		}
	}
	if run, _ := r.w.Store.GetRun(bg, "p1", 1); run.State != store.StateQuiescent || run.Cause != store.FinishCause {
		t.Fatalf("the finish was not begun: %+v", run)
	}
	r.witnessAccepts(t)
	if r.count(t, `SELECT COUNT(*) FROM doomsday_audit WHERE profile='p1' AND generation=1 AND kind LIKE 'finish.%'`) != 0 && r.begins != 1 {
		t.Fatalf("finish begins = %d", r.begins)
	}
}

// A chained row written outside the witnessed sequence is the unwitnessed writer: the next admission fails closed at row 5.
// This pins that the test above can tell the difference.
func TestAnUnwitnessedStrategyRowFailsTheGenerationClosed(t *testing.T) {
	r := newStratRig(t, finishCharterJSON, nil)
	tx, err := r.w.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.w.Store.AppendStrategyAuditTx(bg, tx, "p1", 1, types.KindProposal, "{}"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	seq, _ := r.w.Sequencer(bg)
	var fe *stop.FreshnessError
	if err := seq.Check(bg, 1); !errors.As(err, &fe) || fe.Row != 5 {
		t.Fatalf("want a row 5 failure, got %v", err)
	}
}

// A2: the timer opens an epoch, the judge answers, the verdicts apply. A judgment that fails the schema is a recorded
// bad-judgment and changes nothing: no direction moves, no finish is requested, and the judge is not called again inside the
// floor.
func TestStrategyEpochAndABadJudgmentChangesNothing(t *testing.T) {
	r := newStratRig(t, finishCharterJSON, nil)
	if res, err := r.propose(t, "first direction"); err != nil || !res.Accepted {
		t.Fatalf("%+v %v", res, err)
	}
	r.judge.garbage = `{"criteria":[{"id":"c9","status":"plausibly-met"}],"directions":[],"next_actions":[]}`
	before := r.count(t, `SELECT COUNT(*) FROM doomsday_strategy_directions WHERE state = 'proposed'`)
	tk := r.tickAfter(t, 31*time.Minute)
	if tk.Epoch == nil || tk.Epoch.Outcome == epoch.OutcomeApplied {
		t.Fatalf("a garbage judgment was applied: %+v", tk.Epoch)
	}
	if r.count(t, `SELECT COUNT(*) FROM doomsday_strategy_epochs WHERE state = 'bad-judgment'`) != 1 ||
		r.count(t, `SELECT COUNT(*) FROM doomsday_strategy_judgments`) != 0 {
		t.Fatal("the bad judgment was not recorded as a bad-judgment epoch without a stored judgment")
	}
	if r.count(t, `SELECT COUNT(*) FROM doomsday_strategy_directions WHERE state = 'proposed'`) != before || r.begins != 0 {
		t.Fatal("a bad judgment changed a direction or asked for a finish")
	}
	if again := r.tickAfter(t, time.Minute); again.Epoch.Outcome != epoch.OutcomeIdle || r.judge.n() != 1 {
		t.Fatalf("a second judging inside the floor: %+v, calls %d", again.Epoch, r.judge.n())
	}
	r.witnessAccepts(t)
	// and a valid judgment afterwards works: the layer is not parked by a bad one
	r.judge.garbage = ""
	r.judge.set(types.CritProgressing)
	if tk := r.tickAfter(t, 31*time.Minute); tk.Epoch.Outcome != epoch.OutcomeApplied {
		t.Fatalf("the layer did not recover: %+v", tk.Epoch)
	}
}

// A stalled direction opens an epoch within one tick, whatever the timer says.
func TestStrategyStalledDirectionTriggersAnEpochWithinOneTick(t *testing.T) {
	var idleSince time.Time
	r := newStratRig(t, finishCharterJSON, func(d *StrategyHostDeps) {
		d.Epoch.StallWindow = 10 * time.Minute
		d.Activity = func(_ context.Context, open []types.Direction) ([]epoch.DirectionActivity, error) {
			var out []epoch.DirectionActivity
			for _, x := range open {
				out = append(out, epoch.DirectionActivity{ID: x.ID, LastProgress: idleSince})
			}
			return out, nil
		}
	})
	idleSince = r.clock.Now()
	res, err := r.propose(t, "a direction that will stall")
	if err != nil || !res.Accepted {
		t.Fatal(res, err)
	}
	if err := r.host.Sink().MoveDirection(bg, "p1", 1, res.DirectionID, types.DirActive); err != nil {
		t.Fatal(err)
	}
	tk := r.tickAfter(t, 11*time.Minute) // past the stall window, well before the 30-minute timer
	if tk.Epoch == nil || tk.Epoch.Outcome != epoch.OutcomeApplied || tk.Epoch.Trigger != types.TriggerDirectionStalled {
		t.Fatalf("%+v", tk.Epoch)
	}
}

// A3: a proposal is checked for novelty; a repeat is refused with the matching direction cited, a near-repeat too, and the
// read verb shows the one direction.
func TestStrategyProposalNoveltyOnTheRealStack(t *testing.T) {
	r := newStratRig(t, finishCharterJSON, nil)
	first, err := r.propose(t, "Cache the build graph")
	if err != nil || !first.Accepted || first.DirectionID == "" {
		t.Fatalf("%+v %v", first, err)
	}
	for _, title := range []string{"Cache the build graph", "  cache   THE build graph "} {
		dup, err := r.propose(t, title)
		if err != nil || dup.Accepted || dup.MatchedID != first.DirectionID {
			t.Fatalf("%q: %+v %v", title, dup, err)
		}
	}
	if r.count(t, `SELECT COUNT(*) FROM doomsday_strategy_directions`) != 1 {
		t.Fatal("a repeat created a direction")
	}
	rd := r.w.StrategyReader(r.w.DB, r.w.Witness.Keys)
	v, err := rd.ReadDirections(bg, 0, 10)
	if err != nil || len(v.Directions) != 1 {
		t.Fatalf("%+v %v", v, err)
	}
	r.witnessAccepts(t)
}

// A4/A5 on the real finish engine: the finish is begun only by the strategy host for the recorded boss, from a fresh judgment
// in which every criterion is plausibly met; any other judgment, a missing boss and a charter without criteria never begin
// it, and the run stays running.
func TestStrategyNeverBeginsAFinishWithoutAFreshAllMetJudgmentAndABoss(t *testing.T) {
	for _, c := range []struct {
		name   string
		status types.CriterionStatus
		boss   bool
	}{
		{"progressing", types.CritProgressing, true},
		{"regressed", types.CritRegressed, true},
		{"not started", types.CritNotStarted, true},
		{"all met but no boss recorded", types.CritPlausiblyMet, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newStratRig(t, finishCharterJSON, func(d *StrategyHostDeps) {
				if !c.boss {
					inner := d.Runs
					d.Runs = noBoss{inner}
				}
			})
			r.judge.set(c.status)
			r.tickAfter(t, 31*time.Minute)
			if r.begins != 0 {
				t.Fatalf("the finish was requested")
			}
			if run, _ := r.w.Store.GetRun(bg, "p1", 1); run.State != store.StateRunning {
				t.Fatalf("the run is %s", run.State)
			}
		})
	}
	t.Run("a charter with no criteria", func(t *testing.T) {
		r := newStratRig(t, goodCharter, nil)
		r.judge.set(types.CritPlausiblyMet)
		r.tickAfter(t, 31*time.Minute)
		if r.begins != 0 {
			t.Fatal("a charter with no criteria requested a finish")
		}
	})
	t.Run("a stale judgment", func(t *testing.T) {
		r := newStratRig(t, finishCharterJSON, nil)
		r.judge.set(types.CritPlausiblyMet)
		r.deps.SubjectChangedAt = nil // an unknown subject time reads as stale
		h, err := NewStrategyHost(r.deps)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = h.Tick(bg)
		r.clock.Advance(31 * time.Minute)
		if _, err := h.Tick(bg); err != nil {
			t.Fatal(err)
		}
		if r.begins != 0 {
			t.Fatal("a judgment of unknown freshness requested a finish")
		}
	})
}

type noBoss struct{ StrategyRunSource }

func (n noBoss) Current(ctx context.Context) (StrategyRun, bool, error) {
	run, ok, err := n.StrategyRunSource.Current(ctx)
	run.Boss, run.HasBoss = 0, false
	return run, ok, err
}

// No model-callable path can request, approve or begin a finish through strategy: the strategy operations refuse every
// other name, the internal one has no session entry, the typed allowlist of approvals does not list them, and only the
// recorded boss's tree may call at all. Nothing changes on a refusal.
func TestNoSessionCallablePathRequestsApprovesOrBeginsAFinishThroughStrategy(t *testing.T) {
	r := newStratRig(t, finishCharterJSON, nil)
	before := r.count(t, `SELECT COUNT(*) FROM doomsday_audit`)
	for op, want := range map[string]error{
		ops.OpFinishBegin: ops.ErrNotAllowlisted, ops.OpStrategyEpoch: ops.ErrNotCallable, "finish.commit": ops.ErrNotAllowlisted,
		"strategy.approve": ops.ErrNotAllowlisted, "": ops.ErrNotAllowlisted, "plan.approve": ops.ErrNotAllowlisted,
	} {
		if _, err := r.w.BossStrategy(bg, r.top, op, json.RawMessage(`{}`)); !errors.Is(err, want) {
			t.Errorf("%q: got %v, want %v", op, err, want)
		}
	}
	// the typed approvals' allowlist does not know the strategy operations
	for _, op := range []string{ops.OpStrategyPropose, ops.OpStrategyReport, ops.OpStrategyEpoch} {
		if ops.Allowed(op) {
			t.Errorf("%s is on the typed allowlist of approvals", op)
		}
		o := r.w.Ops(nil)
		act, ok, err := o.Adapter.Active(bg, "p1")
		if err != nil || !ok {
			t.Fatal(ok, err)
		}
		if _, err := o.Enforcer.Decide(bg, ops.Request{Profile: "p1", CharterID: act.ID, Hash: act.Hash, Generation: act.Generation, Op: op,
			Caller: ops.Caller{Session: r.top}, Addressed: r.top}); !errors.Is(err, ops.ErrNotAllowlisted) {
			t.Errorf("Decide(%s) = %v", op, err)
		}
	}
	// a lead that is not the boss's tree
	other := r.topLead(t, "conv-other")
	args, _ := json.Marshal(ops.ProposeArgs{Title: "x", Rationale: "y", Criteria: []string{"c0"}})
	if _, err := r.w.BossStrategy(bg, other, ops.OpStrategyPropose, args); !errors.Is(err, ops.ErrCallerMismatch) {
		t.Errorf("another lead: %v", err)
	}
	// extra fields that look like a finish are not arguments
	if _, err := r.w.BossStrategy(bg, r.top, ops.OpStrategyPropose, json.RawMessage(`{"title":"t","rationale":"r","criteria":["c0"],"finish":true}`)); !errors.Is(err, ops.ErrBadArguments) {
		t.Errorf("an unknown field: %v", err)
	}
	if after := r.count(t, `SELECT COUNT(*) FROM doomsday_audit`); after != before || r.begins != 0 {
		t.Fatalf("a refused call wrote %d rows, begins %d", after-before, r.begins)
	}
}

// A6: after a Stop nothing new is written (no proposal, no epoch, no judge call, no finish request) and the epoch left open
// is closed as abandoned, even though the run has already quiesced between two ticks.
func TestStrategyAfterStopWritesNothingNewAndAbandonsOpenEpochs(t *testing.T) {
	r := newStratRig(t, finishCharterJSON, nil)
	started := r.clock.Now().Unix()
	open, created, err := r.host.store.InsertEpoch(bg, types.Epoch{Profile: "p1", Generation: 1, Seq: 1, Trigger: types.TriggerTimer, StartedAt: started, State: types.EpochOpen, ModelPin: "p/m@1"})
	if err != nil || !created || open.State != types.EpochOpen {
		t.Fatalf("%+v %v", open, err)
	}
	if out, err := r.do(t, "stop"); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if run, _ := r.w.Store.GetRun(bg, "p1", 1); run.State != store.StateQuiescent {
		t.Fatalf("the run is %s", run.State)
	}
	calls, rows := r.judge.n(), r.strategyRows(t)
	if _, err := r.propose(t, "after the stop"); !errors.Is(err, ops.ErrNoCharter) {
		t.Fatalf("a proposal after the stop: %v", err)
	}
	r.judge.set(types.CritPlausiblyMet)
	for i := 0; i < 3; i++ {
		r.tickAfter(t, 40*time.Minute)
	}
	if r.judge.n() != calls || r.begins != 0 {
		t.Fatalf("judge calls %d -> %d, begins %d", calls, r.judge.n(), r.begins)
	}
	ep, err := r.host.store.GetEpoch(bg, "p1", 1, 1)
	if err != nil || ep.State != types.EpochAbandoned {
		t.Fatalf("the open epoch is %v (%v)", ep.State, err)
	}
	if n := r.strategyRows(t); n != rows+1 || r.count(t, `SELECT COUNT(*) FROM doomsday_strategy_epochs`) != 1 {
		t.Fatalf("strategy rows %d -> %d, epochs %d: only the abandonment may be written", rows, n, r.count(t, `SELECT COUNT(*) FROM doomsday_strategy_epochs`))
	}
	r.witnessAccepts(t)
}

// A7: a restart resumes from the database. A completed epoch is not judged again by a new host, and an epoch the old host
// left open is judged exactly once, under its own sequence number.
func TestStrategyRestartResumesWithoutDoubleJudging(t *testing.T) {
	r := newStratRig(t, finishCharterJSON, nil)
	r.tickAfter(t, 31*time.Minute)
	if r.judge.n() != 1 {
		t.Fatalf("calls %d", r.judge.n())
	}
	h2, err := NewStrategyHost(r.deps)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if tk, err := h2.Tick(bg); err != nil || (tk.Epoch != nil && tk.Epoch.Outcome == epoch.OutcomeApplied) {
			t.Fatalf("a restarted host judged again: %+v %v", tk.Epoch, err)
		}
	}
	if r.judge.n() != 1 || r.count(t, `SELECT COUNT(*) FROM doomsday_strategy_epochs`) != 1 {
		t.Fatalf("calls %d epochs %d", r.judge.n(), r.count(t, `SELECT COUNT(*) FROM doomsday_strategy_epochs`))
	}
	// the old host died after opening the second epoch and before judging it
	now := r.clock.Now().Add(31 * time.Minute)
	r.clock.Advance(31 * time.Minute)
	if _, _, err := r.host.store.InsertEpoch(bg, types.Epoch{Profile: "p1", Generation: 1, Seq: 2, Trigger: types.TriggerTimer, StartedAt: now.Unix(), State: types.EpochOpen, ModelPin: "p/m@1"}); err != nil {
		t.Fatal(err)
	}
	h3, err := NewStrategyHost(r.deps)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := h3.Tick(bg); err != nil {
			t.Fatal(err)
		}
	}
	ep, err := h3.store.GetEpoch(bg, "p1", 1, 2)
	if err != nil || ep.State != types.EpochApplied || r.judge.n() != 2 || r.count(t, `SELECT COUNT(*) FROM doomsday_strategy_epochs`) != 2 {
		t.Fatalf("epoch 2 is %v, calls %d, epochs %d (%v)", ep.State, r.judge.n(), r.count(t, `SELECT COUNT(*) FROM doomsday_strategy_epochs`), err)
	}
	r.witnessAccepts(t)
}

// A8: the sixth parked round does not request again: five requests, each refused by the finish engine and parked, then the
// layer gives up once and goes on working; the status verb shows the five rounds.
func TestStrategyStopsRequestingAfterFiveParkedRounds(t *testing.T) {
	refusals := 0
	r := newStratRig(t, finishCharterJSON, func(d *StrategyHostDeps) {
		d.Finish = func(context.Context) (BeginReport, error) {
			refusals++
			return BeginReport{Refused: "a person item is open"}, nil
		}
	})
	r.judge.set(types.CritPlausiblyMet)
	for i := 0; i < 8; i++ {
		r.tickAfter(t, 31*time.Minute)
	}
	if refusals != types.MaxParkedFinishRounds {
		t.Fatalf("the finish was requested %d times, want %d", refusals, types.MaxParkedFinishRounds)
	}
	if r.count(t, `SELECT COUNT(*) FROM doomsday_audit WHERE profile='p1' AND generation=1 AND kind=?`, types.KindFinishGaveUp) != 1 {
		t.Fatal("the operator was not told exactly once")
	}
	if run, _ := r.w.Store.GetRun(bg, "p1", 1); run.State != store.StateRunning {
		t.Fatalf("the run is %s", run.State)
	}
	if r.judge.n() < 8 { // epochs go on: the layer keeps judging while it no longer asks
		t.Fatalf("judge calls %d", r.judge.n())
	}
	v, err := r.w.StrategyReader(r.w.DB, r.w.Witness.Keys).ReadStatus(bg, 0)
	if err != nil || v.ParkedFinish != types.MaxParkedFinishRounds {
		t.Fatalf("%+v %v", v, err)
	}
	r.witnessAccepts(t)
}

func TestStrategyOpsHostNeverTicks(t *testing.T) {
	r := newStratRig(t, finishCharterJSON, nil)
	h, err := NewStrategyOpsHost(r.deps)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Tick(bg); err == nil || !strings.Contains(err.Error(), "operations only") {
		t.Fatalf("an ops host ticked: %v", err)
	}
}

func TestStrategyHostRefusesToExistWithoutAWitnessedSequence(t *testing.T) {
	r := newStratRig(t, finishCharterJSON, nil)
	d := r.deps
	d.Seq = nil
	if _, err := NewStrategyHost(d); err == nil {
		t.Fatal("a host without a sequencer was built")
	}
	if _, err := NewStrategyOpsHost(d); err == nil {
		t.Fatal("an ops host without a sequencer was built")
	}
}

// The judge is the charter's boss pin run through the product's runner with one turn and no tools; it can never be the
// finish verifier's pin (the independence rule), and a run with no pin has no judge.
func TestStrategyJudgeModelIsTheBossPinAndNeverTheVerifier(t *testing.T) {
	var got monomind.ExecOptions
	x := StrategyExtras{JudgeExec: func(_ context.Context, o monomind.ExecOptions, _ func(monomind.Event)) (*monomind.TurnResult, error) {
		got = o
		return &monomind.TurnResult{SawDone: true, ResultText: "{}"}, nil
	}}
	r := newStratRig(t, finishCharterJSON, nil)
	d, err := r.w.StrategyHostDeps(bg, x)
	if err != nil {
		t.Fatal(err)
	}
	run, ok, err := d.Runs.Current(bg)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	m := d.ModelFor(run)
	if m == nil || m.Pin() != "p/m@1" || run.Verifier.String() != "p/v@2" {
		t.Fatalf("pin %v, verifier %v", m, run.Verifier)
	}
	if _, err := m.Complete(bg, judgeRequest("sys", "user")); err != nil {
		t.Fatal(err)
	}
	if got.Runtime != "p" || got.Model != "m" || got.MaxTurns != 1 || got.Sandbox != monomind.TurnSandboxMode || got.Prompt != "user" || got.SystemPrompt != "sys" {
		t.Fatalf("%+v", got)
	}
	refuse := d.RefuseFor(run)
	if refuse(run.Judge.String()) || !refuse(run.Verifier.String()) {
		t.Fatal("the independence rule does not separate the judge from the verifier")
	}
	if d.ModelFor(StrategyRun{}) != nil {
		t.Fatal("a run with no pin got a judge")
	}
	if !d.RefuseFor(StrategyRun{})("anything") {
		t.Fatal("a run with no verifier pin must refuse every judge")
	}
}

func judgeRequest(system, user string) judge.Request {
	return judge.Request{System: system, User: user, MaxBytes: 1024}
}
