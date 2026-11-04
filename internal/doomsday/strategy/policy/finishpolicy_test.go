package policy

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

var t0 = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

func met(ids ...string) *types.Judgment {
	j := &types.Judgment{}
	for _, id := range ids {
		j.Criteria = append(j.Criteria, types.CriterionProgress{ID: id, Status: types.CritPlausiblyMet})
	}
	return j
}

// good is a state in which the run reads as ready; each test spoils exactly one thing.
func good() State {
	return State{RunRunning: true, CriterionIDs: []string{"c1", "c2"}, Judgment: met("c1", "c2"), EpochSeq: 3, JudgmentHash: "ab12",
		SubjectKnown: true, JudgmentAt: t0.Add(-time.Minute), Now: t0}
}

func TestDecideIsReadyOnlyWhenEverythingHolds(t *testing.T) {
	if d := (Config{}).Decide(good()); d.Action != ActionReady || d.Reason != ReasonReady || !d.Ready() {
		t.Fatalf("good state: %v", d)
	}
}

func TestDecideRefusals(t *testing.T) {
	cases := []struct {
		name   string
		spoil  func(*State)
		action Action
		reason Reason
	}{
		{"run not running", func(s *State) { s.RunRunning = false }, ActionWait, ReasonRunNotRunning},
		{"finish in progress", func(s *State) { s.FinishInProgress = true }, ActionWait, ReasonInProgress},
		{"no criteria", func(s *State) { s.CriterionIDs, s.Judgment = nil, met() }, ActionWait, ReasonNoCriteria},
		{"no judgment", func(s *State) { s.Judgment = nil }, ActionWait, ReasonNoJudgment},
		{"a later bad judgment clears an earlier all-met one", func(s *State) { s.LatestBad = true }, ActionWait, ReasonLatestBad},
		{"subject time unknown", func(s *State) { s.SubjectKnown = false }, ActionWait, ReasonSubjectUnknown},
		{"zero judgment time", func(s *State) { s.JudgmentAt = time.Time{} }, ActionWait, ReasonStale},
		{"judgment from the future", func(s *State) { s.JudgmentAt = t0.Add(time.Second) }, ActionWait, ReasonStale},
		{"judgment too old", func(s *State) { s.JudgmentAt = t0.Add(-2*types.DefaultEpochInterval - time.Second) }, ActionWait, ReasonStale},
		{"subject changed after judgment", func(s *State) { s.SubjectChangedAt = t0.Add(-time.Second) }, ActionWait, ReasonStale},
		{"subject changed at judgment time", func(s *State) { s.SubjectChangedAt = s.JudgmentAt }, ActionWait, ReasonStale},
		{"one criterion not met", func(s *State) { s.Judgment.Criteria[1].Status = types.CritProgressing }, ActionWait, ReasonNotAllMet},
		{"one criterion missing", func(s *State) { s.Judgment = met("c1") }, ActionWait, ReasonNotAllMet},
		{"the charter's criteria changed", func(s *State) { s.CriterionIDs = []string{"c1", "c2", "c3"} }, ActionWait, ReasonNotAllMet},
		{"unknown extra criterion", func(s *State) { s.Judgment = met("c1", "c2", "c3") }, ActionWait, ReasonNotAllMet},
		{"regressed", func(s *State) { s.Judgment.Criteria[0].Status = types.CritRegressed }, ActionWait, ReasonNotAllMet},
		{"a direction proposed after the judgment", func(s *State) { s.OpenDirectionIDs = []string{"d-new"} }, ActionWait, ReasonUnjudgedDirection},
		{"parked and judgment not newer", func(s *State) { s.LastParkedAt = s.JudgmentAt }, ActionPark, ReasonAwaitingNewJudg},
		{"parked after judgment", func(s *State) { s.LastParkedAt = s.JudgmentAt.Add(time.Second) }, ActionPark, ReasonAwaitingNewJudg},
		{"fifth parked round", func(s *State) { s.ParkedRounds = types.MaxParkedFinishRounds }, ActionGiveUp, ReasonParkedRounds},
		{"far past the bound", func(s *State) { s.ParkedRounds = 99 }, ActionGiveUp, ReasonParkedRounds},
	}
	for _, c := range cases {
		s := good()
		s.Judgment = met("c1", "c2")
		c.spoil(&s)
		d := (Config{}).Decide(s)
		if d.Action != c.action || d.Reason != c.reason {
			t.Errorf("%s: got %v, want %s: %s", c.name, d, c.action, c.reason)
		}
	}
}

func TestDecideCoveredDirectionsStayReady(t *testing.T) {
	s := good()
	s.Judgment.Directions = []types.DirectionAssessment{{ID: "d1", Verdict: types.VerdictKeep}}
	s.OpenDirectionIDs = []string{"d1"}
	if d := (Config{}).Decide(s); !d.Ready() {
		t.Fatalf("%v", d)
	}
}

func TestDecideParkedThenNewerJudgmentIsReadyAgain(t *testing.T) {
	s := good()
	s.LastParkedAt = s.JudgmentAt.Add(-time.Second)
	s.ParkedRounds = types.MaxParkedFinishRounds - 1
	if d := (Config{}).Decide(s); !d.Ready() {
		t.Fatalf("the last allowed round: %v", d)
	}
}

