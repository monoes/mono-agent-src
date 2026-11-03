package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

type fakeSink struct {
	proposals []StrategyProposal
	dirs      map[string]types.Direction
	moves     []string
	judged    [][]byte
	bad       int
	exp       types.Expect
	moveErr   error
}

func (f *fakeSink) Propose(_ context.Context, p StrategyProposal) (ProposeOutcome, error) {
	f.proposals = append(f.proposals, p)
	id := fmt.Sprintf("d%d", len(f.proposals))
	return ProposeOutcome{Accepted: true, DirectionID: id, Outcome: types.NoveltyAccepted}, nil
}
func (f *fakeSink) GetDirection(_ context.Context, _ string, _ int64, id string) (types.Direction, error) {
	d, ok := f.dirs[id]
	if !ok {
		return types.Direction{}, errors.New("no such direction")
	}
	return d, nil
}
func (f *fakeSink) MoveDirection(_ context.Context, _ string, _ int64, id string, to types.DirectionState) error {
	f.moves = append(f.moves, id+">"+string(to))
	return f.moveErr
}
func (f *fakeSink) EpochExpect(context.Context, string, int64, int64) (types.Expect, error) {
	return f.exp, nil
}
func (f *fakeSink) RecordJudgment(_ context.Context, _ string, _ int64, _ int64, body []byte) (bool, error) {
	f.judged = append(f.judged, body)
	return true, nil
}
func (f *fakeSink) MarkBadJudgment(context.Context, string, int64, int64) error { f.bad++; return nil }

// fakeAllowlist registers the names as the integrator's S2 edit would.
func fakeAllowlist() map[string]bool {
	m := map[string]bool{}
	for _, s := range StrategyOpSpecs() {
		if s.SessionCallable {
			m[s.Name] = true
		}
	}
	return m
}

type srig struct {
	*rig
	s    *StrategyOps
	sink *fakeSink
}

// planner session 5 and lead session 6 sit under the boss 4; session 8 is in another tree (boss 9).
func newSRig(t *testing.T) *srig {
	t.Helper()
	r := newRig(t)
	r.e.Bosses = fakeBosses{id: 4, roots: map[int64]int64{5: 4, 6: 4, 8: 9}}
	sink := &fakeSink{dirs: map[string]types.Direction{"d1": {ID: "d1", ProposerSession: "5", SessionID: "6"}}}
	return &srig{rig: r, s: &StrategyOps{E: r.e, Sink: sink}, sink: sink}
}

var callSeq int

func sreq(op, session string, args any) StrategyRequest {
	callSeq++
	n, _ := strconv.ParseInt(session, 10, 64)
	raw, _ := json.Marshal(args)
	if s, ok := args.(string); ok {
		raw = json.RawMessage(s)
	}
	return StrategyRequest{Profile: "default", CharterID: "ch1", Hash: "h1", Generation: 7, Op: op, CallID: fmt.Sprintf("c%d", callSeq),
		Caller: Caller{n}, Addressed: n, Args: raw}
}

func propose(session string) StrategyRequest {
	return sreq(OpStrategyPropose, session, ProposeArgs{Title: "try a cache", Rationale: "reads dominate", Criteria: []string{"crit-1"}})
}

func TestSpecsAndAllowlist(t *testing.T) {
	al := fakeAllowlist()
	if !al[OpStrategyPropose] || !al[OpStrategyReport] || al[OpStrategyEpoch] {
		t.Fatalf("allowlist %v: epoch must not be session-callable", al)
	}
	if AllowedStrategy(OpStrategyEpoch) || AllowedStrategy(OpFinishBegin) || AllowedStrategy("strategy.finish") {
		t.Fatal("only propose and report are session operations")
	}
	for _, s := range StrategyOpSpecs() {
		if strings.HasPrefix(s.Name, "finish") {
			t.Fatalf("no strategy op may be a finish: %s", s.Name)
		}
		if s.SessionCallable && (s.Counter == "" || s.Cap < 1) {
			t.Fatalf("%s: a session op needs a bound", s.Name)
		}
	}
}

