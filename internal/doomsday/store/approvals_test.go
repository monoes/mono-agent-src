package store

import (
	"encoding/hex"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func hexHash(c Charter) string { h := c.Hash(); return hex.EncodeToString(h[:]) }

// approvalFix enters a run and returns a store plus the issue input for it.
func approvalFix(t *testing.T) (*SQLStore, Run, IssueInput) {
	t.Helper()
	s, _ := newStore(t)
	r := enter(t, s, "p")
	c, err := s.LoadCharter(ctx, "p", r.Generation)
	if err != nil {
		t.Fatal(err)
	}
	return s, r, IssueInput{Profile: "p", Generation: r.Generation, CharterHash: hexHash(c), Op: "send.third_party", ArgsHash: "h1", Caller: 7, Boss: "boss"}
}

func consume(s *SQLStore, a Approval, op, args string, charges ...Charge) error {
	return s.ConsumeApproval(ctx, a.Profile, a.Generation, a.ID, op, args, charges, AuditEntry{Kind: "ops." + op + ".approved", Payload: "{}"})
}

func TestApprovalIssueGetConsumeReplay(t *testing.T) {
	s, _, in := approvalFix(t)
	a, err := s.IssueApproval(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetApproval(ctx, a.ID)
	if err != nil || got.Op != "send.third_party" || got.ArgsHash != "h1" || got.Caller != 7 || got.Revoked || got.ExpiresSeq != got.IssuedSeq+DefaultApprovalTTLSeq {
		t.Fatalf("got %+v err %v", got, err)
	}
	if err := consume(s, a, "send.third_party", "h1", Charge{"sends", 1, 5}); err != nil {
		t.Fatal(err)
	}
	if sumOf(t, s, "sends") != 1 || sumOf(t, s, "approvals") != 1 {
		t.Fatal("charges not recorded")
	}
	before := auditCount(t, s)
	if err := consume(s, a, "send.third_party", "h1", Charge{"sends", 1, 5}); !errors.Is(err, ErrAlreadyConsumed) {
		t.Fatalf("replay: %v", err)
	}
	if sumOf(t, s, "sends") != 1 || auditCount(t, s) != before {
		t.Fatal("a replay changed state")
	}
}

func TestApprovalRescindBeforeAndAfterConsume(t *testing.T) {
	s, r, in := approvalFix(t)
	a, _ := s.IssueApproval(ctx, in)
	if err := s.RescindApproval(ctx, "p", r.Generation, a.ID, "boss"); err != nil {
		t.Fatal(err)
	}
	if err := s.RescindApproval(ctx, "p", r.Generation, a.ID, "boss"); !errors.Is(err, ErrApprovalRevoked) {
		t.Fatalf("second rescind: %v", err)
	}
	if g, _ := s.GetApproval(ctx, a.ID); !g.Revoked {
		t.Fatal("not shown as revoked")
	}
	if err := consume(s, a, "send.third_party", "h1", Charge{"sends", 1, 5}); !errors.Is(err, ErrApprovalRevoked) {
		t.Fatalf("consume of a rescinded approval: %v", err)
	}
	if sumOf(t, s, "sends") != 0 || sumOf(t, s, "approvals") != 0 {
		t.Fatal("a rescinded approval was charged")
	}
	b, _ := s.IssueApproval(ctx, in)
	if err := consume(s, b, "send.third_party", "h1"); err != nil {
		t.Fatal(err)
	}
	if err := s.RescindApproval(ctx, "p", r.Generation, b.ID, "boss"); !errors.Is(err, ErrAlreadyConsumed) {
		t.Fatalf("rescind after consume: %v", err)
	}
	if err := s.RescindApproval(ctx, "p", r.Generation, "ap-nope", "boss"); !errors.Is(err, ErrApprovalUnknown) {
		t.Fatalf("unknown: %v", err)
	}
}

func TestApprovalExpiresBySeqNotTime(t *testing.T) {
	s, r, in := approvalFix(t)
	in.TTLSeq = 2
	a, err := s.IssueApproval(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ { // the chain advances past expires_seq
		if _, err := s.AppendAudit(ctx, "p", r.Generation, "note", "x"); err != nil {
			t.Fatal(err)
		}
	}
	if seq, _ := s.ApprovalSeq(ctx, "p"); seq <= a.ExpiresSeq {
		t.Fatalf("setup: seq %d expires %d", seq, a.ExpiresSeq)
	}
	if err := consume(s, a, "send.third_party", "h1", Charge{"sends", 1, 5}); !errors.Is(err, ErrApprovalExpired) {
		t.Fatalf("expired: %v", err)
	}
	if sumOf(t, s, "sends") != 0 || sumOf(t, s, "approvals") != 0 {
		t.Fatal("an expired approval was charged")
	}
}

func TestApprovalBoundToOperationAndEffect(t *testing.T) {
	s, _, in := approvalFix(t)
	a, _ := s.IssueApproval(ctx, in)
	if err := consume(s, a, "vault.use", "h1"); !errors.Is(err, ErrApprovalMismatch) {
		t.Fatalf("another operation: %v", err)
	}
	if err := consume(s, a, "send.third_party", "h2"); !errors.Is(err, ErrApprovalMismatch) {
		t.Fatalf("another effect: %v", err)
	}
	if sumOf(t, s, "approvals") != 0 {
		t.Fatal("charged")
	}
	if err := consume(s, Approval{Profile: "p", Generation: a.Generation, ID: "ap-nope"}, "send.third_party", "h1"); !errors.Is(err, ErrApprovalUnknown) {
		t.Fatalf("unknown: %v", err)
	}
	if err := consume(s, a, "send.third_party", "h1"); err != nil {
		t.Fatalf("the right effect still works after refusals: %v", err)
	}
}

func TestApprovalIssueRefusals(t *testing.T) {
	s, r, in := approvalFix(t)
	bad := in
	bad.CharterHash = "00"
	if _, err := s.IssueApproval(ctx, bad); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("hash: %v", err)
	}
	for name, f := range map[string]func(*IssueInput){"op": func(i *IssueInput) { i.Op = "" }, "args": func(i *IssueInput) { i.ArgsHash = "" },
		"caller": func(i *IssueInput) { i.Caller = 0 }, "ttl": func(i *IssueInput) { i.TTLSeq = MaxApprovalTTLSeq + 1 }} {
		x := in
		f(&x)
		if _, err := s.IssueApproval(ctx, x); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	walk(t, s, r, StateStopRequested)
	var ref *Refusal
	if _, err := s.IssueApproval(ctx, in); !errors.As(err, &ref) {
		t.Fatalf("issue on a stopping run: %v", err)
	}
}

func TestApprovalConsumeRefusedOnceStopped(t *testing.T) {
	s, r, in := approvalFix(t)
	a, _ := s.IssueApproval(ctx, in)
	walk(t, s, r, StateStopRequested)
	var ref *Refusal
	if err := consume(s, a, "send.third_party", "h1", Charge{"sends", 1, 5}); !errors.As(err, &ref) {
		t.Fatalf("consume after stop: %v", err)
	}
	if sumOf(t, s, "approvals") != 0 {
		t.Fatal("charged after stop")
	}
}

func TestApprovalOverLimitLeavesApprovalUnspent(t *testing.T) {
	s, _, in := approvalFix(t)
	a, _ := s.IssueApproval(ctx, in)
	var over *OverLimitError
	if err := consume(s, a, "send.third_party", "h1", Charge{"sends", 6, 5}); !errors.As(err, &over) || over.Kinds[0] != "sends" {
		t.Fatalf("over: %v", err)
	}
	if sumOf(t, s, "approvals") != 0 || sumOf(t, s, "sends") != 0 {
		t.Fatal("a refused consume spent the approval unit")
	}
	if err := consume(s, a, "send.third_party", "h1", Charge{"sends", 1, 5}); err != nil {
		t.Fatalf("the approval was spent by a refusal: %v", err)
	}
}

func TestApprovalConsumeAuditFailureRollsBack(t *testing.T) {
	s, _, in := approvalFix(t)
	a, _ := s.IssueApproval(ctx, in)
	if _, err := s.db.Exec(`CREATE TRIGGER t_fail BEFORE INSERT ON doomsday_audit WHEN NEW.kind = 'ops.send.third_party.approved' BEGIN SELECT RAISE(ABORT, 'no'); END`); err != nil {
		t.Fatal(err)
	}
	if err := consume(s, a, "send.third_party", "h1", Charge{"sends", 1, 5}); err == nil {
		t.Fatal("audit failure accepted")
	}
	if sumOf(t, s, "approvals") != 0 || sumOf(t, s, "sends") != 0 {
		t.Fatal("a consume without its audit row left charges")
	}
	_, _ = s.db.Exec(`DROP TRIGGER t_fail`)
	if err := consume(s, a, "send.third_party", "h1", Charge{"sends", 1, 5}); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

func TestApprovalOneWinnerUnderContention(t *testing.T) {
	s, _, in := approvalFix(t)
	a, _ := s.IssueApproval(ctx, in)
	var wins, replays atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch err := consume(s, a, "send.third_party", "h1", Charge{"sends", 1, 100}); {
			case err == nil:
				wins.Add(1)
			case errors.Is(err, ErrAlreadyConsumed):
				replays.Add(1)
			default:
				t.Errorf("unexpected: %v", err)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 || sumOf(t, s, "sends") != 1 {
		t.Fatalf("wins %d sends %d", wins.Load(), sumOf(t, s, "sends"))
	}
}

func TestReservedKindsCannotBeForged(t *testing.T) {
	s, r, _ := approvalFix(t)
	for _, k := range []string{"approval.issue", "approval.rescind", "finish.commit", "finish.park", "finish.freeze"} {
		if _, err := s.AppendAudit(ctx, "p", r.Generation, k, `{"id":"x"}`); !errors.Is(err, ErrReservedKind) {
			t.Errorf("AppendAudit(%s): %v", k, err)
		}
		if err := s.ConsumeMany(ctx, "p", r.Generation, "x-"+k, []Charge{{"k", 1, 5}}, AuditEntry{Kind: k, Payload: "{}"}); !errors.Is(err, ErrReservedKind) {
			t.Errorf("ConsumeMany(%s): %v", k, err)
		}
	}
	if sumOf(t, s, "k") != 0 {
		t.Fatal("charged")
	}
}

func TestApprovalRowTamperIsUnverifiable(t *testing.T) {
	s, _, in := approvalFix(t)
	a, _ := s.IssueApproval(ctx, in)
	_, _ = s.db.Exec(`DROP TRIGGER doomsday_audit_no_update`)
	if _, err := s.db.Exec(`UPDATE doomsday_audit SET payload = replace(payload, '"caller":7', '"caller":8') WHERE kind = 'approval.issue'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetApproval(ctx, a.ID); !errors.Is(err, ErrUnverifiable) {
		t.Fatalf("tampered approval read: %v", err)
	}
}

func TestWorkAdmittingKindMatchesSQL(t *testing.T) {
	s, _ := newStore(t)
	kinds := []string{"counter.consume", "approval.issue", "approval.consume", "ops.plan.approve.approved", "ops.vault.use.approved", "ops.vault.use.refused:approval",
		"ops.approved", "run.advance", "start", "finish.commit", "approved", "xops.a.approved"}
	for _, k := range kinds {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM (SELECT ? AS kind) WHERE `+workAdmittingSQL, k).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if (n == 1) != WorkAdmittingKind(k) {
			t.Errorf("%q: sql %v go %v", k, n == 1, WorkAdmittingKind(k))
		}
	}
}
