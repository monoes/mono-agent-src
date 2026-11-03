package store

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
)

var errDied = errors.New("process died here")

// finFix is a run that stopped with cause=finish and is quiescent, over the real SQLite store.
type finFix struct {
	s *SQLStore
	l *FinishLedger
	c Charter
	r Run
}

type okVerifier struct {
	calls atomic.Int32
	pin   finish.Pin
	say   string
}

func (v *okVerifier) Review(context.Context, finish.ReviewInput) (finish.ReviewResult, error) {
	v.calls.Add(1)
	return finish.ReviewResult{Observed: v.pin, Verdict: v.say, ReasonCode: "fine"}, nil
}

func finCharter() Charter {
	c := charter("p", 1)
	c.Criteria = []Criterion{{Kind: "command", Spec: "go build", Expected: "0"}, {Kind: "test", Spec: "go test", Expected: "ok"}}
	return c
}

func newFin(t *testing.T) *finFix { return newFinWith(t, finCharter()) }

func newFinWith(t *testing.T, c Charter) *finFix {
	t.Helper()
	s, _ := newStore(t)
	r, err := s.Enter(ctx, c, c.Hash(), "op")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Advance(ctx, "p", 1, StateStopRequested, "op", FinishCause); err != nil {
		t.Fatal(err)
	}
	walk(t, s, r, StateAuthorityRevoked, StateCleanupPending, StateQuiescent)
	return &finFix{s: s, l: s.FinishLedger("p", 1), c: c, r: r}
}

func (f *finFix) verifier(say string) *okVerifier {
	return &okVerifier{pin: finishPin(f.c.VerifierPin), say: say}
}

func (f *finFix) facts(t *testing.T) finish.Facts {
	t.Helper()
	fa, err := f.l.Facts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return fa
}

func (f *finFix) freeze(t *testing.T, digest string) finish.Round {
	t.Helper()
	if _, err := f.l.Freeze(ctx, digest, "snap"); err != nil {
		t.Fatal(err)
	}
	return f.facts(t).Round
}

func (f *finFix) evidence(t *testing.T, idx int, result string) {
	t.Helper()
	rd := f.facts(t).Round
	if err := f.l.RecordEvidence(ctx, finish.Evidence{Round: rd.N, FreezeSeq: rd.FreezeSeq, Digest: rd.Digest, CriterionID: CriterionID(idx),
		Result: result, Verifier: finishPin(f.c.VerifierPin), StdoutDigest: "o", StderrDigest: "e"}); err != nil {
		t.Fatal(err)
	}
}

// ready records passing evidence for both criteria and runs the finisher until only the recheck is missing.
func (f *finFix) ready(t *testing.T, v *okVerifier) finish.Finisher {
	t.Helper()
	f.evidence(t, 0, "pass")
	f.evidence(t, 1, "pass")
	fn := finish.Finisher{Ledger: f.l, Verifier: v}
	if vd, err := fn.Step(ctx); err != nil || vd.Reason != finish.WaitRecheck {
		t.Fatalf("expected the recheck wait: %+v %v", vd, err)
	}
	return fn
}