// Mutant tried: SessionCallable true on strategy.epoch makes both this and TestSpecsAndAllowlist red.
func TestEpochIsNotCallableBySessionsAndNothingFinishes(t *testing.T) {
	r := newSRig(t)
	for _, op := range []string{OpStrategyEpoch, OpFinishBegin, "finish.commit", "strategy.finish", ""} {
		for _, who := range []string{"4", "5"} {
			_, err := r.s.Handle(bg, sreq(op, who, `{"epoch":1}`))
			if err == nil || !(errors.Is(err, ErrNotCallable) || errors.Is(err, ErrNotAllowlisted)) {
				t.Fatalf("%q by %s: %v", op, who, err)
			}
		}
	}
	if _, err := r.s.Handle(bg, sreq(OpStrategyEpoch, "4", `{}`)); !errors.Is(err, ErrNotCallable) {
		t.Fatalf("epoch must be refused as internal, got %v", err)
	}
	if len(r.sink.judged) != 0 || len(r.l.consumed) != 0 {
		t.Fatal("a refused call wrote something")
	}
}

func TestProposeRecordsWithVerifiedProposerAndChargesOnce(t *testing.T) {
	r := newSRig(t)
	req := propose("5")
	res, err := r.s.Handle(bg, req)
	if err != nil || !res.Accepted || res.DirectionID != "d1" || res.Outcome != types.NoveltyAccepted {
		t.Fatalf("%+v %v", res, err)
	}
	p := r.sink.proposals[0]
	if p.Proposal.ProposerSession != "5" || p.Generation != 7 || p.Profile != "default" || p.Proposal.Title != "try a cache" {
		t.Fatalf("%+v", p)
	}
	if r.l.used[KindStrategyProposals] != 1 || len(r.l.audits) != 1 || r.l.audits[0].Op != OpStrategyPropose {
		t.Fatalf("counter %v audits %v", r.l.used, r.l.audits)
	}
	if _, err := r.s.Handle(bg, req); !errors.Is(err, ErrAlreadyConsumed) || len(r.sink.proposals) != 1 {
		t.Fatalf("a repeated call id must be a replay: %v", err)
	}
}

