package task

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
	"github.com/monoes/mono-agent/internal/missionpursuit"
	"github.com/monoes/mono-agent/internal/missionpursuit/verifier"
	"github.com/monoes/mono-agent/internal/storage"
)

const checkGoalJSON = `[{"kind":"command","spec":"go test ./...","expected":"exit:0"},{"kind":"file","spec":"docs/report.md","expected":"exists"},{"kind":"rubric","spec":"The report reads well"},{"kind":"rubric","spec":"Nothing is made up"}]`

type checkDoc struct {
	View               string                `json:"view"`
	Check              *missionpursuit.Check `json:"check"`
	Notice             string                `json:"notice"`
	UnconfinedChecksOK bool                  `json:"unconfined_checks_ok"`
}

// checkedMission is a started pursuit whose goal has a passed check over a real project folder (host-written through the
// capability, as the host step will), and what is needed to answer it.
type checkedMission struct {
	db      string
	session int64
	tok     string
	root    string
	check   int64
	goal    missionpursuit.Goal
}

func newCheckedMission(t *testing.T, containment string) checkedMission {
	t.Helper()
	db := newTaskTestDB(t)
	s, tok := goalMission(t, db, "mission")
	var g goalDoc
	mustTaskJSON(t, db, "default", &g, checkGoalJSON, "session", "goal", "set", id(s), "--criteria", "-")
	var st pursuitDoc
	mustTaskJSON(t, db, "default", &st, "", "session", "pursuit", "start", id(s))
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tree, err := finish.DigestTree(root, "")
	if err != nil {
		t.Fatal(err)
	}
	d, err := storage.NewDatabase(db)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	hc := missionpursuit.NewHostChecks(missionpursuit.NewStore(d.DB))
	goal, err := missionpursuit.NewStore(d.DB).Get(context.Background(), "default", s)
	if err != nil {
		t.Fatal(err)
	}
	cid, err := hc.Begin(context.Background(), "default", s, missionpursuit.BeginCheck{GoalHash: goal.Hash, Round: 1, SubjectRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	for i, k := range []string{"command", "file"} {
		c := goal.Criteria[i]
		if err := hc.Add(context.Background(), cid, verifier.CriterionResult{ID: c.ID, Hash: c.Hash, Kind: k, Result: verifier.ResPass, StdoutDigest: "aabbccddeeff0011", Stdout: "PRIVATE-OUTPUT-MARKER \x1b[31m", RunsSubjectCode: k == "command"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := hc.Finish(context.Background(), cid, verifier.Result{Outcome: verifier.OutcomePassedMachine, GoalHash: goal.Hash, SubjectDigest: tree.Digest, SubjectFiles: 1,
		Containment: containment, Net: "denied", ResultsDigest: "RD", ReviewerPin: "a/checker", ReviewerObserved: "a/checker", ReviewerVerdict: "ok", CostMicro: 2000, CostKnown: true}); err != nil {
		t.Fatal(err)
	}
	asOperator(t)
	return checkedMission{db: db, session: s, tok: tok, root: root, check: cid, goal: goal}
}

func (m checkedMission) read(t *testing.T) checkDoc {
	t.Helper()
	var d checkDoc
	mustTaskJSON(t, m.db, "default", &d, "", "session", "goal", "check", id(m.session))
	return d
}

func (m checkedMission) answerArgs(d checkDoc, crit string, verdict string, extra ...string) []string {
	a := []string{"session", "goal", "answer", id(m.session), "--check", id(m.check), "--criterion", crit, verdict, "--dossier", d.Check.Dossier}
	return append(a, extra...)
}

func TestGoalCheckShowsTheOperatorTheExcerptsAndAnAgentHostFactsOnly(t *testing.T) {
	t.Parallel()
	m := newCheckedMission(t, verifier.ContainmentFileOnly)
	op := m.read(t)
	if op.View != "operator" || op.Check == nil || op.Check.Outcome != "passed-machine" || len(op.Check.Results) != 2 || len(op.Check.Rubric) != 2 {
		t.Fatalf("operator view: %+v", op)
	}
	if !strings.Contains(op.Check.Results[0].Stdout, "PRIVATE-OUTPUT-MARKER") || strings.Contains(op.Check.Results[0].Stdout, "\x1b") {
		t.Errorf("the operator's excerpt: %q", op.Check.Results[0].Stdout)
	}
	asLead(t, m.tok)
	out, _, err := runTask(t, m.db, "default", true, "", "session", "goal", "check", id(m.session), "--as", "lead")
	if err != nil {
		t.Fatalf("an agent reads the check: %v %s", err, out)
	}
	if strings.Contains(out, "PRIVATE-OUTPUT-MARKER") {
		t.Errorf("an agent was shown what a command printed: %s", out)
	}
	if !strings.Contains(out, `"view": "host facts"`) || !strings.Contains(out, "aabbccddeeff0011") || !strings.Contains(out, "The report reads well") {
		t.Errorf("an agent lost a host fact or the criteria: %s", out)
	}
	text, _, err := runTask(t, m.db, "default", false, "", "session", "goal", "check", id(m.session), "--as", "lead")
	if err != nil || strings.Contains(text, "PRIVATE-OUTPUT-MARKER") {
		t.Errorf("an agent's text view: %v %q", err, text)
	}
}

func TestGoalCheckNamesWhatIsNotConfinedWheneverTheCheckRanUnconfined(t *testing.T) {
	t.Parallel()
	m := newCheckedMission(t, verifier.ContainmentUnconfined)
	text, _, err := runTask(t, m.db, "default", false, "", "session", "goal", "check", id(m.session))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"UNCONFINED", "files", "sockets", "/proc", "keyring", "setsid"} {
		if !strings.Contains(text, want) {
			t.Errorf("the text view lacks %q:\n%s", want, text)
		}
	}
	d := m.read(t)
	if !d.Check.Unconfined || !strings.Contains(d.Notice, "UNCONFINED") {
		t.Errorf("json: unconfined %v, notice %q", d.Check.Unconfined, d.Notice)
	}
	calm := newCheckedMission(t, verifier.ContainmentConfinedPrefix+"box + process-group")
	text, _, _ = runTask(t, calm.db, "default", false, "", "session", "goal", "check", id(calm.session))
	if strings.Contains(text, "UNCONFINED") {
		t.Errorf("a check the host confined is marked unconfined:\n%s", text)
	}
}

func TestGoalCheckOfAMissionWithNoCheckSaysSo(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	s, _ := confirmedGoalMission(t, db)
	mustTaskJSON(t, db, "default", &pursuitDoc{}, "", "session", "pursuit", "start", id(s))
	var d checkDoc
	mustTaskJSON(t, db, "default", &d, "", "session", "goal", "check", id(s))
	if d.Check != nil {
		t.Errorf("a check from nowhere: %+v", d.Check)
	}
	text, _, err := runTask(t, db, "default", false, "", "session", "goal", "check", id(s))
	if err != nil || !strings.Contains(text, "no goal check yet") {
		t.Errorf("text: %v %q", err, text)
	}
}

func TestGoalAnswerIsTheOperatorsAlone(t *testing.T) {
	t.Parallel()
	m := newCheckedMission(t, verifier.ContainmentFileOnly)
	d := m.read(t)
	asLead(t, m.tok)
	out, _, err := runTask(t, m.db, "default", true, "", append(m.answerArgs(d, "c3", "--yes"), "--as", "lead")...)
	if exitCode(err) != 3 || !strings.Contains(out, `"operator_only"`) {
		t.Errorf("the lead's answer: exit %d %s", exitCode(err), out)
	}
	out, _, err = runTask(t, m.db, "default", true, "", m.answerArgs(d, "c3", "--yes")...)
	if exitCode(err) != 3 {
		t.Errorf("an answer with the lead's token and no --as: exit %d %s", exitCode(err), out)
	}
	asOperator(t)
	if got := m.read(t); got.Check.Rubric[0].Answer != "" {
		t.Errorf("an agent's answer was recorded: %+v", got.Check.Rubric)
	}
}

func TestGoalAnswerAcceptsOnTheLastYesAndRefusesStaleOrChanged(t *testing.T) {
	t.Parallel()
	m := newCheckedMission(t, verifier.ContainmentFileOnly)
	d := m.read(t)
	bad := failedTaskJSON(t, m.db, "default", 3, "session", "goal", "answer", id(m.session), "--check", id(m.check), "--criterion", "c3", "--yes", "--dossier", strings.Repeat("a", 64))
	if !strings.Contains(bad["error"].(string), "changed since you read it") {
		t.Errorf("a stale dossier: %v", bad)
	}
	failedTaskJSON(t, m.db, "default", 3, "session", "goal", "answer", id(m.session), "--check", id(m.check), "--criterion", "c1", "--yes", "--dossier", d.Check.Dossier) // a command criterion
	failedTaskJSON(t, m.db, "default", 3, "session", "goal", "answer", id(m.session), "--check", id(m.check), "--criterion", "c3", "--dossier", d.Check.Dossier)          // neither yes nor no
	failedTaskJSON(t, m.db, "default", 3, "session", "goal", "answer", id(m.session), "--check", id(m.check), "--criterion", "c3", "--yes", "--no", "--dossier", d.Check.Dossier)
	// the project changes after the check: the yes is refused, and nothing is recorded
	if err := os.WriteFile(filepath.Join(m.root, "main.go"), []byte("package main // edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	moved := failedTaskJSON(t, m.db, "default", 3, m.answerArgs(d, "c3", "--yes")...)
	if !strings.Contains(moved["error"].(string), "the project changed after the check; a new check will run") {
		t.Errorf("a changed project: %v", moved)
	}
	if got := m.read(t); got.Check.Rubric[0].Answer != "" {
		t.Fatalf("an answer was recorded for a changed project: %+v", got.Check.Rubric)
	}
	if err := os.WriteFile(filepath.Join(m.root, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var one checkDoc
	mustTaskJSON(t, m.db, "default", &one, "", m.answerArgs(d, "c3", "--yes")...)
	if one.Check.Outcome != "passed-machine" || one.Check.Rubric[0].Answer != "yes" {
		t.Fatalf("after one yes: %+v", one.Check)
	}
	failedTaskJSON(t, m.db, "default", 3, m.answerArgs(d, "c3", "--yes")...) // once per criterion
	var two checkDoc
	mustTaskJSON(t, m.db, "default", &two, "", m.answerArgs(d, "c4", "--yes")...)
	if two.Check.Outcome != "accepted" {
		t.Fatalf("after every yes: %+v", two.Check)
	}
	text, _, err := runTask(t, m.db, "default", false, "", "session", "goal", "check", id(m.session))
	if err != nil || !strings.Contains(text, "accepted") || !strings.Contains(text, "task session close") {
		t.Errorf("the accepted check's text: %v\n%s", err, text)
	}
}

func TestGoalAnswerNoRejectsAndKeepsTheNote(t *testing.T) {
	t.Parallel()
	m := newCheckedMission(t, verifier.ContainmentFileOnly)
	d := m.read(t)
	var r checkDoc
	mustTaskJSON(t, m.db, "default", &r, "", m.answerArgs(d, "c4", "--no", "--note", "two claims are invented")...)
	if r.Check.Outcome != "rejected" || r.Check.Rubric[1].Answer != "no" || r.Check.Rubric[1].Note != "two claims are invented" {
		t.Fatalf("after a no: %+v", r.Check)
	}
	failedTaskJSON(t, m.db, "default", 3, m.answerArgs(d, "c3", "--yes")...)
	asLead(t, m.tok)
	out, _, err := runTask(t, m.db, "default", true, "", "session", "goal", "check", id(m.session), "--as", "lead")
	if err != nil || strings.Contains(out, "two claims are invented") {
		t.Errorf("the lead was shown the person's note through the read verb: %v %s", err, out)
	}
}

func TestPursuitAllowUnconfinedChecksIsTheOperatorsAndDefaultsOff(t *testing.T) {
	t.Parallel()
	m := newCheckedMission(t, verifier.ContainmentFileOnly)
	if d := m.read(t); d.UnconfinedChecksOK {
		t.Fatal("the switch is born on")
	}
	asLead(t, m.tok)
	out, _, err := runTask(t, m.db, "default", true, "", "session", "pursuit", "allow-unconfined-checks", id(m.session), "--on", "--as", "lead")
	if exitCode(err) != 3 || !strings.Contains(out, `"operator_only"`) {
		t.Errorf("the lead turned it on: exit %d %s", exitCode(err), out)
	}
	asOperator(t)
	failedTaskJSON(t, m.db, "default", 3, "session", "pursuit", "allow-unconfined-checks", id(m.session))
	failedTaskJSON(t, m.db, "default", 3, "session", "pursuit", "allow-unconfined-checks", id(m.session), "--on", "--off")
	var already struct {
		Changed bool `json:"changed"`
	}
	mustTaskJSON(t, m.db, "default", &already, "", "session", "pursuit", "allow-unconfined-checks", id(m.session), "--off") // off already: reported, not an error
	if already.Changed {
		t.Error("switching off what is off said it changed something")
	}
	text0, _, err := runTask(t, m.db, "default", false, "", "session", "pursuit", "allow-unconfined-checks", id(m.session), "--off")
	if err != nil || !strings.Contains(text0, "Already off") {
		t.Errorf("already off: %v %q", err, text0)
	}
	var on pursuitDoc
	mustTaskJSON(t, m.db, "default", &on, "", "session", "pursuit", "allow-unconfined-checks", id(m.session), "--on")
	if !on.Pursuit.UnconfinedChecksOK || on.Pursuit.UnconfinedBy != "you" {
		t.Fatalf("on: %+v", on.Pursuit)
	}
	text, _, err := runTask(t, m.db, "default", false, "", "session", "pursuit", "allow-unconfined-checks", id(m.session), "--off")
	if err != nil || !strings.Contains(text, "Off:") {
		t.Errorf("off: %v %q", err, text)
	}
	if d := m.read(t); d.UnconfinedChecksOK {
		t.Error("the switch stayed on")
	}
	text, _, _ = runTask(t, m.db, "default", false, "", "session", "pursuit", "allow-unconfined-checks", id(m.session), "--on")
	for _, want := range []string{"UNCONFINED", "sockets", "/proc", "keyring", "setsid"} {
		if !strings.Contains(text, want) {
			t.Errorf("the notice for turning it on lacks %q:\n%s", want, text)
		}
	}
}

func TestGoalCheckOfAMissionWhoseGoalIsConfirmedButNotPursuedSaysNoCheckYet(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	s, _ := confirmedGoalMission(t, db) // no pursuit started
	text, _, err := runTask(t, db, "default", false, "", "session", "goal", "check", id(s))
	if err != nil || !strings.Contains(text, "no goal check yet") {
		t.Errorf("a mission with no pursuit: %v %q", err, text)
	}
}

func TestGoalCheckShowsAMicroDollarCostAndAnswerRepeatsTheUnconfinedNotice(t *testing.T) {
	t.Parallel()
	m := newCheckedMission(t, verifier.ContainmentUnconfined)
	text, _, err := runTask(t, m.db, "default", false, "", "session", "goal", "check", id(m.session))
	if err != nil || !strings.Contains(text, "cost $0.002000") {
		t.Errorf("a cost of 2000 micro-USD: %v\n%s", err, text)
	}
	d := m.read(t)
	out, _, err := runTask(t, m.db, "default", false, "", m.answerArgs(d, "c3", "--yes")...)
	if err != nil || !strings.Contains(out, "UNCONFINED") {
		t.Errorf("the answer to an unconfined check: %v\n%s", err, out)
	}
	var j struct {
		Notice string `json:"notice"`
	}
	mustTaskJSON(t, m.db, "default", &j, "", m.answerArgs(d, "c4", "--yes")...)
	if !strings.Contains(j.Notice, "UNCONFINED") {
		t.Errorf("the json answer lacks the notice: %q", j.Notice)
	}
}

// A check that cannot be answered is refused before the project is read: the project is gone, and the reason is still the check's.
func TestGoalAnswerAsksTheStoreBeforeItReadsTheProject(t *testing.T) {
	t.Parallel()
	m := newCheckedMission(t, verifier.ContainmentFileOnly)
	d := m.read(t)
	d2, err := storage.NewDatabase(m.db)
	if err != nil {
		t.Fatal(err)
	}
	hc := missionpursuit.NewHostChecks(missionpursuit.NewStore(d2.DB))
	next, err := hc.Begin(context.Background(), "default", m.session, missionpursuit.BeginCheck{GoalHash: m.goal.Hash, Round: 2, SubjectRoot: m.root})
	if err != nil {
		t.Fatal(err)
	}
	if err := hc.Finish(context.Background(), next, verifier.Result{Outcome: verifier.OutcomeFailed, GoalHash: m.goal.Hash}); err != nil {
		t.Fatal(err)
	}
	d2.Close()
	if err := os.RemoveAll(m.root); err != nil {
		t.Fatal(err)
	}
	e := failedTaskJSON(t, m.db, "default", 3, m.answerArgs(d, "c3", "--yes")...) // check 1 is not the latest any more
	msg := e["error"].(string)
	if !strings.Contains(msg, "not the latest check") || strings.Contains(msg, "could not be read") {
		t.Errorf("the project was read before the check was asked: %q", msg)
	}
}
