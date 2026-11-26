package orgdecide

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/orgdesign"
)

// idlessItems are two calls of a monomind that records no request ids: a
// routine one (approved by rule) and one a decider is asked about.
func idlessItems() *fakeOrg {
	return &fakeOrg{approvals: []map[string]interface{}{
		{"roleId": "dev", "action": "Bash", "question": "q", "ts": 1, "approved": nil},
		{"roleId": "dev", "action": "org_complete", "question": "q", "ts": 2, "approved": nil},
	}}
}

// Neither a rule nor a decider approves a call without a request id: the
// approval would release calls queued after it looked. A person gets it.
func TestAutonomyNeverApprovesWithoutARequestID(t *testing.T) {
	org := idlessItems()
	dec := &scriptedDecider{replies: map[string]string{KindApproval: `{"verdict":"approve","rationale":"fine"}`}}
	s := newTestService(t, org, dec)
	setLevel(t, s, orgdesign.LevelFull)
	ds, err := s.ProcessOrg(context.Background(), "p", t.TempDir(), "growth")
	if err != nil {
		t.Fatal(err)
	}
	if len(org.resolved) != 0 {
		t.Fatalf("resolved %v", org.resolved)
	}
	got := map[string]string{}
	for _, d := range ds {
		got[d.Class] = d.Resolver + "/" + d.Verdict
		if !strings.Contains(d.Rationale, "no request id") || !strings.Contains(d.Rationale, "org approve growth dev ") {
			t.Errorf("%s rationale = %q", d.Class, d.Rationale)
		}
	}
	if got["tool:Bash"] != "rule/escalated" || got["org_complete"] != "model:test/escalated" {
		t.Fatalf("verdicts = %v", got)
	}
	// Left to the person: not routed again, and still not approved.
	if again, _ := s.ProcessOrg(context.Background(), "p", t.TempDir(), "growth"); len(again) != 0 || len(org.resolved) != 0 {
		t.Fatalf("second pass: %+v %v", again, org.resolved)
	}
}

// A decider's denial of such a call is applied as before.
func TestAutonomyStillDeniesWithoutARequestID(t *testing.T) {
	org := idlessItems()
	org.approvals = org.approvals[1:]
	dec := &scriptedDecider{replies: map[string]string{KindApproval: `{"verdict":"deny","rationale":"no"}`}}
	s := newTestService(t, org, dec)
	setLevel(t, s, orgdesign.LevelFull)
	if _, err := s.ProcessOrg(context.Background(), "p", t.TempDir(), "growth"); err != nil {
		t.Fatal(err)
	}
	if len(org.resolved) != 1 || !strings.Contains(org.resolved[0], "approval:dev:org_complete:deny") {
		t.Fatalf("resolved %v", org.resolved)
	}
}

// ApplyVerdict is the one way a verdict reaches monomind (the service and a
// decider role's decision_resolve): it holds the same rule.
func TestApplyVerdictNeedsARequestIDToApprove(t *testing.T) {
	org := &fakeOrg{}
	it := Item{Kind: KindApproval, Ref: "dev:Bash:1", Requester: "dev", Action: "Bash"}
	err := ApplyVerdict(context.Background(), org, nil, t.TempDir(), "growth", it, true, "", "boss:ceo", "ok")
	var noID *monomind.NoRequestIDError
	if !errors.As(err, &noID) || len(org.resolved) != 0 {
		t.Fatalf("approve = %v, resolved %v", err, org.resolved)
	}
	if err := ApplyVerdict(context.Background(), org, nil, t.TempDir(), "growth", it, false, "", "boss:ceo", "no"); err != nil || len(org.resolved) != 1 {
		t.Fatalf("deny = %v, resolved %v", err, org.resolved)
	}
	it.Ref, it.RequestID = "apr-1", "apr-1"
	if err := ApplyVerdict(context.Background(), org, nil, t.TempDir(), "growth", it, true, "", "boss:ceo", "ok"); err != nil || len(org.resolved) != 2 {
		t.Fatalf("approve by id = %v, resolved %v", err, org.resolved)
	}
}
