package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/orgdecide"
	"github.com/monoes/mono-agent/internal/orggrant"
)

// A decider role cannot approve a call monomind gave no request id: that would
// release calls it never saw. The item stays with it to deny.
func TestDecisionResolveNeverApprovesWithoutARequestID(t *testing.T) {
	s, db := newOrgToolServer(t, []orggrant.OrgTool{
		{Tool: orgdecide.ToolDecisionList, Orgs: []string{"sales"}},
		{Tool: orgdecide.ToolDecisionResolve, Orgs: []string{"sales"}},
	})
	ctx := context.Background()
	client := &recordingClient{}
	old := decisionClient
	decisionClient = client
	t.Cleanup(func() { decisionClient = old })
	store := orgdecide.NewStore(db.DB)
	setOrgLevel(t, store, "sales", "mid", nil)
	d, err := store.Delegate(ctx, "default", "sales", "mid", "hq", "ceo",
		orgdecide.Item{Kind: orgdecide.KindApproval, Ref: "lead:Bash:1", Requester: "lead", Class: "tool:Bash", Tier: "consequential", Action: "Bash"},
		time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	resolve := func(verdict string) error {
		_, err := s.callGrantTool(ctx, orgdecide.ToolDecisionResolve, json.RawMessage(`{"id":"`+d.ID+`","verdict":"`+verdict+`","rationale":"x"}`))
		return err
	}
	if err := resolve("approve"); err == nil || !strings.Contains(err.Error(), "no request id") {
		t.Fatalf("approve = %v", err)
	}
	if got, _ := store.GetDelegation(ctx, d.ID); got == nil || got.Status != orgdecide.DelegationPending || len(client.calls) != 0 {
		t.Fatalf("after the refusal: %+v, calls %v", got, client.calls)
	}
	if err := resolve("deny"); err != nil || len(client.calls) != 1 || !strings.Contains(client.calls[0], "Bash:deny") {
		t.Fatalf("deny = %v, calls %v", err, client.calls)
	}
}
