package people

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
)

func TestPeopleMessagesReadUnread(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dbPath := filepath.Join(t.TempDir(), "m.db")
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
	for _, id := range []string{"m1", "m2"} {
		if err := db.UpsertPersonMessage(&storage.PersonMessage{ID: id, PersonID: "p1", Source: "x", ExternalID: id, Direction: "inbound", Status: "sent"}, "default"); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	cfg := &testConfig{DBPath: dbPath, JSONOutput: true, ProfileID: "default"}
	run := func(args ...string) (string, error) {
		var err error
		out := captureStdout(t, func() {
			c := testDeps(cfg).newPeopleMessagesCmd()
			c.SetArgs(args)
			err = c.Execute()
		})
		return out, err
	}
	count := func() int {
		out, err := run("all", "--unread")
		if err != nil {
			t.Fatal(err)
		}
		var rows []map[string]any
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			if out == "null\n" {
				return 0
			}
			t.Fatalf("%v: %q", err, out)
		}
		return len(rows)
	}
	if n := count(); n != 2 {
		t.Fatalf("unread = %d", n)
	}
	if _, err := run("read", "--person", "p1"); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 0 {
		t.Fatalf("after read --person: %d unread", n)
	}
	if _, err := run("unread", "m2"); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 1 {
		t.Fatalf("after unread m2: %d unread", n)
	}
	if _, err := run("read"); exitCode(err) != 3 {
		t.Fatalf("read with nothing: exit %d", exitCode(err))
	}
	if _, err := run("unread", "nope"); exitCode(err) != 2 {
		t.Fatalf("unread unknown: exit %d", exitCode(err))
	}
}