func (f *finFix) count(t *testing.T, kind string) int {
	t.Helper()
	var n int
	if err := f.s.db.QueryRow(`SELECT COUNT(*) FROM doomsday_audit WHERE kind = ?`, kind).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestFinishCommitsOnlyAfterEveryConditionHolds(t *testing.T) {
	f := newFin(t)
	if st := f.facts(t).State; st != "quiescent" {
		t.Fatalf("before the freeze the state is %s", st)
	}
	f.freeze(t, "d1")
	if st := f.facts(t).State; st != finish.StateVerifying {
		t.Fatal(st)
	}
	v := f.verifier(finish.VerdictOK)
	fn := f.ready(t, v)
	if err := f.l.Commit(ctx); !errors.Is(err, finish.ErrCommitRefused) {
		t.Fatalf("a commit without the recheck: %v", err)
	}
	if err := f.l.RecordRecheck(ctx, true); err != nil {
		t.Fatal(err)
	}
	vd, err := fn.Step(ctx)
	if err != nil || vd.Outcome != finish.Commit {
		t.Fatalf("%+v %v", vd, err)
	}
	if f.facts(t).State != finish.StateFinished || f.count(t, "finish.commit") != 1 || v.calls.Load() != 1 {
		t.Fatalf("state %s commits %d verifier calls %d", f.facts(t).State, f.count(t, "finish.commit"), v.calls.Load())
	}
	if vd, err := fn.Step(ctx); err != nil || vd.Outcome != finish.Commit || f.count(t, "finish.commit") != 1 {
		t.Fatalf("a repeat step wrote a second commit: %+v %v", vd, err)
	}
	if err := f.s.VerifyAudit(ctx, "p", 1); err != nil {
		t.Fatal(err)
	}
	if r, _ := f.s.GetRun(ctx, "p", 1); r.State != StateQuiescent {
		t.Fatal("the run row changed")
	}
}

func TestFinishRefusalsWriteNothing(t *testing.T) {
	cases := map[string]func(t *testing.T, f *finFix){
		"not frozen": func(t *testing.T, f *finFix) {},
		"missing evidence": func(t *testing.T, f *finFix) {
			f.freeze(t, "d")
			f.evidence(t, 0, "pass")
			_ = f.l.RecordRecheck(ctx, true)
		},
		"failed criterion": func(t *testing.T, f *finFix) {
			f.freeze(t, "d")
			f.evidence(t, 0, "pass")
			f.evidence(t, 1, "fail")
			_ = f.l.RecordRecheck(ctx, true)
		},
		"no review": func(t *testing.T, f *finFix) {
			f.freeze(t, "d")
			f.evidence(t, 0, "pass")
			f.evidence(t, 1, "pass")
			_ = f.l.RecordRecheck(ctx, true)
		},
		"blocking review": func(t *testing.T, f *finFix) {
			f.freeze(t, "d")
			f.evidence(t, 0, "pass")
			f.evidence(t, 1, "pass")
			if vd, _ := (finish.Finisher{Ledger: f.l, Verifier: f.verifier(finish.VerdictBlock)}).Step(ctx); vd.Reason != finish.ReasonVerifierBlocked {
				t.Fatalf("%+v", vd)
			}
			_ = f.l.RecordRecheck(ctx, true)
		},
		"recheck failed after passing": func(t *testing.T, f *finFix) {
			f.freeze(t, "d")
			f.ready(t, f.verifier(finish.VerdictOK))
			_ = f.l.RecordRecheck(ctx, true)
			_ = f.l.RecordRecheck(ctx, false)
		},
		"work after the close point": func(t *testing.T, f *finFix) {
			f.freeze(t, "d")
			f.ready(t, f.verifier(finish.VerdictOK))
			_ = f.l.RecordRecheck(ctx, true)
			if _, err := f.s.AppendAudit(ctx, "p", 1, "ops.plan.approve.approved", "{}"); err != nil {
				t.Fatal(err)
			}
		},
		"review by another model": func(t *testing.T, f *finFix) {
			f.freeze(t, "d")
			v := f.verifier(finish.VerdictOK)
			v.pin.Version = "other"
			f.evidence(t, 0, "pass")
			f.evidence(t, 1, "pass")
			_, _ = finish.Finisher{Ledger: f.l, Verifier: v}.Step(ctx)
			_ = f.l.RecordRecheck(ctx, true)
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFin(t)
			setup(t, f)
			if err := f.l.Commit(ctx); !errors.Is(err, finish.ErrCommitRefused) {
				t.Fatalf("commit: %v", err)
			}
			if f.count(t, "finish.commit") != 0 {
				t.Fatal("a commit row exists")
			}
		})
	}
}

