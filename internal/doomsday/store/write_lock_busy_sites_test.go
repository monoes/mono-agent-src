package store

import (
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
)

// The sites of #1460 batch 2: each reads the chain or the run first and writes after, so under a deferred BEGIN a
// writer that commits in between fails the write with SQLITE_BUSY_SNAPSHOT (517). onWrapped fires the writer before
// the first statement the store executes (the first write, after the reads).

func TestIssueApprovalSurvivesAWriterCommittingBetweenItsReadAndItsWrite(t *testing.T) {
	s, _, in := approvalFix(t)
	w, fired := onWrapped(t, s)
	if _, err := w.IssueApproval(ctx, in); err != nil {
		t.Fatalf("IssueApproval failed when a writer committed between its read and its write: %v", err)
	}
	if !fired() {
		t.Fatal("the hook never ran")
	}
}

func TestRescindApprovalSurvivesAWriterCommittingBetweenItsReadAndItsWrite(t *testing.T) {
	s, r, in := approvalFix(t)
	a, err := s.IssueApproval(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	w, fired := onWrapped(t, s)
	if err := w.RescindApproval(ctx, "p", r.Generation, a.ID, "boss"); err != nil {
		t.Fatalf("RescindApproval failed when a writer committed between its read and its write: %v", err)
	}
	if !fired() {
		t.Fatal("the hook never ran")
	}
}

// The consume paths start with their conditional insert: no read comes before the first write, so there is no
// snapshot for a writer to invalidate and the deferred BEGIN was already safe. They are left as they were.
func TestConsumeApprovalAndManyStayWriteFirst(t *testing.T) {
	s, _, in := approvalFix(t)
	a, err := s.IssueApproval(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	w, fired := onWrapped(t, s)
	if err := consume(w, a, "send.third_party", "h1", Charge{"sends", 1, 5}); err != nil {
		t.Fatalf("ConsumeApproval: %v", err)
	}
	if !fired() {
		t.Fatal("the hook never ran")
	}
	w2, fired2 := onWrapped(t, s)
	if err := w2.ConsumeMany(ctx, "p", a.Generation, "m1", []Charge{{"sends", 1, 5}}, AuditEntry{Kind: "note", Payload: "{}"}); err != nil {
		t.Fatalf("ConsumeMany: %v", err)
	}
	if !fired2() {
		t.Fatal("the hook never ran")
	}
}

func TestAdvisoryEvidenceSurvivesAWriterCommittingBetweenItsReadAndItsWrite(t *testing.T) {
	s, _ := newStore(t)
	enter(t, s, "p")
	w, fired := onWrapped(t, s)
	if err := w.FinishLedger("p", 1).RecordEvidence(ctx, finish.Evidence{Advisory: true, CriterionID: CriterionID(0), Result: "pass"}); err != nil {
		t.Fatalf("advisory evidence failed when a writer committed between its read and its write: %v", err)
	}
	if !fired() {
		t.Fatal("the hook never ran")
	}
}

func TestFinishWriteSurvivesAWriterCommittingBetweenItsReadAndItsWrite(t *testing.T) {
	f := newFin(t)
	f.freeze(t, "d")
	w, fired := onWrapped(t, f.s)
	if err := w.FinishLedger("p", 1).RecordRecheck(ctx, true); err != nil {
		t.Fatalf("RecordRecheck failed when a writer committed between its read and its write: %v", err)
	}
	if !fired() {
		t.Fatal("the hook never ran")
	}
}

func TestFinishCommitSurvivesAWriterCommittingBetweenItsReadAndItsWrite(t *testing.T) {
	f := newFin(t)
	f.commitReady(t)
	w, fired := onWrapped(t, f.s)
	if err := w.FinishLedger("p", 1).Commit(ctx); err != nil {
		t.Fatalf("Commit failed when a writer committed between its read and its write: %v", err)
	}
	if !fired() {
		t.Fatal("the hook never ran")
	}
}

func TestFinishParkSurvivesAWriterCommittingBetweenItsReadAndItsWrite(t *testing.T) {
	f := newFin(t)
	f.freeze(t, "d")
	w, fired := onWrapped(t, f.s)
	err := w.FinishLedger("p", 1).Park(ctx, finish.Verdict{Outcome: finish.Park, Reason: finish.ReasonCriteriaFailed})
	if err != nil {
		t.Fatalf("Park failed when a writer committed between its read and its write: %v", err)
	}
	if !fired() {
		t.Fatal("the hook never ran")
	}
}
