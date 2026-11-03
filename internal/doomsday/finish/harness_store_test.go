package finish_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
	"github.com/monoes/mono-agent/internal/doomsday/store"
	"github.com/monoes/mono-agent/internal/storage"
)

// These tests run the harness against the REAL SQLite store (the conditional insert, the verified chain): only the
// kernel (the placement into a containment unit) and the model are fakes, and both are named as such.

var bg = context.Background()

var verifierPin = finish.Pin{Provider: "anthropic", Model: "m2", Version: "v1", PolicyHash: "judging-v1"}

type rig struct {
	t        *testing.T
	db       *sql.DB
	s        *store.SQLStore
	l        *store.FinishLedger
	subject  string
	h        *finish.Harness
	verifier *scriptedVerifier
	placed   int
	ended    int
}

type scriptedVerifier struct {
	calls    int
	verdict  string
	observed finish.Pin
	err      error
}

func (v *scriptedVerifier) Review(context.Context, finish.ReviewInput) (finish.ReviewResult, error) {
	v.calls++
	return finish.ReviewResult{Observed: v.observed, Verdict: v.verdict, ReasonCode: "reviewed"}, v.err
}

func newRig(t *testing.T, crit []store.Criterion) *rig {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
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
	s := store.New(db.DB, store.NewHMACKeyer([]byte("k")))
	c := store.Charter{Profile: "p", Generation: 1, Goal: "ship it", Criteria: crit,
		Limits:      store.Limits{TreeDepth: 4, LivePerTree: 30, SpawnRate: 6, OpenSessions: 50, MaxConcurrent: 30, MaxTotal: 100, BudgetMicro: 5_000_000},
		Lines:       []string{store.LineSpending},
		BossPin:     store.Pin{Provider: "anthropic", Model: "m1", Version: "v1", PurposePolicy: "judging-v1"},
		VerifierPin: store.Pin{Provider: "anthropic", Model: "m2", Version: "v1", PurposePolicy: "judging-v1"}}
	if _, err := s.Enter(bg, c, c.Hash(), "op"); err != nil {
		t.Fatal(err)
	}
	for i, st := range []store.RunState{store.StateStopRequested, store.StateAuthorityRevoked, store.StateCleanupPending, store.StateQuiescent} {
		cause := "test"
		if i == 0 {
			cause = store.FinishCause
		}
		if _, err := s.Advance(bg, "p", 1, st, "op", cause); err != nil {
			t.Fatal(err)
		}
	}
	r := &rig{t: t, db: db.DB, s: s, l: s.FinishLedger("p", 1), subject: t.TempDir(), verifier: &scriptedVerifier{verdict: "ok", observed: verifierPin}}
	if err := os.WriteFile(filepath.Join(r.subject, "a.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	scratch := t.TempDir()
	r.h = &finish.Harness{Ledger: r.l, Verifier: r.verifier, Subject: r.subject, Area: filepath.Join(t.TempDir(), "area"),
		NewSandbox: func(context.Context, int64) (finish.Sandbox, func(), error) {
			return finish.Sandbox{Scratch: scratch, Place: func(*exec.Cmd) (func(), error) { r.placed++; return func() {}, nil }}, func() { r.ended++ }, nil
		}}
	for _, k := range crit {
		r.h.Specs = append(r.h.Specs, finish.Spec{Kind: k.Kind, Spec: k.Spec, Expected: k.Expected})
	}
	t.Cleanup(func() { finish.RemoveTree(r.h.Area) })
	return r
}

func (r *rig) advance() finish.Report {
	r.t.Helper()
	rep, err := r.h.Advance(bg)
	if err != nil {
		r.t.Fatalf("advance: %v", err)
	}
	return rep
}

func (r *rig) rows(kind string) int {
	var n int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM doomsday_audit WHERE profile='p' AND generation=1 AND kind=?`, kind).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

var twoMachine = []store.Criterion{{Kind: "command", Spec: "echo hello", Expected: "hello"}, {Kind: "file", Spec: "a.txt", Expected: "exists"}}

func TestFinishCommitsOnlyThroughFreezeEvidenceReviewAndRecheck(t *testing.T) { // T10
	r := newRig(t, twoMachine)
	rep := r.advance()
	if !rep.Done || rep.State != finish.StateFinished {
		t.Fatalf("%+v", rep)
	}
	if r.verifier.calls != 1 || r.rows("finish.commit") != 1 || r.rows("finish.freeze") != 1 || r.rows("finish.evidence") != 2 || r.rows("finish.recheck") != 1 {
		t.Fatalf("calls=%d commit=%d freeze=%d evidence=%d recheck=%d", r.verifier.calls, r.rows("finish.commit"), r.rows("finish.freeze"), r.rows("finish.evidence"), r.rows("finish.recheck"))
	}
	if r.placed != 1 || r.ended != 1 { // only the command needed a process; the unit of the round was ended
		t.Fatalf("placed=%d ended=%d", r.placed, r.ended)
	}
	if again := r.advance(); !again.Done || r.rows("finish.commit") != 1 {
		t.Fatalf("a repeat after the commit wrote another row: %+v", again)
	}
}

func TestAFailingCriterionParksAndNothingCanThenCommit(t *testing.T) { // T11
	r := newRig(t, []store.Criterion{{Kind: "command", Spec: "cat flag.txt", Expected: "ok"}})
	os.WriteFile(filepath.Join(r.subject, "flag.txt"), []byte("no"), 0o644)
	rep := r.advance()
	if rep.State != finish.StateParked || rep.Reason != finish.ReasonCriteriaFailed {
		t.Fatalf("%+v", rep)
	}
	if err := r.l.Commit(bg); !errors.Is(err, finish.ErrCommitRefused) && err == nil {
		t.Fatal("a parked finish committed")
	}
	if again := r.advance(); again.State != finish.StateParked || again.Done {
		t.Fatalf("%+v", again)
	}
	// the operator fixes the subject and starts another round: new snapshot, new freeze point, all criteria again
	os.WriteFile(filepath.Join(r.subject, "flag.txt"), []byte("ok"), 0o644)
	rep, err := r.h.Reverify(bg)
	if err != nil || !rep.Done || rep.Round != 2 {
		t.Fatalf("%+v %v", rep, err)
	}
	if r.rows("finish.freeze") != 2 || r.rows("finish.evidence") != 2 {
		t.Fatalf("freeze=%d evidence=%d", r.rows("finish.freeze"), r.rows("finish.evidence"))
	}
	if _, err := r.h.Reverify(bg); err == nil {
		t.Fatal("a finished finish was verified again")
	}
}

func TestAnUnrunnableCriterionIsAnErrorAndParks(t *testing.T) { // T26 class, test kind
	r := newRig(t, []store.Criterion{{Kind: "test", Spec: "go test ./...", Expected: "ok"}})
	rep := r.advance()
	if rep.State != finish.StateParked || rep.Reason != finish.ReasonCriterionError {
		t.Fatalf("%+v", rep)
	}
}

func TestRubricNeedsAPersonBoundToTheDossier(t *testing.T) { // section 10
	crit := append(append([]store.Criterion{}, twoMachine[:1]...), store.Criterion{Kind: "rubric", Spec: "the docs read well"})
	r := newRig(t, crit)
	rep := r.advance()
	if rep.State != finish.StateVerifying || rep.Done || len(rep.Waiting) != 1 || rep.Waiting[0] != "c1" {
		t.Fatalf("%+v", rep)
	}
	if again := r.advance(); again.Done { // no timeout and no repeat resolves a wait
		t.Fatal("the finish ended without the decision")
	}
	ds, err := r.h.Dossiers(bg)
	if err != nil || len(ds) != 1 || !strings.Contains(ds[0].Text, "the docs read well") {
		t.Fatalf("%+v %v", ds, err)
	}
	if err := r.h.Decide(bg, "c1", true, "deadbeef", ""); !errors.Is(err, finish.ErrRubric) {
		t.Fatalf("a decision on a dossier that was not shown: %v", err)
	}
	if err := r.h.Decide(bg, "c0", true, ds[0].Hash, ""); !errors.Is(err, finish.ErrRubric) {
		t.Fatalf("a machine criterion was decided by a person: %v", err)
	}
	if err := r.h.Decide(bg, "c1", true, ds[0].Hash[:12], "reads well"); err != nil {
		t.Fatal(err)
	}
	if err := r.h.Decide(bg, "c1", false, ds[0].Hash, ""); !errors.Is(err, finish.ErrRubric) {
		t.Fatalf("a second decision for one request: %v", err)
	}
	if r.rows("finish.commit") != 0 {
		t.Fatal("an approval finished by itself")
	}
	if rep := r.advance(); !rep.Done {
		t.Fatalf("%+v", rep)
	}
}

func TestRubricRejectParksAndASubjectChangeVoidsTheDecision(t *testing.T) {
	crit := []store.Criterion{{Kind: "rubric", Spec: "reads well"}, twoMachine[1]}
	r := newRig(t, crit)
	r.advance()
	ds, _ := r.h.Dossiers(bg)
	if err := r.h.Decide(bg, "c0", false, ds[0].Hash, "no"); err != nil {
		t.Fatal(err)
	}
	if rep := r.advance(); rep.State != finish.StateParked || rep.Reason != finish.ReasonRubricRejected {
		t.Fatalf("%+v", rep)
	}
	r2 := newRig(t, crit)
	r2.advance()
	ds, _ = r2.h.Dossiers(bg)
	os.WriteFile(filepath.Join(r2.subject, "a.txt"), []byte("changed after the dossier"), 0o644)
	if err := r2.h.Decide(bg, "c0", true, ds[0].Hash, ""); !errors.Is(err, finish.ErrRubric) {
		t.Fatalf("approved a version that is not the live one: %v", err)
	}
	if rep := r2.advance(); rep.State != finish.StateParked || rep.Reason != finish.ReasonSubjectChanged || r2.rows("finish.decision") != 0 {
		t.Fatalf("%+v decisions=%d", rep, r2.rows("finish.decision"))
	}
}

// T15: the subject changes between the last check and the finish.
func TestSubjectChangeAtEachPointOfTheRecheck(t *testing.T) {
	cases := []struct {
		point    string
		finished bool
	}{
		{"after-last-evidence", false},
		{"after-full-digest", false},
		{"after-recheck", true}, // the documented residue: after the last comparison and before the commit
	}
	for _, c := range cases {
		t.Run(c.point, func(t *testing.T) {
			r := newRig(t, twoMachine)
			r.h.Hook = func(p string) error {
				if p == c.point {
					return os.WriteFile(filepath.Join(r.subject, "b.txt"), []byte("late"), 0o644)
				}
				return nil
			}
			rep := r.advance()
			switch {
			case c.finished && (!rep.Done || rep.State != finish.StateFinished):
				t.Fatalf("the documented window did not finish: %+v", rep)
			case !c.finished && (rep.Done || rep.Reason != finish.ReasonSubjectChanged || !strings.Contains(rep.Detail, "b.txt")):
				t.Fatalf("%+v", rep)
			}
			if !c.finished && r.rows("finish.commit") != 0 {
				t.Fatal("committed")
			}
		})
	}
	// a change after the commit is not the finish's concern
	r := newRig(t, twoMachine)
	r.h.Hook = func(p string) error { return nil }
	r.advance()
	os.WriteFile(filepath.Join(r.subject, "after.txt"), []byte("x"), 0o644)
	if rep := r.advance(); !rep.Done {
		t.Fatalf("%+v", rep)
	}
}

func TestCrashAtEveryPointRestartsWithoutAFalseOrSecondCommit(t *testing.T) { // crash-before / during / at-commit
	for _, point := range []string{"freeze.snapshotted", "evidence.recorded", "after-last-evidence", "review.started", "review.called", "review.complete", "after-full-digest", "after-recheck", "commit"} {
		t.Run(point, func(t *testing.T) {
			r := newRig(t, twoMachine)
			crashed := false
			r.h.Hook = func(p string) error {
				if p == point && !crashed {
					crashed = true
					return errors.New("died here")
				}
				return nil
			}
			if _, err := r.h.Advance(bg); err == nil {
				t.Fatal("the crash point was not reached")
			}
			if r.rows("finish.commit") != 0 {
				t.Fatal("a crash before the commit left a commit")
			}
			rep, err := r.h.Advance(bg)
			if err != nil || !rep.Done {
				t.Fatalf("restart: %+v %v", rep, err)
			}
			if r.rows("finish.commit") != 1 || r.rows("finish.freeze") != 1 || r.rows("finish.evidence") != 2 {
				t.Fatalf("commit=%d freeze=%d evidence=%d", r.rows("finish.commit"), r.rows("finish.freeze"), r.rows("finish.evidence"))
			}
		})
	}
}

func TestTheVerifierCanOnlyBlockAndAChangedOrAbsentModelParks(t *testing.T) {
	cases := []struct {
		name   string
		set    func(v *scriptedVerifier)
		reason finish.Reason
	}{
		{"block", func(v *scriptedVerifier) { v.verdict = "block" }, finish.ReasonVerifierBlocked},
		{"other model", func(v *scriptedVerifier) { v.observed.Model = "m1" }, finish.ReasonVerifierChanged},
		{"unavailable", func(v *scriptedVerifier) { v.err = errors.New("down") }, finish.ReasonVerifierUnavailable},
		{"outside the enumeration", func(v *scriptedVerifier) { v.verdict = "approve" }, finish.ReasonVerifierUnavailable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, twoMachine)
			c.set(r.verifier)
			rep := r.advance()
			if rep.State != finish.StateParked || rep.Reason != c.reason || r.rows("finish.commit") != 0 {
				t.Fatalf("%+v", rep)
			}
		})
	}
}

func TestATamperedSnapshotParksAndNoConfinedEnvironmentMeansNoRun(t *testing.T) {
	r := newRig(t, twoMachine)
	first := true
	r.h.Hook = func(p string) error {
		if p == "evidence.recorded" && first {
			first = false
			return errors.New("died here")
		}
		return nil
	}
	if _, err := r.h.Advance(bg); err == nil {
		t.Fatal("no crash")
	}
	snap := filepath.Join(r.h.Area, "g1-r1", "snapshot")
	os.Chmod(snap, 0o700)
	os.Chmod(filepath.Join(snap, "a.txt"), 0o600)
	os.WriteFile(filepath.Join(snap, "a.txt"), []byte("tampered"), 0o600)
	if rep := r.advance(); rep.Reason != finish.ReasonTampered {
		t.Fatalf("%+v", rep)
	}

	r2 := newRig(t, twoMachine)
	r2.h.NewSandbox = nil
	if _, err := r2.h.Advance(bg); err == nil || r2.rows("finish.evidence") != 0 {
		t.Fatalf("criteria ran without a confined environment: %v", err)
	}
}

func TestSpecsThatDoNotMatchTheCharterRunNothing(t *testing.T) {
	r := newRig(t, twoMachine)
	r.h.Specs = r.h.Specs[:1] // a shape that does not match the charter is refused, not guessed
	if _, err := r.h.Advance(bg); err == nil || r.rows("finish.evidence") != 0 {
		t.Fatalf("specs that do not match the charter were used: %v", err)
	}
}
