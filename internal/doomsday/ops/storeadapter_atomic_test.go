package ops

import (
	"errors"
	"testing"
)

// The atomic path: the store's consume checks the approval row itself, so a rescind, an expiry or a mismatch that lands
// after the enforcer's own read is still refused, and a typed operation cannot be consumed without an issued row.
func TestAdapterConsumeChecksTheApprovalRowAtomically(t *testing.T) {
	f := newRealFix(t, typedStoreCharter())
	send := func(dest string) TypedRequest {
		return TypedRequest{Op: OpSendThirdParty, Send: &SendArgs{Platform: "x", Account: "acct", Destination: dest, ContentHash: "c1"}}
	}
	consume := func(id, op, args string) (ConsumeResult, error) {
		return f.ad.Consume(bg, Consume{Profile: "p", CharterID: f.act.ID, Generation: 1, ApprovalID: id, Spends: []Spend{{Kind: KindSends, N: 1, Cap: 5}},
			Audit: AuditRow{Profile: "p", CharterID: f.act.ID, Generation: 1, Op: op, ApprovalID: id, ArgsHash: args}})
	}
	argsOf := func(r TypedRequest) string {
		canon, _, _, _ := typedEffect(f.act.Constraints, r)
		h, _ := ArgsHash(r.Op, canon)
		return h
	}
	if _, err := consume("ap-never-issued", OpSendThirdParty, "h"); !errors.Is(err, ErrUnknownApproval) {
		t.Fatalf("no row: %v", err)
	}
	r := f.issue(t, send("@alice"), 0)
	h := argsOf(r)
	if _, err := consume(r.ApprovalID, OpSendThirdParty, "other"); !errors.Is(err, ErrArgsMismatch) {
		t.Fatalf("another effect: %v", err)
	}
	if _, err := consume(r.ApprovalID, OpHostSetting, h); !errors.Is(err, ErrArgsMismatch) {
		t.Fatalf("another operation: %v", err)
	}
	if res, err := consume(r.ApprovalID, OpSendThirdParty, h); err != nil || !res.Done {
		t.Fatalf("%+v %v", res, err)
	}
	if res, err := consume(r.ApprovalID, OpSendThirdParty, h); err != nil || !res.Replay {
		t.Fatalf("replay %+v %v", res, err)
	}
	b := f.issue(t, send("@bob"), 0)
	if err := f.st.RescindApproval(bg, "p", 1, b.ApprovalID, "boss"); err != nil {
		t.Fatal(err)
	}
	if _, err := consume(b.ApprovalID, OpSendThirdParty, argsOf(b)); !errors.Is(err, ErrRevoked) {
		t.Fatalf("rescinded: %v", err)
	}
	e := f.issue(t, send("@alice"), 1)
	for i := 0; i < 3; i++ {
		_, _ = f.st.AppendAudit(bg, "p", 1, "note", "x")
	}
	if _, err := consume(e.ApprovalID, OpSendThirdParty, argsOf(e)); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired: %v", err)
	}
	// plan.approve and ask.answer have no approval row: the boss's auto-approval is decided by the limits, not a row.
	if res, err := consume("plan-1", OpPlanApprove, "x"); err != nil || !res.Done {
		t.Fatalf("an auto-approval needs no row: %+v %v", res, err)
	}
}
