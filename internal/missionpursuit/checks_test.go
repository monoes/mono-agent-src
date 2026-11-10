package missionpursuit

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/missionpursuit/verifier"
	"github.com/monoes/mono-agent/internal/tasks"
)

// checkCriteria: c1 a command, c2 a file, c3 and c4 rubric criteria the person decides.
func checkCriteria() []Criterion {
	return []Criterion{
		{Kind: "command", Spec: "go test ./...", Expected: "exit:0"},
		{Kind: "file", Spec: "docs/report.md", Expected: "exists"},
		{Kind: "rubric", Spec: "The report reads well to the person who asked for it"},
		{Kind: "rubric", Spec: "Nothing in the report is made up"},
	}
}

type checkWorld struct {
	fixture
	goal Goal
	hc   HostChecks
}

// newCheckWorld is a mission whose goal is locked by a started pursuit.
func newCheckWorld(t *testing.T) checkWorld {
	t.Helper()
	f := newMission(t)
	if _, err := f.store.Set(bg, "default", f.session, checkCriteria(), human); err != nil {
		t.Fatal(err)
	}
	mustStart(t, f)
	g, err := f.store.Get(bg, "default", f.session)
	if err != nil || g.State != StateLocked {
		t.Fatalf("goal %+v %v", g, err)
	}
	return checkWorld{fixture: f, goal: g, hc: NewHostChecks(f.store)}
}

func (w checkWorld) begin(t *testing.T) int64 {
	t.Helper()
	round := int64(count(t, w.db, `SELECT COUNT(*) FROM mission_goal_checks WHERE session_id = ?`, w.session)) + 1
	id, err := w.hc.Begin(bg, "default", w.session, BeginCheck{GoalHash: w.goal.Hash, Round: round, SubjectRoot: "/work/project"})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	return id
}

func (w checkWorld) crit(pos int) verifier.CriterionResult {
	c := w.goal.Criteria[pos-1]
	return verifier.CriterionResult{ID: c.ID, Hash: c.Hash, Kind: c.Kind, Result: verifier.ResPass, StdoutDigest: "sd", StderrDigest: "ed", Stdout: "ok \x1b[31mred‮", Net: "denied"}
}

func (w checkWorld) result(outcome verifier.Outcome, verdict string) verifier.Result {
	return verifier.Result{Outcome: outcome, GoalHash: w.goal.Hash, SubjectDigest: "SUBJECT", SubjectFiles: 12, Containment: verifier.ContainmentFileOnly,
		ResultsDigest: "RD", ReviewerPin: "a/b", ReviewerObserved: "a/b", ReviewerVerdict: verdict, CostMicro: 1500, CostKnown: true}
}