func TestFinishCrashBeforeDuringAndAtTheCommit(t *testing.T) {
	t.Run("before", func(t *testing.T) {
		f := newFin(t)
		f.freeze(t, "d")
		fn := f.ready(t, f.verifier(finish.VerdictOK))
		_ = f.l.RecordRecheck(ctx, true)
		fn.Hook = func(p string) error {
			if p == "commit" {
				return errDied
			}
			return nil
		}
		if _, err := fn.Step(ctx); !errors.Is(err, errDied) || f.count(t, "finish.commit") != 0 {
			t.Fatalf("%v commits %d", err, f.count(t, "finish.commit"))
		}
		fn.Hook = nil
		if vd, err := fn.Step(ctx); err != nil || vd.Outcome != finish.Commit || f.count(t, "finish.commit") != 1 {
			t.Fatalf("recovery: %+v %v", vd, err)
		}
	})
	t.Run("during", func(t *testing.T) {
		f := newFin(t)
		f.freeze(t, "d")
		fn := f.ready(t, f.verifier(finish.VerdictOK))
		_ = f.l.RecordRecheck(ctx, true)
		f.l.Hook = func(p string) error {
			if p == "commit.inserted" {
				return errDied // the row is inserted, the transaction has not committed
			}
			return nil
		}
		if _, err := fn.Step(ctx); !errors.Is(err, errDied) || f.count(t, "finish.commit") != 0 {
			t.Fatalf("%v commits %d", err, f.count(t, "finish.commit"))
		}
		if st := f.facts(t).State; st != finish.StateVerifying {
			t.Fatalf("a rolled-back commit left state %s", st)
		}
		if err := f.s.VerifyAudit(ctx, "p", 1); err != nil {
			t.Fatal(err)
		}
		f.l.Hook = nil
		if vd, err := fn.Step(ctx); err != nil || vd.Outcome != finish.Commit || f.count(t, "finish.commit") != 1 {
			t.Fatalf("recovery: %+v %v", vd, err)
		}
	})
	t.Run("at", func(t *testing.T) {
		f := newFin(t)
		f.freeze(t, "d")
		fn := f.ready(t, f.verifier(finish.VerdictOK))
		_ = f.l.RecordRecheck(ctx, true)
		fn.Ledger = lostAnswer{f.l}
		if _, err := fn.Step(ctx); !errors.Is(err, errDied) || f.count(t, "finish.commit") != 1 {
			t.Fatalf("%v commits %d", err, f.count(t, "finish.commit"))
		}
		fn.Ledger = f.l
		if vd, err := fn.Step(ctx); err != nil || vd.Outcome != finish.Commit || f.count(t, "finish.commit") != 1 || f.count(t, "finish.park") != 0 {
			t.Fatalf("a repeat after the commit: %+v %v commits %d parks %d", vd, err, f.count(t, "finish.commit"), f.count(t, "finish.park"))
		}
	})
}

// lostAnswer commits for real and then reports that the process died before it saw the answer.
type lostAnswer struct{ *FinishLedger }

func (l lostAnswer) Commit(ctx context.Context) error {
	if err := l.FinishLedger.Commit(ctx); err != nil {
		return err
	}
	return errDied
}

func TestFinishConcurrentCommitsWriteOneRow(t *testing.T) {
	f := newFin(t)
	f.freeze(t, "d")
	f.ready(t, f.verifier(finish.VerdictOK))
	_ = f.l.RecordRecheck(ctx, true)
	var wg sync.WaitGroup
	var bad atomic.Int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := f.s.FinishLedger("p", 1).Commit(ctx); err != nil && !errors.Is(err, finish.ErrCommitRefused) {
				bad.Add(1)
			}
		}()
	}
	wg.Wait()
	if f.count(t, "finish.commit") != 1 || bad.Load() != 0 {
		t.Fatalf("commits %d, unexpected errors %d", f.count(t, "finish.commit"), bad.Load())
	}
}

