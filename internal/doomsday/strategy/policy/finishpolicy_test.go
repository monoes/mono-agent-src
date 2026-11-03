package policy

import (
	"context"
	"errors"
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

// good is a state in which a request is allowed; each test spoils exactly one thing.
func good() State {
	return State{RunRunning: true, Caller: "boss", Boss: "boss", CriterionIDs: []string{"c1", "c2"}, Judgment: met("c1", "c2"),
		JudgmentAt: t0.Add(-time.Minute), Now: t0}
}

func TestDecideRequestsOnlyWhenEverythingHolds(t *testing.T) {
	if d := (Config{}).Decide(good()); d.Action != ActionRequest || d.Reason != ReasonRequested {
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
		{"planner asks", func(s *State) { s.Caller = "planner-1" }, ActionWait, ReasonNotBoss},
		{"empty caller and boss", func(s *State) { s.Caller, s.Boss = "", "" }, ActionWait, ReasonNotBoss},
		{"no boss known", func(s *State) { s.Boss = "" }, ActionWait, ReasonNotBoss},
		{"no criteria", func(s *State) { s.CriterionIDs, s.Judgment = nil, met() }, ActionWait, ReasonNoCriteria},
		{"no judgment", func(s *State) { s.Judgment = nil }, ActionWait, ReasonNoJudgment},
		{"zero judgment time", func(s *State) { s.JudgmentAt = time.Time{} }, ActionWait, ReasonStale},
		{"judgment from the future", func(s *State) { s.JudgmentAt = t0.Add(time.Second) }, ActionWait, ReasonStale},
		{"judgment too old", func(s *State) { s.JudgmentAt = t0.Add(-2*types.DefaultEpochInterval - time.Second) }, ActionWait, ReasonStale},
		{"subject changed after judgment", func(s *State) { s.SubjectChangedAt = t0.Add(-time.Second) }, ActionWait, ReasonStale},
		{"subject changed at judgment time", func(s *State) { s.SubjectChangedAt = s.JudgmentAt }, ActionWait, ReasonStale},
		{"one criterion not met", func(s *State) { s.Judgment.Criteria[1].Status = types.CritProgressing }, ActionWait, ReasonNotAllMet},
		{"one criterion missing", func(s *State) { s.Judgment = met("c1") }, ActionWait, ReasonNotAllMet},
		{"unknown extra criterion", func(s *State) { s.Judgment = met("c1", "c2", "c3") }, ActionWait, ReasonNotAllMet},
		{"regressed", func(s *State) { s.Judgment.Criteria[0].Status = types.CritRegressed }, ActionWait, ReasonNotAllMet},
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

func TestDecideParkedThenNewerJudgmentRequestsAgain(t *testing.T) {
	s := good()
	s.LastParkedAt = s.JudgmentAt.Add(-time.Second)
	s.ParkedRounds = types.MaxParkedFinishRounds - 1
	if d := (Config{}).Decide(s); d.Action != ActionRequest {
		t.Fatalf("the last allowed round: %v", d)
	}
}

func TestDecideLastRoundBoundary(t *testing.T) {
	s := good()
	s.ParkedRounds = types.MaxParkedFinishRounds - 1
	if (Config{}).Decide(s).Action != ActionRequest {
		t.Fatal("4 parked rounds still allow a request")
	}
	s.ParkedRounds = types.MaxParkedFinishRounds
	if (Config{}).Decide(s).Action != ActionGiveUp {
		t.Fatal("the bound is reached")
	}
}

func TestDecideGiveUpIsForTheBossOnly(t *testing.T) {
	s := good()
	s.ParkedRounds, s.Caller = 9, "planner"
	if d := (Config{}).Decide(s); d.Action == ActionGiveUp {
		t.Fatalf("a planner must not trigger the notification path: %v", d)
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

type fakeFinish struct {
	calls int
	out   Outcome
	err   error
}

func (f *fakeFinish) Begin(context.Context) (Outcome, error) { f.calls++; return f.out, f.err }

type fakeSink struct {
	kinds []string
	err   error
}

func (s *fakeSink) Record(_ context.Context, kind, _ string) error {
	if s.err != nil {
		return s.err
	}
	s.kinds = append(s.kinds, kind)
	return nil
}

func TestRunnerRequestsAndRecordsFirst(t *testing.T) {
	f, s := &fakeFinish{out: Outcome{Accepted: true}}, &fakeSink{}
	d, err := (Runner{Finish: f, Sink: s}).Step(context.Background(), good())
	if err != nil || d.Action != ActionRequest || f.calls != 1 {
		t.Fatalf("%v %v calls=%d", d, err, f.calls)
	}
	if len(s.kinds) != 1 || s.kinds[0] != types.KindFinishRequested {
		t.Fatalf("lines: %v", s.kinds)
	}
}

func TestRunnerNeverAsksOnAnyOtherDecision(t *testing.T) {
	f, s := &fakeFinish{}, &fakeSink{}
	r := Runner{Finish: f, Sink: s}
	for _, spoil := range []func(*State){
		func(st *State) { st.Caller = "planner" },
		func(st *State) { st.CriterionIDs = nil },
		func(st *State) { st.Judgment = nil },
		func(st *State) { st.SubjectChangedAt = st.JudgmentAt },
		func(st *State) { st.FinishInProgress = true },
		func(st *State) { st.RunRunning = false },
		func(st *State) { st.ParkedRounds = types.MaxParkedFinishRounds },
	} {
		st := good()
		spoil(&st)
		if _, err := r.Step(context.Background(), st); err != nil {
			t.Fatal(err)
		}
	}
	if f.calls != 0 {
		t.Fatalf("finish engine asked %d times", f.calls)
	}
}

func TestRunnerEngineRefusalIsRecordedAsParked(t *testing.T) {
	f, s := &fakeFinish{out: Outcome{Refused: "3 person item(s) are open"}}, &fakeSink{}
	if _, err := (Runner{Finish: f, Sink: s}).Step(context.Background(), good()); err != nil {
		t.Fatal(err)
	}
	if len(s.kinds) != 2 || s.kinds[1] != types.KindFinishParked {
		t.Fatalf("lines: %v", s.kinds)
	}
}

func TestRunnerNoAuditNoRequest(t *testing.T) {
	f := &fakeFinish{out: Outcome{Accepted: true}}
	_, err := (Runner{Finish: f, Sink: &fakeSink{err: errors.New("disk")}}).Step(context.Background(), good())
	if !errors.Is(err, ErrAudit) || f.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, f.calls)
	}
	if _, err = (Runner{Finish: f}).Step(context.Background(), good()); !errors.Is(err, ErrAudit) || f.calls != 0 {
		t.Fatalf("nil sink: err=%v calls=%d", err, f.calls)
	}
}

func TestRunnerGiveUpNotifiesOnce(t *testing.T) {
	f, s := &fakeFinish{}, &fakeSink{}
	st := good()
	st.ParkedRounds = types.MaxParkedFinishRounds
	r := Runner{Finish: f, Sink: s}
	if d, err := r.Step(context.Background(), st); err != nil || d.Action != ActionGiveUp {
		t.Fatalf("%v %v", d, err)
	}
	st.GaveUpNotified = true
	if _, err := r.Step(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if len(s.kinds) != 1 || s.kinds[0] != types.KindFinishGaveUp || f.calls != 0 {
		t.Fatalf("lines %v calls %d", s.kinds, f.calls)
	}
}

func TestRunnerEngineErrorSurfaces(t *testing.T) {
	f := &fakeFinish{err: errors.New("boom")}
	if _, err := (Runner{Finish: f, Sink: &fakeSink{}}).Step(context.Background(), good()); err == nil {
		t.Fatal("want error")
	}
}

func TestRunnerCancelledContextAsksNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &fakeFinish{}
	if _, err := (Runner{Finish: f, Sink: &fakeSink{}}).Step(ctx, good()); err == nil || f.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, f.calls)
	}
}