// passed runs a whole check to passed-machine and returns its id.
func (w checkWorld) passed(t *testing.T) int64 {
	t.Helper()
	id := w.begin(t)
	for _, pos := range []int{1, 2} {
		if err := w.hc.Add(bg, id, w.crit(pos)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.hc.Finish(bg, id, w.result(verifier.OutcomePassedMachine, "ok")); err != nil {
		t.Fatal(err)
	}
	return id
}

func (w checkWorld) read(t *testing.T, id int64) Check {
	t.Helper()
	c, err := w.store.GetCheck(bg, "default", w.session, id, true)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (w checkWorld) answer(t *testing.T, c Check, crit string, yes bool, by tasks.Actor) (Check, error) {
	return w.store.AnswerRubric(bg, "default", w.session, RubricAnswer{Check: c.ID, Criterion: crit, Yes: yes, Note: "n", Dossier: c.Dossier, SubjectNow: c.SubjectDigest}, by)
}

func pursuitUpdatedAt(t *testing.T, w checkWorld) string {
	t.Helper()
	var at string
	if err := w.db.QueryRow(`SELECT updated_at FROM mission_pursuit WHERE session_id = ?`, w.session).Scan(&at); err != nil {
		t.Fatal(err)
	}
	return at
}

func TestTheHostRecordsACheckAndNeverActsAsThePerson(t *testing.T) {
	w := newCheckWorld(t)
	before := pursuitUpdatedAt(t, w)
	events := count(t, w.db, `SELECT COUNT(*) FROM task_session_events`)
	id := w.passed(t)
	c := w.read(t, id)
	if c.State != CheckDone || c.Outcome != OutcomePassedMachine || c.WrittenBy != HostVerifier || c.ReviewerVerdict != "ok" || c.CostMicro != 1500 || !c.CostKnown {
		t.Fatalf("check = %+v", c)
	}
	if len(c.Results) != 2 || c.Results[0].Criterion != "c1" || c.Results[1].Criterion != "c2" {
		t.Fatalf("results = %+v", c.Results)
	}
	if strings.ContainsAny(c.Results[0].Stdout, "\x1b‮") || !strings.Contains(c.Results[0].Stdout, "ok") {
		t.Errorf("an excerpt was not cleaned: %q", c.Results[0].Stdout)
	}
	if len(c.Rubric) != 2 || c.Rubric[0].Criterion != "c3" || c.Rubric[1].Spec != "Nothing in the report is made up" {
		t.Errorf("rubric = %+v", c.Rubric)
	}
	// the host is not the person: no event row, and the date the away limits are measured from did not move
	if n := count(t, w.db, `SELECT COUNT(*) FROM task_session_events`); n != events {
		t.Errorf("the host verifier wrote %d task_session_events rows", n-events)
	}
	if got := pursuitUpdatedAt(t, w); got != before {
		t.Errorf("a host check moved mission_pursuit.updated_at from %s to %s: it is not a person's action", before, got)
	}
	if n := count(t, w.db, `SELECT COUNT(*) FROM mission_goal_checks WHERE written_by <> 'host-verifier'`); n != 0 {
		t.Errorf("%d rows not written by the host verifier", n)
	}
}

func TestTheZeroHostChecksRefusesEverything(t *testing.T) {
	var h HostChecks
	if _, err := h.Begin(bg, "default", 1, BeginCheck{}); err == nil {
		t.Error("begin")
	}
	if err := h.Add(bg, 1, verifier.CriterionResult{}); err == nil {
		t.Error("add")
	}
	if err := h.Finish(bg, 1, verifier.Result{}); err == nil {
		t.Error("finish")
	}
	if _, err := h.AbandonRunning(bg); err == nil {
		t.Error("abandon")
	}
}

func TestBeginRefusesWhatIsNotTheLockedGoalOfAStartedPursuit(t *testing.T) {
	w := newCheckWorld(t)
	if _, err := w.hc.Begin(bg, "default", w.session, BeginCheck{GoalHash: strings.Repeat("0", 64), Round: 1}); err == nil {
		t.Error("a check for another goal hash was accepted")
	}
	if _, err := w.hc.Begin(bg, "other", w.session, BeginCheck{GoalHash: w.goal.Hash}); err == nil {
		t.Error("a check in another profile was accepted")
	}
	w.begin(t)
	_, err := w.hc.Begin(bg, "default", w.session, BeginCheck{GoalHash: w.goal.Hash, Round: 2, SubjectRoot: "/work/project"})
	wantIs(t, err, ErrCheckRunning, "a second running check")
	// a mission with no pursuit has no check
	f := newFixture(t, "mission", tasks.SessionKindMission)
	if _, err := f.store.Set(bg, "default", f.session, checkCriteria(), human); err != nil {
		t.Fatal(err)
	}
	g, _ := f.store.Get(bg, "default", f.session)
	_, err = NewHostChecks(f.store).Begin(bg, "default", f.session, BeginCheck{GoalHash: g.Hash, Round: 1, SubjectRoot: "/work/project"})
	wantIs(t, err, ErrNotStarted, "a check without a pursuit")
}

func TestFinishPairsTheResultWithItsCheckAndKnowsTheOutcomes(t *testing.T) {
	w := newCheckWorld(t)
	id := w.begin(t)
	bad := w.result(verifier.OutcomePassedMachine, "ok")
	bad.GoalHash = strings.Repeat("1", 64)
	if err := w.hc.Finish(bg, id, bad); err == nil {
		t.Error("a result for another goal ended the check")
	}
	if err := w.hc.Finish(bg, id, w.result(verifier.Outcome("accepted"), "ok")); err == nil {
		t.Error("the person's outcome was written by the host")
	}
	if err := w.hc.Finish(bg, id, w.result(verifier.OutcomePassedMachine, "maybe")); err == nil {
		t.Error("a verdict outside ok and block was written")
	}
	if err := w.hc.Finish(bg, 999, w.result(verifier.OutcomeFailed, "")); !errors.Is(err, ErrNoCheck) {
		t.Errorf("an unknown check: %v", err)
	}
	if err := w.hc.Finish(bg, id, w.result(verifier.OutcomeAbandoned, "")); err != nil {
		t.Fatal(err)
	}
	if c := w.read(t, id); c.State != CheckAbandoned || c.Outcome != "" {
		t.Errorf("abandoned check = %+v", c)
	}
	if err := w.hc.Finish(bg, id, w.result(verifier.OutcomeFailed, "")); err == nil {
		t.Error("an ended check ended again")
	}
}

func TestAddKeepsRubricCriteriaOutAndTakesNoForgedPositions(t *testing.T) {
	w := newCheckWorld(t)
	id := w.begin(t)
	if err := w.hc.Add(bg, id, verifier.CriterionResult{ID: "c3", Hash: w.goal.Criteria[2].Hash, Kind: "rubric", Result: verifier.ResPerson}); err != nil {
		t.Errorf("a person result is skipped, not an error: %v", err)
	}
	if n := count(t, w.db, `SELECT COUNT(*) FROM mission_goal_check_results`); n != 0 {
		t.Errorf("a rubric result was stored: %d", n)
	}
	for _, bad := range []string{"", "c0", "c17", "c01", "x1", "c1 "} {
		r := w.crit(1)
		r.ID = bad
		if err := w.hc.Add(bg, id, r); err == nil {
			t.Errorf("criterion id %q was accepted", bad)
		}
	}
	r := w.crit(1)
	r.Hash = strings.Repeat("f", 64)
	if err := w.hc.Add(bg, id, r); err == nil {
		t.Error("a result with the wrong criterion hash was stored")
	}
	if err := w.hc.Add(bg, id, w.crit(1)); err != nil {
		t.Fatal(err)
	}
	if err := w.hc.Add(bg, id, w.crit(1)); err == nil {
		t.Error("a result for c1 was stored twice")
	}
}

func TestAbandonRunningEndsOnlyRunningChecks(t *testing.T) {
	w := newCheckWorld(t)
	done := w.passed(t)
	run := w.begin(t)
	n, err := w.hc.AbandonRunning(bg)
	if err != nil || n != 1 {
		t.Fatalf("abandoned %d, %v", n, err)
	}
	if c := w.read(t, run); c.State != CheckAbandoned {
		t.Errorf("running check = %+v", c)
	}
	if c := w.read(t, done); c.Outcome != OutcomePassedMachine {
		t.Errorf("a finished check was touched: %+v", c)
	}
}

func TestOnlyThePersonAnswersARubricCriterion(t *testing.T) {
	w := newCheckWorld(t)
	c := w.read(t, w.passed(t))
	for name, by := range map[string]tasks.Actor{"the lead": lead, "an agent": agent("x"), "a capture": {Kind: tasks.Capture, Name: "chrome"}, "nobody": {}} {
		_, err := w.answer(t, c, "c3", true, by)
		wantIs(t, err, tasks.ErrOperatorOnly, name)
	}
	if n := count(t, w.db, `SELECT COUNT(*) FROM mission_goal_answers`); n != 0 {
		t.Errorf("%d answers by a non-person", n)
	}
}

func TestEveryYesAcceptsAndTheLastOneDoesItInTheSameStep(t *testing.T) {
	w := newCheckWorld(t)
	c := w.read(t, w.passed(t))
	before := pursuitUpdatedAt(t, w)
	got, err := w.answer(t, c, "c3", true, human)
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != OutcomePassedMachine || got.Rubric[0].Answer != "yes" || got.Rubric[1].Answer != "" {
		t.Errorf("after one yes: %+v", got)
	}
	mid := pursuitUpdatedAt(t, w)
	if mid <= before {
		t.Errorf("the person's answer did not move mission_pursuit.updated_at: %s then %s", before, mid)
	}
	if _, err := w.answer(t, c, "c3", true, human); err == nil {
		t.Error("c3 was answered twice")
	}
	got, err = w.answer(t, c, "c4", true, human)
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != OutcomeAccepted {
		t.Errorf("after every yes: outcome %q", got.Outcome)
	}
	if after := pursuitUpdatedAt(t, w); after <= mid {
		t.Errorf("the last answer did not move updated_at: %s then %s", mid, after)
	}
	if _, err := w.answer(t, c, "c4", false, human); err == nil {
		t.Error("an accepted check took another answer")
	}
	if n := count(t, w.db, `SELECT COUNT(*) FROM mission_goal_answers WHERE answered_by = 'you'`); n != 2 {
		t.Errorf("answers by the person: %d", n)
	}
}

func TestANoRejectsTheCheckAtOnceAndKeepsTheNote(t *testing.T) {
	w := newCheckWorld(t)
	c := w.read(t, w.passed(t))
	got, err := w.store.AnswerRubric(bg, "default", w.session, RubricAnswer{Check: c.ID, Criterion: "c4", Yes: false, Note: "two\nclaims\x1b are invented", Dossier: c.Dossier, SubjectNow: c.SubjectDigest}, human)
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != OutcomeRejected || got.Rubric[1].Answer != "no" || got.Rubric[1].Note != "two claims  are invented" {
		t.Errorf("after a no: %+v", got)
	}
	if _, err := w.answer(t, c, "c3", true, human); err == nil {
		t.Error("a rejected check took another answer")
	}
	// the agent's view shows the enums and the criteria, never the person's note
	agentView, err := w.store.GetCheck(bg, "default", w.session, c.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if agentView.Rubric[1].Note != "" || agentView.Rubric[1].Answer != "no" {
		t.Errorf("agent view of the rubric: %+v", agentView.Rubric[1])
	}
}

func TestAnAnswerIsRefusedForAStaleDossierAndAChangedProject(t *testing.T) {
	w := newCheckWorld(t)
	c := w.read(t, w.passed(t))
	_, err := w.store.AnswerRubric(bg, "default", w.session, RubricAnswer{Check: c.ID, Criterion: "c3", Yes: true, Dossier: strings.Repeat("a", 64), SubjectNow: c.SubjectDigest}, human)
	wantIs(t, err, ErrDossierStale, "a dossier that is not the one read")
	_, err = w.store.AnswerRubric(bg, "default", w.session, RubricAnswer{Check: c.ID, Criterion: "c3", Yes: true, Dossier: c.Dossier, SubjectNow: "OTHER"}, human)
	wantIs(t, err, ErrSubjectChanged, "a project that changed after the check")
	_, err = w.store.AnswerRubric(bg, "default", w.session, RubricAnswer{Check: c.ID, Criterion: "c3", Yes: true, Dossier: c.Dossier}, human)
	wantIs(t, err, ErrSubjectChanged, "no project digest")
	_, err = w.store.AnswerRubric(bg, "default", w.session, RubricAnswer{Check: c.ID, Criterion: "c3", Yes: true}, human)
	wantIs(t, err, tasks.ErrInvalid, "no dossier")
	if n := count(t, w.db, `SELECT COUNT(*) FROM mission_goal_answers`); n != 0 {
		t.Errorf("%d answers after refusals", n)
	}
}

func TestAnAnswerNamesAnExistingRubricCriterionOfTheLatestPassedCheck(t *testing.T) {
	w := newCheckWorld(t)
	c := w.read(t, w.passed(t))
	for _, crit := range []string{"c1", "c2", "c5", "c0", "c99", "3", "rubric", ""} {
		if _, err := w.answer(t, c, crit, true, human); err == nil {
			t.Errorf("criterion %q was answered", crit)
		}
	}
	long := strings.Repeat("n", MaxNoteBytes+1)
	if _, err := w.store.AnswerRubric(bg, "default", w.session, RubricAnswer{Check: c.ID, Criterion: "c3", Yes: true, Note: long, Dossier: c.Dossier, SubjectNow: c.SubjectDigest}, human); err == nil {
		t.Error("a note over the limit was taken")
	}
	// a newer check makes the older one unanswerable
	next := w.begin(t)
	if _, err := w.answer(t, c, "c3", true, human); err == nil {
		t.Error("an older check was answered while a newer one runs")
	}
	if err := w.hc.Finish(bg, next, w.result(verifier.OutcomeFailed, "")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.answer(t, c, "c3", true, human); err == nil {
		t.Error("an older check was answered after a newer one")
	}
	failed := w.read(t, next)
	if _, err := w.answer(t, failed, "c3", true, human); err == nil {
		t.Error("a failed check was answered")
	}
}

func TestACheckWhoseReviewerBlockedTakesNoAnswer(t *testing.T) {
	w := newCheckWorld(t)
	id := w.begin(t)
	if err := w.hc.Finish(bg, id, w.result(verifier.OutcomePassedMachine, "block")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.answer(t, w.read(t, id), "c3", true, human); err == nil {
		t.Error("a check the reviewer blocked was answered")
	}
}

func TestTheSwitchIsTheOperatorsAndMovesTheDate(t *testing.T) {
	w := newCheckWorld(t)
	for _, by := range []tasks.Actor{lead, agent("x"), {Kind: tasks.Capture, Name: "chrome"}, {}} {
		_, _, err := w.store.SetUnconfinedChecks(bg, "default", w.session, true, by)
		wantIs(t, err, tasks.ErrOperatorOnly, "switch by "+by.Name)
	}
	p, _ := w.store.GetPursuit(bg, "default", w.session)
	if p.UnconfinedChecksOK {
		t.Fatal("the switch is born on")
	}
	atStart := pursuitUpdatedAt(t, w)
	if _, changed, err := w.store.SetUnconfinedChecks(bg, "default", w.session, false, human); err != nil || changed {
		t.Errorf("switching off what is off: changed %v, %v: it is a no-op, not an error", changed, err)
	}
	if pursuitUpdatedAt(t, w) != atStart {
		t.Error("a no-op moved updated_at: it is not an action of yours")
	}
	// a frozen clock: the date still moves forward, because a person's action must be later than the one before it
	frozen := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	w.store.now = func() time.Time { return frozen }
	before := pursuitUpdatedAt(t, w)
	p, changed, err := w.store.SetUnconfinedChecks(bg, "default", w.session, true, human)
	if err != nil || !changed || !p.UnconfinedChecksOK || p.UnconfinedBy != "you" {
		t.Fatalf("on: %+v %v", p, err)
	}
	mid := pursuitUpdatedAt(t, w)
	if mid == before {
		t.Error("switching on did not move updated_at")
	}
	p, _, err = w.store.SetUnconfinedChecks(bg, "default", w.session, false, human)
	if err != nil || p.UnconfinedChecksOK {
		t.Fatalf("off: %+v %v", p, err)
	}
	if after := pursuitUpdatedAt(t, w); after <= mid {
		t.Errorf("a second change in the same second did not move updated_at: %s then %s", mid, after)
	}
	f := newMission(t) // no pursuit
	_, _, err = f.store.SetUnconfinedChecks(bg, "default", f.session, true, human)
	if err == nil {
		t.Error("the switch moved on a mission with no pursuit")
	}
}

func TestWhatAnAgentSeesOfACheckIsHostFactsOnly(t *testing.T) {
	w := newCheckWorld(t)
	id := w.passed(t)
	op := w.read(t, id)
	ag, err := w.store.GetCheck(bg, "default", w.session, id, false)
	if err != nil {
		t.Fatal(err)
	}
	if op.Results[0].Stdout == "" {
		t.Fatal("the operator sees no excerpt")
	}
	for _, r := range ag.Results {
		if r.Stdout != "" || r.Stderr != "" || r.Detail != "" {
			t.Errorf("an agent sees an excerpt: %+v", r)
		}
	}
	if ag.Results[0].StdoutDigest == "" || ag.Results[0].Result != "pass" || ag.Rubric[0].Spec == "" || ag.Dossier != op.Dossier {
		t.Errorf("an agent lost a host fact: %+v", ag)
	}
	latest, err := w.store.LatestCheck(bg, "default", w.session, false)
	if err != nil || latest.ID != id {
		t.Errorf("latest = %+v, %v", latest, err)
	}
	if _, err := w.store.LatestCheck(bg, "default", w.session+50, false); err == nil {
		t.Error("a session of no one's was read")
	}
	f := newMission(t)
	if _, err := f.store.LatestCheck(bg, "default", f.session, false); !errors.Is(err, ErrNoCheck) {
		t.Errorf("no check: %v", err)
	}
}

func TestACheckThatRanUnconfinedSaysSo(t *testing.T) {
	for name, c := range map[string]struct {
		containment string
		want        bool
	}{
		"unconfined":    {verifier.ContainmentUnconfined, true},
		"file only":     {verifier.ContainmentFileOnly, true}, // a command result exists: the string claims nothing ran, the results say otherwise
		"confined":      {verifier.ContainmentConfinedPrefix + "box + process-group", false},
		"empty":         {"", true}, // a program ran and nothing says it was confined
		"something new": {"anything else", true},
	} {
		w := newCheckWorld(t)
		id := w.begin(t)
		if err := w.hc.Add(bg, id, w.crit(1)); err != nil {
			t.Fatal(err)
		}
		res := w.result(verifier.OutcomePassedMachine, "ok")
		res.Containment = c.containment
		if err := w.hc.Finish(bg, id, res); err != nil {
			t.Fatal(err)
		}
		if got := w.read(t, id).Unconfined; got != c.want {
			t.Errorf("%s: unconfined = %v, want %v", name, got, c.want)
		}
	}
}