func TestFinishRoundTwoNeedsFreshEvidenceAndAFreshReview(t *testing.T) {
	f := newFin(t)
	f.freeze(t, "d1")
	f.evidence(t, 0, "pass")
	f.evidence(t, 1, "fail")
	v := f.verifier(finish.VerdictOK)
	fn := finish.Finisher{Ledger: f.l, Verifier: v}
	if vd, err := fn.Step(ctx); err != nil || vd.Outcome != finish.Park || vd.Reason != finish.ReasonCriteriaFailed {
		t.Fatalf("%+v %v", vd, err)
	}
	if st := f.facts(t).State; st != finish.StateParked {
		t.Fatal(st)
	}
	if vd, _ := fn.Step(ctx); vd.Reason != finish.ReasonCriteriaFailed || f.count(t, "finish.park") != 1 {
		t.Fatalf("a repeat step on a parked finish: %+v parks %d", vd, f.count(t, "finish.park"))
	}
	if round, err := f.l.Freeze(ctx, "d2", "snap2"); err != nil || round != 2 {
		t.Fatalf("%d %v", round, err)
	}
	if st := f.facts(t).State; st != finish.StateVerifying {
		t.Fatal(st)
	}
	f.evidence(t, 0, "pass")
	f.evidence(t, 1, "pass")
	if vd, err := fn.Step(ctx); err != nil || vd.Reason != finish.WaitRecheck {
		t.Fatalf("%+v %v", vd, err)
	}
	if v.calls.Load() != 2 {
		t.Fatalf("round 2 reused a review of round 1: %d verifier calls", v.calls.Load())
	}
	_ = f.l.RecordRecheck(ctx, true)
	if vd, err := fn.Step(ctx); err != nil || vd.Outcome != finish.Commit {
		t.Fatalf("%+v %v", vd, err)
	}
	if _, err := f.l.Freeze(ctx, "d3", ""); !errors.Is(err, ErrFinishState) {
		t.Fatalf("freeze after finishing: %v", err)
	}
}

