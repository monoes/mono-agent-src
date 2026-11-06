package chat

import (
	"context"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/orggrant"
	"github.com/monoes/mono-agent/internal/orgproposal"
)

// The chat's revoke (set_org_grant with revoke:true) is a proposal like the grant it undoes (#1477): the tool stores a
// pending revoke and runs nothing, whatever confirm says, and the user confirms on the card.

func (f *proposalToolFixture) grant(t *testing.T) {
	t.Helper()
	_, err := orggrant.NewStore(f.db.DB).UpsertGrant(context.Background(), orggrant.GrantInput{ProfileID: "p1", OrgName: "growth", RoleID: "lead",
		Tool: orggrant.Tool{Alias: "summarize", WorkflowID: "wf-1", Mode: "run", Wait: true, Timeout: 600, Approval: "required", Tier: "reversible",
			MaxCallsPerRun: 20, MaxCallsPerDay: 200, MaxOutputBytes: 16384}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRevokeStoresAPendingProposalAndRunsNothing(t *testing.T) {
	for _, confirm := range []string{"", `,"confirm":true`} {
		f := newProposalToolFixture(t) // its exec stub fails the test if the CLI is run
		f.grant(t)
		before := f.orgFile(t)
		out, err := f.mt.Execute("set_org_grant", `{"org_name":"growth","role_id":"lead","automation":"summarize","revoke":true`+confirm+`}`)
		if err != nil {
			t.Fatalf("revoke%s: %v", confirm, err)
		}
		m := decodeResult(t, out)
		id, _ := m["proposal_id"].(string)
		if m["tool"] != "set_org_grant" || m["applied"] != false || !hex32.MatchString(id) || m["status"] != "pending" || m["verb"] != "grant.remove" {
			t.Fatalf("revoke%s result %s", confirm, out)
		}
		if _, has := m["would_apply"]; has {
			t.Errorf("the old preview shape is back: %s", out)
		}
		p, err := orgproposal.NewStore(f.db.DB).Get(context.Background(), "p1", id)
		if err != nil || p.Verb != "grant.remove" || p.ConversationID != "conv-7" || p.Status != orgproposal.StatusPending {
			t.Fatalf("stored %+v %v", p, err)
		}
		if !strings.Contains(p.PreviewText, "Revoke") || !strings.Contains(p.PreviewText, "lead") || !strings.Contains(p.PreviewText, "summarize") {
			t.Errorf("preview %q", p.PreviewText)
		}
		if n := f.count(t, "org_grants"); n != 1 || string(f.orgFile(t)) != string(before) {
			t.Errorf("proposing a revoke changed the org (%d grants)", n)
		}
	}
}

func TestRevokeOfAGrantThatDoesNotExistIsRefusedAndStoresNothing(t *testing.T) {
	f := newProposalToolFixture(t)
	if _, err := f.mt.Execute("set_org_grant", `{"org_name":"growth","role_id":"lead","automation":"summarize","revoke":true}`); err == nil {
		t.Fatal("a revoke with no grant was accepted")
	}
	if n := f.count(t, "org_proposals"); n != 0 {
		t.Fatalf("%d proposals stored", n)
	}
}

func TestRevokeIsRefusedAfterSyncedCommsLikeTheOtherOrgTools(t *testing.T) {
	f := newProposalToolFixture(t)
	f.grant(t)
	f.mt.markSyncedCommsSeen()
	if _, err := f.mt.Execute("set_org_grant", `{"org_name":"growth","role_id":"lead","automation":"summarize","revoke":true}`); err == nil {
		t.Fatal("the injection gate did not come first")
	}
	if n := f.count(t, "org_proposals"); n != 0 {
		t.Fatalf("%d proposals stored", n)
	}
}