func TestDecideLastRoundBoundary(t *testing.T) {
	s := good()
	s.ParkedRounds = types.MaxParkedFinishRounds - 1
	if !(Config{}).Decide(s).Ready() {
		t.Fatal("4 parked rounds still allow the marker")
	}
	s.ParkedRounds = types.MaxParkedFinishRounds
	if (Config{}).Decide(s).Action != ActionGiveUp {
		t.Fatal("the bound is reached")
	}
}

func TestDecideIsDeterministic(t *testing.T) {
	s := good()
	first := (Config{}).Decide(s)
	for i := 0; i < 50; i++ {
		if (Config{}).Decide(s) != first {
			t.Fatal("decision changed")
		}
	}
}

func TestDecideHonoursAConfiguredAge(t *testing.T) {
	s := good()
	s.JudgmentAt = t0.Add(-10 * time.Minute)
	if (Config{MaxJudgmentAge: 5 * time.Minute}).Decide(s).Reason != ReasonStale {
		t.Fatal("older than the configured age must be stale")
	}
}

// ---- Runner ----

type fakeSink struct {
	kinds   []string
	details []string
	err     error
}

func (s *fakeSink) Record(_ context.Context, kind, detail string) error {
	if s.err != nil {
		return s.err
	}
	s.kinds, s.details = append(s.kinds, kind), append(s.details, detail)
	return nil
}

func TestRunnerRecordsTheReadyMarkerBoundToTheEpochAndJudgment(t *testing.T) {
	s := &fakeSink{}
	d, err := (Runner{Sink: s}).Step(context.Background(), good())
	if err != nil || !d.Ready() {
		t.Fatalf("%v %v", d, err)
	}
	if len(s.kinds) != 1 || s.kinds[0] != types.KindFinishReady || !strings.Contains(s.details[0], "epoch=3 judgment=ab12") {
		t.Fatalf("lines: %v %v", s.kinds, s.details)
	}
}

func TestRunnerRecordsTheMarkerOnlyOnce(t *testing.T) {
	s := &fakeSink{}
	st := good()
	st.ReadyRecorded = true
	if _, err := (Runner{Sink: s}).Step(context.Background(), st); err != nil || len(s.kinds) != 0 {
		t.Fatalf("err=%v lines=%v", err, s.kinds)
	}
}

func TestRunnerWritesNothingOnAnyOtherDecision(t *testing.T) {
	s := &fakeSink{}
	r := Runner{Sink: s}
	for _, spoil := range []func(*State){
		func(st *State) { st.CriterionIDs = nil },
		func(st *State) { st.Judgment = nil },
		func(st *State) { st.LatestBad = true },
		func(st *State) { st.SubjectChangedAt = st.JudgmentAt },
		func(st *State) { st.FinishInProgress = true },
		func(st *State) { st.RunRunning = false },
		func(st *State) { st.LastParkedAt = st.JudgmentAt },
	} {
		st := good()
		spoil(&st)
		if _, err := r.Step(context.Background(), st); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.kinds) != 0 {
		t.Fatalf("lines written: %v", s.kinds)
	}
}

// The Runner has no finish engine to call: its only seam is the audit sink, so it cannot begin, request or approve one.
func TestRunnerHasNoWayToCallAFinishEngine(t *testing.T) {
	rt := reflect.TypeOf(Runner{})
	for i := 0; i < rt.NumField(); i++ {
		if n := rt.Field(i).Name; n != "Config" && n != "Sink" {
			t.Errorf("Runner has a field %q besides the config and the sink", n)
		}
	}
}

func TestRunnerNoAuditSurfacesAndIsNotRetried(t *testing.T) {
	_, err := (Runner{Sink: &fakeSink{err: errors.New("disk")}}).Step(context.Background(), good())
	if !errors.Is(err, ErrAudit) {
		t.Fatalf("err=%v", err)
	}
	if _, err = (Runner{}).Step(context.Background(), good()); !errors.Is(err, ErrAudit) {
		t.Fatalf("nil sink: err=%v", err)
	}
}

func TestRunnerGiveUpNotifiesOnce(t *testing.T) {
	s := &fakeSink{}
	st := good()
	st.ParkedRounds = types.MaxParkedFinishRounds
	r := Runner{Sink: s}
	if d, err := r.Step(context.Background(), st); err != nil || d.Action != ActionGiveUp {
		t.Fatalf("%v %v", d, err)
	}
	st.GaveUpNotified = true
	if _, err := r.Step(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if len(s.kinds) != 1 || s.kinds[0] != types.KindFinishGaveUp {
		t.Fatalf("lines %v", s.kinds)
	}
}

func TestRunnerCancelledContextWritesNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := &fakeSink{}
	if _, err := (Runner{Sink: s}).Step(ctx, good()); err == nil || len(s.kinds) != 0 {
		t.Fatalf("err=%v lines=%v", err, s.kinds)
	}
}