func TestFinishRoundsAreBounded(t *testing.T) {
	f := newFin(t)
	for i := 1; i <= MaxFinishRounds; i++ {
		f.freeze(t, "d")
		if err := f.l.Park(ctx, finish.Verdict{Outcome: finish.Park, Reason: finish.ReasonSubjectChanged}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.l.Freeze(ctx, "d", ""); !errors.Is(err, ErrFinishState) {
		t.Fatalf("round %d: %v", MaxFinishRounds+1, err)
	}
}

func TestFinishFreezeNeedsAFinishingQuiescentRun(t *testing.T) {
	s, _ := newStore(t)
	enter(t, s, "p")
	if _, err := s.FinishLedger("p", 1).Freeze(ctx, "d", ""); !errors.Is(err, ErrFinishState) {
		t.Fatalf("a running run: %v", err)
	}
	walk(t, s, Run{Profile: "p", Generation: 1}, StateStopRequested, StateAuthorityRevoked, StateCleanupPending, StateQuiescent)
	if _, err := s.FinishLedger("p", 1).Freeze(ctx, "d", ""); !errors.Is(err, ErrFinishState) {
		t.Fatalf("an ordinary stop is not a finish: %v", err)
	}
	g := newFin(t)
	g.freeze(t, "d")
	if _, err := g.l.Freeze(ctx, "d", ""); !errors.Is(err, ErrFinishState) {
		t.Fatalf("a second freeze without a park: %v", err)
	}
}

func TestFinishRowsAreBoundByTheStore(t *testing.T) {
	f := newFin(t)
	ev := finish.Evidence{CriterionID: "c0", Result: "pass", Verifier: finishPin(f.c.VerifierPin)}
	if err := f.l.RecordEvidence(ctx, ev); !errors.Is(err, ErrFinishState) {
		t.Fatalf("evidence before the freeze: %v", err)
	}
	rd := f.freeze(t, "d")
	bound := func(e finish.Evidence) finish.Evidence {
		e.Round, e.FreezeSeq, e.Digest = rd.N, rd.FreezeSeq, rd.Digest
		return e
	}
	for name, e := range map[string]finish.Evidence{
		"another round":       {Round: 9, FreezeSeq: rd.FreezeSeq, Digest: rd.Digest, CriterionID: "c0", Result: "pass"},
		"another freeze":      {Round: rd.N, FreezeSeq: rd.FreezeSeq + 1, Digest: rd.Digest, CriterionID: "c0", Result: "pass"},
		"another digest":      {Round: rd.N, FreezeSeq: rd.FreezeSeq, Digest: "x", CriterionID: "c0", Result: "pass"},
		"unknown criterion":   bound(finish.Evidence{CriterionID: "c9", Result: "pass"}),
		"result off the list": bound(finish.Evidence{CriterionID: "c0", Result: "great"}),
	} {
		if err := f.l.RecordEvidence(ctx, e); !errors.Is(err, ErrFinishRow) {
			t.Errorf("%s: %v", name, err)
		}
	}
	forged := bound(finish.Evidence{CriterionID: "c0", Result: "pass", Generation: 77, CharterHash: "forged", CriterionHash: "forged", Seq: 5, Verifier: finishPin(f.c.VerifierPin)})
	if err := f.l.RecordEvidence(ctx, forged); err != nil {
		t.Fatal(err)
	}
	got := f.facts(t).Evidence[0]
	if got.Generation != 1 || got.CharterHash != f.facts(t).Charter.Hash || got.CriterionHash != CriterionHash(f.c.Criteria[0]) || got.Seq == 5 {
		t.Fatalf("the caller's binding survived: %+v", got)
	}
	if err := f.l.RecordEvidence(ctx, bound(finish.Evidence{CriterionID: "c0", Result: "pass"})); !errors.Is(err, ErrFinishRow) {
		t.Fatalf("a second row for one criterion: %v", err)
	}
}

func TestFinishReviewRowsAndDecisionsAreChecked(t *testing.T) {
	f := newFin(t)
	rd := f.freeze(t, "d")
	rv := func(state, verdict string) finish.Review {
		return finish.Review{Round: rd.N, FreezeSeq: rd.FreezeSeq, Digest: rd.Digest, State: state, Verdict: verdict}
	}
	if err := f.l.RecordReview(ctx, rv(finish.ReviewComplete, finish.VerdictOK)); !errors.Is(err, ErrFinishRow) {
		t.Fatalf("a complete review with no started row: %v", err)
	}
	if err := f.l.RecordReview(ctx, rv("pending", "")); !errors.Is(err, ErrFinishRow) {
		t.Fatalf("a state off the list: %v", err)
	}
	if err := f.l.RecordReview(ctx, rv(finish.ReviewStarted, "")); err != nil {
		t.Fatal(err)
	}
	if err := f.l.RecordReview(ctx, rv(finish.ReviewComplete, "maybe")); !errors.Is(err, ErrFinishRow) {
		t.Fatalf("a verdict outside the closed enumeration: %v", err)
	}
	stale := rv(finish.ReviewComplete, finish.VerdictOK)
	stale.FreezeSeq++
	if err := f.l.RecordReview(ctx, stale); !errors.Is(err, ErrFinishRow) {
		t.Fatalf("a review of another freeze: %v", err)
	}
	if err := f.l.RecordDecision(ctx, finish.RubricDecision{Round: rd.N, FreezeSeq: rd.FreezeSeq, Digest: rd.Digest, CriterionID: "c0", Approve: true}); !errors.Is(err, ErrFinishRow) {
		t.Fatalf("a rubric decision on a machine criterion: %v", err)
	}
}

func TestFinishAbortAndParkAreFinal(t *testing.T) {
	f := newFin(t)
	f.freeze(t, "d")
	f.ready(t, f.verifier(finish.VerdictOK))
	_ = f.l.RecordRecheck(ctx, true)
	if err := f.l.Park(ctx, finish.Verdict{Outcome: finish.Park, Reason: "because"}); !errors.Is(err, ErrFinishRow) {
		t.Fatalf("a reason outside the closed list: %v", err)
	}
	if err := f.l.Park(ctx, finish.Verdict{Outcome: finish.Wait, Reason: finish.ReasonInternalError}); !errors.Is(err, ErrFinishRow) {
		t.Fatalf("a wait is not a park: %v", err)
	}
	if err := f.l.Abort(ctx, "operator"); err != nil {
		t.Fatal(err)
	}
	if f.facts(t).State != StateStopped {
		t.Fatal(f.facts(t).State)
	}
	if err := f.l.Commit(ctx); !errors.Is(err, finish.ErrCommitRefused) {
		t.Fatalf("a commit after the operator stop: %v", err)
	}
	if err := f.l.Park(ctx, finish.Verdict{Outcome: finish.Park, Reason: finish.ReasonInternalError}); !errors.Is(err, ErrFinishState) {
		t.Fatalf("a park after the operator stop: %v", err)
	}
	g := newFin(t)
	g.freeze(t, "d")
	g.ready(t, g.verifier(finish.VerdictOK))
	_ = g.l.RecordRecheck(ctx, true)
	if err := g.l.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := g.l.Park(ctx, finish.Verdict{Outcome: finish.Park, Reason: finish.ReasonInternalError}); !errors.Is(err, ErrFinishState) {
		t.Fatalf("a park after the commit: %v", err)
	}
	if err := g.l.Abort(ctx, "operator"); !errors.Is(err, ErrFinishState) {
		t.Fatalf("an abort after the commit: %v", err)
	}
}

func TestFinishAdvisoryEvidenceNeverCounts(t *testing.T) {
	s, _ := newStore(t)
	c := finCharter()
	if _, err := s.Enter(ctx, c, c.Hash(), "op"); err != nil {
		t.Fatal(err)
	}
	l := s.FinishLedger("p", 1)
	if err := l.RecordEvidence(ctx, finish.Evidence{Advisory: true, CriterionID: "c0", Result: "pass", Round: 4, FreezeSeq: 4, Digest: "x"}); err != nil {
		t.Fatalf("pre-flight while running: %v", err)
	}
	fa, _ := l.Facts(ctx)
	if len(fa.Evidence) != 1 || !fa.Evidence[0].Advisory || fa.Evidence[0].Round != 0 || fa.State != "running" {
		t.Fatalf("%+v", fa)
	}
	walk(t, s, Run{Profile: "p", Generation: 1}, StateStopRequested)
	if err := l.RecordEvidence(ctx, finish.Evidence{Advisory: true, CriterionID: "c0", Result: "pass"}); !errors.Is(err, ErrFinishRow) {
		t.Fatalf("advisory evidence after the stop: %v", err)
	}
}

func TestFinishFactsFailClosedOnATamperedRow(t *testing.T) {
	f := newFin(t)
	f.freeze(t, "d")
	f.evidence(t, 0, "fail")
	_, _ = f.s.db.Exec(`DROP TRIGGER doomsday_audit_no_update`)
	if _, err := f.s.db.Exec(`UPDATE doomsday_audit SET payload = replace(payload, '"fail"', '"pass"') WHERE kind = 'finish.evidence'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.l.Facts(ctx); !errors.Is(err, ErrUnverifiable) {
		t.Fatalf("facts over a tampered chain: %v", err)
	}
	if err := f.l.Commit(ctx); !errors.Is(err, ErrUnverifiable) || f.count(t, "finish.commit") != 0 {
		t.Fatalf("commit over a tampered chain: %v", err)
	}
}

func TestFinishCharterThatCanNeverFinishParksAtTheCommit(t *testing.T) {
	c := finCharter()
	c.VerifierPin = c.BossPin
	c.VerifierPin.PurposePolicy = "other-policy" // a distinct pin (Validate passes) but the same model: it can never finish (Q3)
	f := newFinWith(t, c)
	f.freeze(t, "d")
	f.evidence(t, 0, "pass")
	f.evidence(t, 1, "pass")
	_ = f.l.RecordRecheck(ctx, true)
	v := f.verifier(finish.VerdictOK)
	vd, err := finish.Finisher{Ledger: f.l, Verifier: v}.Step(ctx)
	if err != nil || vd.Outcome != finish.Park || v.calls.Load() != 0 || f.count(t, "finish.commit") != 0 || f.facts(t).State != finish.StateParked {
		t.Fatalf("%+v %v calls %d", vd, err, v.calls.Load())
	}
}