// Mutant tried: DisallowUnknownFields removed from decodeStrict -> the unknown-field rows are accepted (red).
func TestArgumentsAreDecodedStrictly(t *testing.T) {
	r := newSRig(t)
	for name, raw := range map[string]string{
		"unknown field":  `{"title":"a","rationale":"b","criteria":[],"approve":true}`,
		"proposer field": `{"title":"a","rationale":"b","criteria":[],"proposer":"4"}`,
		"trailing":       `{"title":"a","rationale":"b","criteria":[]} {}`,
		"not an object":  `[1]`,
		"empty":          ``,
		"wrong type":     `{"title":5}`,
		"empty title":    `{"title":"","rationale":"b","criteria":[]}`,
		"control char":   `{"title":"a\u0000b","rationale":"","criteria":[]}`,
		"too many crit":  `{"title":"a","rationale":"","criteria":[` + strings.Repeat(`"c",`, types.MaxProposalCrit) + `"c"]}`,
	} {
		if _, err := r.s.Handle(bg, sreq(OpStrategyPropose, "5", raw)); !errors.Is(err, ErrBadArguments) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, raw := range map[string]string{
		"unknown field": `{"direction":"d1","state":"finished","extra":1}`,
		"pruned":        `{"direction":"d1","state":"pruned"}`,
		"active":        `{"direction":"d1","state":"active"}`,
		"no direction":  `{"state":"finished"}`,
	} {
		if _, err := r.s.Handle(bg, sreq(OpStrategyReport, "5", raw)); !errors.Is(err, ErrBadArguments) {
			t.Errorf("report %s: %v", name, err)
		}
	}
	if len(r.sink.proposals) != 0 || len(r.sink.moves) != 0 || len(r.l.consumed) != 0 {
		t.Fatal("a refused call reached the store or spent a unit")
	}
}

// Mutant tried: the size caps removed (MaxStrategyArgsBytes check and Proposal.Validate) -> oversized rows accepted (red).
func TestOversizedPayloadsAreRefused(t *testing.T) {
	r := newSRig(t)
	big := func(n int) string { return strings.Repeat("x", n) }
	for name, a := range map[string]ProposeArgs{
		"title":     {Title: big(types.MaxTitleLen + 1)},
		"rationale": {Title: "t", Rationale: big(types.MaxRationaleLen + 1)},
	} {
		if _, err := r.s.Handle(bg, sreq(OpStrategyPropose, "5", a)); !errors.Is(err, ErrBadArguments) {
			t.Errorf("%s: %v", name, err)
		}
	}
	huge := `{"title":"t","rationale":"` + big(MaxStrategyArgsBytes) + `","criteria":[]}`
	if _, err := r.s.Handle(bg, sreq(OpStrategyPropose, "5", huge)); !errors.Is(err, ErrBadArguments) {
		t.Errorf("raw size: %v", err)
	}
	padded := `{"title":"t","rationale":"","criteria":[]}` + strings.Repeat(" ", MaxStrategyArgsBytes)
	if _, err := r.s.Handle(bg, sreq(OpStrategyPropose, "5", padded)); !errors.Is(err, ErrBadArguments) {
		t.Errorf("raw size with valid content: %v", err)
	}
	if len(r.sink.proposals) != 0 {
		t.Fatal("an oversized proposal reached the store")
	}
	body := ProposeArgs{Title: big(types.MaxTitleLen), Rationale: big(types.MaxRationaleLen)}
	if _, err := r.s.Handle(bg, sreq(OpStrategyPropose, "5", body)); err != nil {
		t.Fatalf("the exact cap is fine: %v", err)
	}
	out, err := r.s.IntakeEpoch(bg, judge(1, []byte(big(types.MaxJudgmentBytes+1))))
	if err != nil || !out.Bad || len(r.sink.judged) != 0 {
		t.Fatalf("an oversized judgment is a bad judgment and is not stored: %+v %v", out, err)
	}
}

// Mutant tried: ignoring the live flag in live() makes the stopped-run rows pass (red).
func TestRefusesWhenTheRunIsNotRunning(t *testing.T) {
	r := newSRig(t)
	r.c.live = false
	for _, req := range []StrategyRequest{propose("5"), sreq(OpStrategyReport, "5", ReportArgs{Direction: "d1", State: "stalled"})} {
		if _, err := r.s.Handle(bg, req); !errors.Is(err, ErrNoCharter) {
			t.Fatalf("%s: %v", req.Op, err)
		}
	}
	if _, err := r.s.IntakeEpoch(bg, judge(1, []byte(`{}`))); !errors.Is(err, ErrNoCharter) {
		t.Fatalf("intake: %v", err)
	}
	if len(r.sink.proposals)+len(r.sink.moves)+len(r.sink.judged)+r.sink.bad != 0 || len(r.l.consumed) != 0 {
		t.Fatal("a stopped run was written to")
	}
}

// Mutant tried: dropping requireBoss from Handle admits session 8 (another tree) and 99 (unknown) (red).
func TestRefusesSessionsThatAreNotOfTheActiveCharter(t *testing.T) {
	r := newSRig(t)
	for _, who := range []string{"8", "99"} {
		if _, err := r.s.Handle(bg, propose(who)); !errors.Is(err, ErrCallerMismatch) {
			t.Fatalf("session %s: %v", who, err)
		}
	}
	mis := propose("5")
	mis.Addressed = 6
	stale := propose("5")
	stale.Hash = "other"
	zero := propose("0")
	nocall := propose("5")
	nocall.CallID = ""
	for name, req := range map[string]StrategyRequest{"addressed": mis, "stale": stale, "zero": zero, "callid": nocall} {
		if _, err := r.s.Handle(bg, req); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	r.e.Bosses = nil
	if _, err := r.s.Handle(bg, propose("5")); !errors.Is(err, ErrCallerMismatch) {
		t.Fatalf("no boss recorded: %v", err)
	}
	if len(r.sink.proposals) != 0 || len(r.l.consumed) != 0 {
		t.Fatal("a refused caller wrote something")
	}
}

func TestPerRunCeilingBoundsProposals(t *testing.T) {
	r := newSRig(t)
	for i := 0; i < MaxStrategyProposalsPerRun; i++ {
		if _, err := r.s.Handle(bg, propose("5")); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if _, err := r.s.Handle(bg, propose("5")); !errors.Is(err, ErrStrategyCap) {
		t.Fatalf("over the ceiling: %v", err)
	}
	if len(r.sink.proposals) != MaxStrategyProposalsPerRun || len(r.pk.parks) != 0 {
		t.Fatalf("%d stored, %d parks", len(r.sink.proposals), len(r.pk.parks))
	}
}

func TestReportMovesOnlyOwnDirectionsToStalledOrFinished(t *testing.T) {
	r := newSRig(t)
	rep := func(who, state string) StrategyRequest {
		return sreq(OpStrategyReport, who, ReportArgs{Direction: "d1", State: state})
	}
	if res, err := r.s.Handle(bg, rep("6", "finished")); err != nil || res.State != types.DirFinished || res.DirectionID != "d1" {
		t.Fatalf("lead owner: %+v %v", res, err)
	}
	if _, err := r.s.Handle(bg, rep("5", "stalled")); err != nil {
		t.Fatalf("proposer: %v", err)
	}
	if _, err := r.s.Handle(bg, rep("4", "stalled")); err != nil {
		t.Fatalf("boss: %v", err)
	}
	r.e.Bosses = fakeBosses{id: 4, roots: map[int64]int64{5: 4, 6: 4, 7: 4}}
	if _, err := r.s.Handle(bg, rep("7", "finished")); !errors.Is(err, ErrNotYourStrat) {
		t.Fatalf("another planner of the tree: %v", err)
	}
	if got := strings.Join(r.sink.moves, ","); got != "d1>finished,d1>stalled,d1>stalled" {
		t.Fatalf("moves %s", got)
	}
}

func judge(seq int64, body []byte) JudgeResult {
	return JudgeResult{Profile: "default", CharterID: "ch1", Hash: "h1", Generation: 7, Seq: seq, Body: body}
}

func goodJudgment() []byte {
	b, _ := json.Marshal(types.Judgment{
		Criteria:   []types.CriterionProgress{{ID: "crit-1", Status: types.CritPlausiblyMet}},
		Directions: []types.DirectionAssessment{{ID: "d1", Verdict: types.VerdictKeep}},
	})
	return b
}

func TestEpochIntakeRecordsAValidJudgmentOnly(t *testing.T) {
	r := newSRig(t)
	r.sink.exp = types.Expect{CriterionIDs: []string{"crit-1"}, DirectionIDs: []string{"d1"}}
	out, err := r.s.IntakeEpoch(bg, judge(2, goodJudgment()))
	if err != nil || !out.Recorded || out.Bad || len(r.sink.judged) != 1 {
		t.Fatalf("%+v %v", out, err)
	}
	// Every kind of bad output is a recorded bad-judgment that changes nothing else; "all met" is just a record here.
	for name, body := range map[string]string{
		"garbage":       `not json`,
		"unknown field": `{"criteria":[{"id":"crit-1","status":"plausibly-met"}],"directions":[{"id":"d1","verdict":"keep"}],"finish":true}`,
		"unknown id":    `{"criteria":[{"id":"crit-9","status":"plausibly-met"}],"directions":[]}`,
		"skipped crit":  `{"criteria":[],"directions":[]}`,
		"bad verdict":   `{"criteria":[{"id":"crit-1","status":"plausibly-met"}],"directions":[{"id":"d1","verdict":"finish"}]}`,
	} {
		before := len(r.sink.judged)
		out, err := r.s.IntakeEpoch(bg, judge(3, []byte(body)))
		if err != nil || !out.Bad || out.Recorded || len(r.sink.judged) != before {
			t.Errorf("%s: %+v %v", name, out, err)
		}
	}
	if r.sink.bad != 5 {
		t.Fatalf("bad judgments recorded: %d", r.sink.bad)
	}
	if _, err := r.s.IntakeEpoch(bg, judge(0, goodJudgment())); !errors.Is(err, ErrBadArguments) {
		t.Fatalf("epoch 0: %v", err)
	}
	stale := judge(2, goodJudgment())
	stale.Generation = 6
	if _, err := r.s.IntakeEpoch(bg, stale); !errors.Is(err, ErrCharterStale) {
		t.Fatalf("stale generation: %v", err)
	}
}
