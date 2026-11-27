package orgdecide

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/orgbridge"
	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/orggrant"
)

func TestBossDeciderDelegatesAndFallsBack(t *testing.T) {
	root := t.TempDir()
	doc := &orgdesign.Doc{Name: "growth", Status: "stopped", Schedule: json.RawMessage("null"), Roles: []orgdesign.Role{
		{ID: "lead", Title: "Lead", Type: "boss", Responsibilities: []string{}},
		{ID: "dev", Title: "Dev", Type: "specialist", ReportsTo: strPtr("lead"), Responsibilities: []string{}},
	}}
	if _, err := orgdesign.Save(root, doc); err != nil {
		t.Fatal(err)
	}
	org := &fakeOrg{approvals: []map[string]interface{}{
		{"roleId": "dev", "action": "WebFetch", "question": "q", "ts": 1, "approved": nil, "requestId": "apr-1"},
		{"roleId": "lead", "action": "org_complete", "question": "q", "ts": 2, "approved": nil, "requestId": "apr-2"},
	}}
	dec := &scriptedDecider{replies: map[string]string{KindApproval: `{"verdict":"approve","rationale":"fine"}`}}
	s := newTestService(t, org, dec)
	var sent []orgbridge.SendRequest
	oldCap, oldSend := capabilityCheck, delegateSend
	capabilityCheck = func(context.Context) bool { return true }
	delegateSend = func(_ context.Context, _ *sql.DB, r orgbridge.SendRequest) error { sent = append(sent, r); return nil }
	t.Cleanup(func() { capabilityCheck, delegateSend = oldCap, oldSend })

	ctx := context.Background()
	a := Defaults("p", "growth")
	a.Level = orgdesign.LevelFull
	a.Decider.Kind = orgdesign.DeciderBoss
	a.Tiers = map[string]string{"tool:*": orgdesign.TierConsequential}
	if err := s.Store.Put(ctx, a, "cli"); err != nil {
		t.Fatal(err)
	}

	// Without decision tools the boss cannot take items: fallback model.
	ds, _ := s.ProcessOrg(ctx, "p", root, "growth")
	if len(sent) != 0 || len(dec.prompts) != 2 || !strings.Contains(ds[0].Rationale, "holds no decision tools") {
		t.Fatalf("no tools: sent=%d prompts=%d rows=%+v", len(sent), len(dec.prompts), ds)
	}

	// With tools: dev's item is delegated, the boss's own item is not.
	s2 := newTestService(t, &fakeOrg{approvals: org.approvals}, &scriptedDecider{replies: dec.replies})
	if err := s2.Store.Put(ctx, a, "cli"); err != nil {
		t.Fatal(err)
	}
	if err := GrantDecisionTools(ctx, orggrant.NewStore(s2.DB), "p", "growth", "lead", "growth"); err != nil {
		t.Fatal(err)
	}
	dec2 := s2.NewDecider(a).(*scriptedDecider)
	ds, _ = s2.ProcessOrg(ctx, "p", root, "growth")
	v := map[string]string{}
	for _, d := range ds {
		v[d.Requester] = d.Resolver + "/" + d.Verdict
	}
	if v["dev"] != "boss:lead/escalated" || v["lead"] != "model:test/approved" {
		t.Fatalf("routing = %v", v)
	}
	if len(sent) != 1 || sent[0].To != "lead" || !strings.HasPrefix(sent[0].Subject, "[decision needed] tool:WebFetch") {
		t.Fatalf("delegation message = %+v", sent)
	}
	pending, _ := s2.Store.PendingFor(ctx, "p", "growth", "lead")
	if len(pending) != 1 || pending[0].Item.Requester != "dev" || pending[0].Item.Action != "WebFetch" {
		t.Fatalf("pending delegations = %+v", pending)
	}
	promptsBefore := len(dec2.prompts)

	// The boss never answers: after the timeout the fallback decides.
	s2.now = func() time.Time { return time.Now().Add(time.Duration(a.Decider.TimeoutSeconds+1) * time.Second) }
	s2.SweepDelegations(ctx, "p", root)
	if len(dec2.prompts) != promptsBefore+1 {
		t.Fatalf("fallback not asked (prompts %d -> %d)", promptsBefore, len(dec2.prompts))
	}
	if got, _ := s2.Store.GetDelegation(ctx, pending[0].ID); got.Status != DelegationExpired {
		t.Fatalf("delegation status = %s", got.Status)
	}
}

func strPtr(s string) *string { return &s }
