package people

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/peoplelinks"
	"github.com/monoes/mono-agent/internal/storage"
)

func newLinksCLITestDB(t *testing.T) (*testConfig, *storage.Database) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TYPESAFE_API_KEY", "test-key")
	t.Setenv("TYPESAFE_DEFAULT_MODEL", "")
	dbPath := filepath.Join(t.TempDir(), "links.db")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, p := range [][3]string{{"pa", "INSTAGRAM", "Ana García"}, {"pb", "LINKEDIN", "Ana Garcia"}, {"pc", "X", "Someone Else"}} {
		if _, err := db.DB.Exec(`INSERT INTO people (id, profile_id, platform, platform_username, full_name)
			VALUES (?, 'default', ?, ?, ?)`, p[0], p[1], p[0]+"-user", p[2]); err != nil {
			t.Fatal(err)
		}
	}
	return &testConfig{DBPath: dbPath, JSONOutput: true, ProfileID: "default"}, db
}

func TestPeopleLinksSuggestDisabled(t *testing.T) {
	cfg, db := newLinksCLITestDB(t)
	srv := jevtest.NewServer(t, nil)

	// Dry run needs no opt-in and sends nothing.
	out, err := runPeople(t, cfg, "links", "suggest", "--dry-run")
	var pairs []peoplelinks.Pair
	if err != nil || json.Unmarshal([]byte(out), &pairs) != nil || len(pairs) != 1 || pairs[0].A.ID != "pa" || pairs[0].B.ID != "pb" {
		t.Fatalf("dry-run = %q, %v", out, err)
	}
	if _, err := runPeople(t, cfg, "links", "suggest"); exitCode(err) != 3 || !strings.Contains(err.Error(), "jev enable people_links") {
		t.Fatalf("disabled suggest: exit %d, %v", exitCode(err), err)
	}
	if srv.Calls() != 0 {
		t.Fatalf("calls = %d", srv.Calls())
	}
	if links, _ := db.ListPersonLinks("default", ""); len(links) != 0 {
		t.Fatalf("links = %v", links)
	}
}

func TestPeopleLinksCLIFlow(t *testing.T) {
	cfg, db := newLinksCLITestDB(t)
	srv := jevtest.NewServer(t, jevtest.Fixed(map[string]string{peoplelinks.QuestionID: "0.95"}))
	if err := jevconf.SetEnabled(db.DB, "default", jevconf.PeopleLinks, true); err != nil {
		t.Fatal(err)
	}

	out, err := runPeople(t, cfg, "links", "suggest", "--limit", "10")
	var res peoplelinks.Result
	if err != nil || json.Unmarshal([]byte(out), &res) != nil || res.Suggested != 1 || srv.Calls() != 1 {
		t.Fatalf("suggest = %q, %v, calls %d", out, err, srv.Calls())
	}
	// Re-run: the pair is linked, so no request.
	if _, err := runPeople(t, cfg, "links", "suggest"); err != nil || srv.Calls() != 1 {
		t.Fatalf("re-run: %v, calls %d", err, srv.Calls())
	}

	out, err = runPeople(t, cfg, "links", "list", "--status", "suggested")
	var views []personLinkView
	if err != nil || json.Unmarshal([]byte(out), &views) != nil || len(views) != 1 {
		t.Fatalf("list = %q, %v", out, err)
	}
	id := views[0].ID
	if !strings.Contains(views[0].LabelA, "Ana García") || views[0].Confidence != 0.95 {
		t.Fatalf("view = %+v", views[0])
	}

	// Not confirmed yet: `people get` shows no links.
	out, _ = runPeople(t, cfg, "get", "pa")
	if strings.Contains(out, `"links"`) {
		t.Fatalf("unconfirmed link shown: %s", out)
	}

	if _, err := runPeople(t, cfg, "links", "confirm", id); err != nil {
		t.Fatal(err)
	}
	out, err = runPeople(t, cfg, "get", "pa")
	var got struct {
		ID    string          `json:"id"`
		Links []confirmedLink `json:"links"`
	}
	if err != nil || json.Unmarshal([]byte(out), &got) != nil || got.ID != "pa" || len(got.Links) != 1 ||
		got.Links[0].PersonID != "pb" || got.Links[0].LinkID != id {
		t.Fatalf("get (json) = %q, %v", out, err)
	}
	cfg.JSONOutput = false
	out, err = runPeople(t, cfg, "get", "pb")
	if err != nil || !strings.Contains(out, "Same person as") || !strings.Contains(out, "pa") {
		t.Fatalf("get (table) = %q, %v", out, err)
	}
	cfg.JSONOutput = true

	if _, err := runPeople(t, cfg, "links", "dismiss", id); err != nil {
		t.Fatal(err)
	}
	out, _ = runPeople(t, cfg, "links", "list", "--status", "dismissed")
	if json.Unmarshal([]byte(out), &views) != nil || len(views) != 1 || views[0].Relation != storage.PersonLinkNotSame {
		t.Fatalf("dismissed = %q", out)
	}
	out, _ = runPeople(t, cfg, "get", "pa")
	if strings.Contains(out, `"links"`) {
		t.Fatalf("dismissed link shown: %s", out)
	}

	for _, c := range []struct {
		args []string
		code int
	}{
		{[]string{"links", "confirm", "nope"}, 2},
		{[]string{"links", "dismiss", "nope"}, 2},
		{[]string{"links", "list", "--status", "maybe"}, 3},
	} {
		if _, err := runPeople(t, cfg, c.args...); exitCode(err) != c.code {
			t.Errorf("%v: exit %d (%v), want %d", c.args, exitCode(err), err, c.code)
		}
	}
	// No merges: all three people remain.
	var n int
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM people`).Scan(&n)
	if n != 3 {
		t.Fatalf("people = %d", n)
	}
}
