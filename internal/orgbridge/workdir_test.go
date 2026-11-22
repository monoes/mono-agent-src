package orgbridge

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/orggrant"
	"github.com/monoes/mono-agent/internal/workflow"
)

// C-46 for automation roles: a message from an agent role starts the
// workflow in the daemon, outside that role's workdir confinement, so the
// run carries the sender's workdir as org.workdir. An org-qualified sender
// this profile root cannot resolve is held to the profile root; a bare
// sender that is no role of the org (a person) leaves the run unconfined.
func TestReceiverPassesTheSenderWorkdir(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	root := t.TempDir()
	boss := "lead"
	for _, d := range []*orgdesign.Doc{
		{Name: "growth", Status: "stopped", Schedule: json.RawMessage("null"),
			RunConfig: map[string]json.RawMessage{"workspace": json.RawMessage(`"isolated"`)},
			Roles: []orgdesign.Role{{ID: "lead", Title: "Lead", Type: "boss", Responsibilities: []string{"x"}},
				{ID: "bot", Title: "Bot", Type: "automation", ReportsTo: &boss, Responsibilities: []string{"x"}}}},
		{Name: "hq", Status: "stopped", Schedule: json.RawMessage("null"),
			RunConfig: map[string]json.RawMessage{"workspace": json.RawMessage(`"worktree"`)},
			Roles:     []orgdesign.Role{{ID: "ceo", Title: "CEO", Type: "boss", Responsibilities: []string{"x"}}}},
	} {
		if _, err := orgdesign.Save(root, d); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO workflows (id, name, profile_id, is_active) VALUES ('wf-bot', 'Bot', 'p', 0)`); err != nil {
		t.Fatal(err)
	}
	ep, err := orggrant.NewStore(db).CreateEndpoint(ctx, "p", "growth", "bot", "wf-bot")
	if err != nil {
		t.Fatal(err)
	}
	row, err := orggrant.NewStore(db).LookupEndpoint(ctx, ep.ID)
	if err != nil {
		t.Fatal(err)
	}
	rcv := &Receiver{DB: db, Store: workflow.NewSQLiteWorkflowStore(db), RootOf: func(string) string { return root }}
	orgs := filepath.Join(root, ".monomind", "orgs")
	cases := []struct {
		from, want string
	}{
		{"lead", filepath.Join(orgs, "growth", "workspace")},
		{"growth:lead", filepath.Join(orgs, "growth", "workspace")},
		{"hq:ceo", filepath.Join(orgs, "hq", "worktree")},
		{"human", ""},
		{"operator:me", ""},
	}
	for i, c := range cases {
		msgID := "msg-" + c.from
		rcv.dispatch(ctx, *row, EndpointDelivery{OrgName: "growth", Run: "run-1", From: c.from, To: "bot", Subject: "go", Body: "go", MessageID: msgID})
		var raw string
		if err := db.QueryRow(`SELECT trigger_data FROM workflow_executions ORDER BY rowid DESC LIMIT 1 OFFSET 0`).Scan(&raw); err != nil {
			t.Fatalf("%s: no execution: %v", c.from, err)
		}
		var n int
		_ = db.QueryRow(`SELECT COUNT(*) FROM workflow_executions`).Scan(&n)
		if n != i+1 {
			t.Fatalf("%s: %d executions, want %d", c.from, n, i+1)
		}
		var td struct {
			Org map[string]interface{} `json:"org"`
		}
		_ = json.Unmarshal([]byte(raw), &td)
		got, _ := td.Org["workdir"].(string)
		if got != c.want {
			t.Errorf("from %s: org.workdir = %q, want %q", c.from, got, c.want)
		}
	}
}

// R06-02: a sender that resolves to no role and is not an explicit person
// is refused, never widened to the profile root nor left unconfined.
func TestSenderWorkdirFailsClosed(t *testing.T) {
	root := t.TempDir()
	for _, from := range []string{"elsewhere:role", "growth:ghost", "ghost", "humanoid"} {
		if wd, ok := senderWorkdir(root, "growth", from); ok || wd != "" || !senderRefused(root, "growth", from) {
			t.Errorf("%s: wd %q ok %v refused %v", from, wd, ok, senderRefused(root, "growth", from))
		}
	}
	for _, from := range []string{"human", "operator:me"} {
		if senderRefused(root, "growth", from) {
			t.Errorf("%s must be allowed", from)
		}
	}
}
