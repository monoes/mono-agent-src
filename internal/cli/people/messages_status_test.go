package people

import (
	"context"
	"encoding/json"
	"github.com/monoes/mono-agent/internal/testhome"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/commsync"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
)

// `people messages` shows the status and hil_ref of queued sends, filters by
// --status, and keeps listing synced messages (status sent, no hil_ref).
func TestPeopleMessagesStatusAndHilRef(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	dbPath := filepath.Join(t.TempDir(), "m.db")
	testdb.MustSeed(dbPath)
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO people (id, platform_username, platform, profile_id) VALUES ('p1','a','x','default')`); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertPersonMessage(&storage.PersonMessage{ID: "synced", PersonID: "p1", Source: "x", ExternalID: "s", Direction: "inbound", Status: "sent"}, "default"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	res, err := commsync.Queue(ctx, db.DB, commsync.Item{Profile: "default", HilRef: "hil-1", Source: "x", Action: "post", Account: commsync.Account{Platform: "x", Handle: "acme"}, Text: "Launch"})
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	cfg := &testConfig{DBPath: dbPath, JSONOutput: true, ProfileID: "default"}
	run := func(args ...string) ([]map[string]any, error) {
		var err error
		out := captureStdout(t, func() {
			c := testDeps(cfg).newPeopleMessagesCmd()
			c.SetArgs(args)
			err = c.Execute()
		})
		var rows []map[string]any
		if err == nil {
			if jerr := json.Unmarshal([]byte(out), &rows); jerr != nil {
				t.Fatalf("%v: %q", jerr, out)
			}
		}
		return rows, err
	}

	all, err := run("all")
	if err != nil || len(all) != 2 {
		t.Fatalf("all = %v %v", err, all)
	}
	pending, err := run("all", "--status", "pending_approval")
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %v %v", err, pending)
	}
	if pending[0]["status"] != "pending_approval" || pending[0]["hil_ref"] != "hil-1" || pending[0]["direction"] != "outbound" {
		t.Fatalf("pending row = %v", pending[0])
	}
	sent, err := run("all", "--status", "sent")
	if err != nil || len(sent) != 1 || sent[0]["id"] != "synced" {
		t.Fatalf("sent = %v %v", err, sent)
	}
	if _, has := sent[0]["hil_ref"]; has {
		t.Errorf("synced message carries a hil_ref: %v", sent[0])
	}
	own, err := run("list", res.PersonID, "--status", "pending_approval")
	if err != nil || len(own) != 1 {
		t.Fatalf("list --status = %v %v", err, own)
	}
	if _, err := run("all", "--status", "bogus"); exitCode(err) != 3 {
		t.Fatalf("bogus status: exit %d (%v)", exitCode(err), err)
	}
}
