package mcp

import (
	"context"
	"encoding/json"
	"github.com/monoes/mono-agent/internal/testhome"
	"testing"

	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/orgproposal"
	"github.com/monoes/mono-agent/internal/profiledir"
	"github.com/monoes/mono-agent/internal/storage"
)

// An org proposal made through MCP carries its own non-empty label: the empty one is the operator's bucket for
// `org proposal create` without --conversation (#1477).
func TestAnMCPOrgProposalIsLabelledUnscopedMCP(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	s := newTestServerAllowMutations(t, true)
	profile := serverTestProfile(s)
	db, err := storage.NewDatabase(s.opts.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := orgdesign.Save(profiledir.Root(db.DB, profile), orgdesign.NewOrg("growth", "grow", orgdesign.NewOrgOptions{})); err != nil {
		t.Fatal(err)
	}
	out, err := callToolDefault(context.Background(), s, "org_autonomy_set", json.RawMessage(`{"org_name":"growth","level":"mid"}`))
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		ID string `json:"proposal_id"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil || res.ID == "" {
		t.Fatalf("result %q: %v", out, err)
	}
	p, err := orgproposal.NewStore(db.DB).Get(context.Background(), profile, res.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.ConversationID != "unscoped:mcp" {
		t.Errorf("label %q, want unscoped:mcp", p.ConversationID)
	}
}
