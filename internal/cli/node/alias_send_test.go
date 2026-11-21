package node

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/connections"
	"github.com/monoes/mono-agent/internal/hilsend"
	"github.com/monoes/mono-agent/internal/orgsign"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
	"github.com/monoes/mono-agent/internal/workflow"
)

// sentNode is a sending node that sends nothing: it counts its runs.
type sentNode struct {
	typ  string
	runs *atomic.Int32
}

func (n sentNode) Type() string { return n.typ }

func (n sentNode) Execute(context.Context, workflow.NodeInput, map[string]interface{}) ([]workflow.NodeOutput, error) {
	n.runs.Add(1)
	return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{{JSON: map[string]interface{}{"sent": true}}}}}, nil
}

// An agent's `node run` of a send under a legacy unprefixed name (an alias
// of the registry) waits for approval as under its canonical name. The
// guard was asked with the name as typed, which no send rule knew, so
// `node run telegram …` sent with no card.
func TestNodeRunOfALegacyNameWaits(t *testing.T) {
	for _, k := range append(orgsign.AgentContextMarkers(), hilsend.EnvAgentContext, "MONOAGENT_ACTOR", browserctl.EnvAICaller, workflow.EnvWorkflowRun,
		hilsend.EnvApprovalID, hilsend.EnvApprovalProof, hilsend.EnvConversation, hilsend.EnvAgentRole, hilsend.EnvAgentID) {
		t.Setenv(k, "")
	}
	var runs atomic.Int32
	d := testDeps(nodeVaultTestDB(t), "default", false)
	d.BuildRegistry = func(*sql.DB) *workflow.NodeTypeRegistry {
		workflow.SetExecGuard(hilsend.Guard) // as noderegistry.Build does
		r := workflow.NewNodeTypeRegistry()
		for legacy, canonical := range map[string]string{"telegram": "comm.telegram", "gmail": "service.gmail", "discord": "service.discord"} {
			typ := canonical
			r.Register(typ, func() workflow.NodeExecutor { return sentNode{typ: typ, runs: &runs} })
			r.Alias(legacy, canonical) // as noderegistry.Build does
		}
		return r
	}
	t.Setenv(hilsend.EnvAgentContext, "coder")
	for name, config := range map[string]map[string]interface{}{
		"telegram": {"chat_id": "42", "text": "Hi Ada"},
		"gmail":    {"operation": "send_message", "to": "ada@example.test", "subject": "Hello", "body": "Hi Ada"},
		"discord":  {"operation": "send_message", "channel_id": "C1", "content": "Hi all"},
	} {
		raw, _ := json.Marshal(map[string]interface{}{"config": config})
		out, err := runNodeSubcmdStdin(t, d, string(raw), name)
		if err != nil {
			t.Errorf("node run %s: %v\n%s", name, err, out)
			continue
		}
		var queued map[string][]map[string]map[string]interface{}
		if err := json.Unmarshal([]byte(out), &queued); err != nil || len(queued["main"]) != 1 || queued["main"][0]["json"]["hil_id"] == nil {
			t.Errorf("node run %s was not queued: %v\n%s", name, err, out)
		}
	}
	if n := runs.Load(); n != 0 {
		t.Errorf("%d of the sends ran without an approval", n)
	}
}

// An agent's `node run` of a send names its connection as it likes (an id,
// or a platform with several connections): the card pins the one this run
// resolved, by id, so an approval sends through the same connection rather
// than whichever it would pick then.
func TestNodeRunOfASendPinsItsConnection(t *testing.T) {
	for _, k := range append(orgsign.AgentContextMarkers(), hilsend.EnvAgentContext, "MONOAGENT_ACTOR", browserctl.EnvAICaller, workflow.EnvWorkflowRun,
		hilsend.EnvApprovalID, hilsend.EnvApprovalProof, hilsend.EnvConversation, hilsend.EnvAgentRole, hilsend.EnvAgentID) {
		t.Setenv(k, "")
	}
	dbPath := nodeVaultTestDB(t)
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	testdb.AddDefaultProfile(t, db.DB)
	store := connections.NewStore(db.DB)
	for _, c := range []*connections.Connection{
		{ID: "conn-old", Platform: "outlook", Method: "oauth", Status: "expired", ProfileID: "default", Data: map[string]interface{}{"email": "me@x.com"}},
		{ID: "conn-new", Platform: "outlook", Method: "oauth", Status: "active", ProfileID: "default", Data: map[string]interface{}{"email": "me@x.com"}},
	} {
		if err := store.Save(context.Background(), c); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	var runs atomic.Int32
	d := testDeps(dbPath, "default", false)
	d.ResolveCredential = func(context.Context, *connections.Store, string, string) (map[string]interface{}, error) {
		return map[string]interface{}{"access_token": "tok"}, nil
	}
	d.BuildRegistry = func(*sql.DB) *workflow.NodeTypeRegistry {
		workflow.SetExecGuard(hilsend.Guard) // as noderegistry.Build does
		r := workflow.NewNodeTypeRegistry()
		r.Register("service.outlook_mail", func() workflow.NodeExecutor { return sentNode{typ: "service.outlook_mail", runs: &runs} })
		return r
	}
	t.Setenv(hilsend.EnvAgentContext, "coder")
	raw, _ := json.Marshal(map[string]interface{}{"config": map[string]interface{}{"operation": "send_message", "to": "ada@example.test",
		"subject": "Hi", "body": "Hello", "credential_id": "outlook"}})
	if out, err := runNodeSubcmdStdin(t, d, string(raw), "service.outlook_mail"); err != nil || runs.Load() != 0 {
		t.Fatalf("node run: %v (runs %d)\n%s", err, runs.Load(), out)
	}
	db, err = storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cards, err := hilsend.Store{DB: db.DB, Profile: "default"}.List(context.Background())
	if err != nil || len(cards) != 1 {
		t.Fatalf("cards: %v %+v", err, cards)
	}
	for _, dt := range cards[0].Details {
		if dt.Key == "connection" && dt.Value != "conn-new" {
			t.Fatalf("connection %q, want conn-new pinned", dt.Value)
		}
	}
}
