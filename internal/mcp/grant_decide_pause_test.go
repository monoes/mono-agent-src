package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/orgdecide"
	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/orggrant"
)

// setOrgLevel stores the org's autonomy at level, optionally paused.
func setOrgLevel(t *testing.T, store *orgdecide.Store, org, level string, pausedUntil *time.Time) {
	t.Helper()
	a := orgdecide.Defaults("default", org)
	a.Level, a.PausedUntil = level, pausedUntil
	if err := store.Put(context.Background(), a, "test"); err != nil {
		t.Fatal(err)
	}
}

// A delegated role cannot settle a decision while its org is paused or set
// to manual: the pause brake stops the sweep, and it stops this too.
func TestDecisionResolveRefusesWhileOrgIsPausedOrManual(t *testing.T) {
	s, db := newOrgToolServer(t, []orggrant.OrgTool{
		{Tool: orgdecide.ToolDecisionList, Orgs: []string{"sales"}},
		{Tool: orgdecide.ToolDecisionResolve, Orgs: []string{"sales"}},
	})
	ctx := context.Background()
	store := orgdecide.NewStore(db.DB)
	d, err := store.Delegate(ctx, "default", "sales", "mid", "hq", "ceo",
		orgdecide.Item{Kind: orgdecide.KindApproval, Ref: "req-1", RequestID: "req-1", Requester: "lead", Class: "grant:x", Tier: "consequential", Action: "monoagent__automation_x"},
		time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	resolve := func() error {
		_, err := s.callGrantTool(ctx, orgdecide.ToolDecisionResolve, json.RawMessage(`{"id":"`+d.ID+`","verdict":"escalate","rationale":"x"}`))
		return err
	}
	later := time.Now().Add(time.Hour)
	for name, setup := range map[string]func(){
		"manual": func() { setOrgLevel(t, store, "sales", orgdesign.LevelManual, nil) },
		"paused": func() { setOrgLevel(t, store, "sales", orgdesign.LevelMid, &later) },
	} {
		setup()
		if err := resolve(); err == nil || !strings.HasPrefix(err.Error(), codeRefusedGrant) {
			t.Fatalf("%s org: resolve = %v, want a refused_grant", name, err)
		}
		if got, _ := store.GetDelegation(ctx, d.ID); got == nil || got.Status != orgdecide.DelegationPending {
			t.Fatalf("%s org: the refused decision changed state: %+v", name, got)
		}
	}
	setOrgLevel(t, store, "sales", orgdesign.LevelMid, nil)
	if err := resolve(); err != nil {
		t.Fatalf("a live mid org: %v", err)
	}
}
