package peopledupes

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db.DB
}

func exec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func person(t *testing.T, db *sql.DB, id, profile, platform, user, name, category string) {
	t.Helper()
	exec(t, db, `INSERT INTO people (id, profile_id, platform, platform_username, full_name, category)
		VALUES (?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''))`, id, profile, platform, user, name, category)
}

func count(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func undoFile(t *testing.T) string { return filepath.Join(t.TempDir(), "undo.json") }

func TestFindGroupsPlatformCaseAndEmailCase(t *testing.T) {
	db := testDB(t)
	person(t, db, "a1", "prof", "instagram", "sam", "", "")
	person(t, db, "a2", "prof", "INSTAGRAM", "sam", "Sam Lee", "")
	person(t, db, "b1", "prof", "email", "Foo@x.com", "", "")
	person(t, db, "b2", "prof", "email", "foo@x.com", "Foo", "")
	person(t, db, "c1", "prof", "instagram", "Sam", "", "") // a different username: not a duplicate
	person(t, db, "d1", "prof", "instagram", "solo", "", "")
	person(t, db, "x1", "other", "INSTAGRAM", "sam", "", "") // another profile: never merged
	groups, err := Find(context.Background(), db, "prof")
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 {
		t.Fatalf("groups = %+v", groups)
	}
	byReason := map[string]Group{}
	for _, g := range groups {
		byReason[g.Reason] = g
		if len(g.Members) != 2 {
			t.Errorf("group %+v", g)
		}
	}
	if g := byReason[ReasonPlatformCase]; g.Winner != "a2" || g.Losers()[0] != "a1" {
		t.Errorf("platform group winner = %q (richest data wins): %+v", g.Winner, g)
	}
	if g := byReason[ReasonEmailCase]; g.Winner != "b2" {
		t.Errorf("email group winner = %q: %+v", g.Winner, g)
	}
}

func TestApplyMergesAndRepoints(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	person(t, db, "w", "prof", "INSTAGRAM", "sam", "Sam Lee", "")
	person(t, db, "l", "prof", "instagram", "sam", "", "")
	exec(t, db, `UPDATE people SET headline = 'Dev' WHERE id = 'l'`)
	exec(t, db, `UPDATE people SET job_title = 'CEO' WHERE id = 'w'`) // w is the richer row
	exec(t, db, `INSERT INTO person_messages (id, person_id, source, external_id, profile_id) VALUES ('m1', 'l', 'x', 'e1', 'prof'), ('m2', 'l', 'x', 'e2', 'prof'), ('m3', 'w', 'x', 'e2', 'prof')`)
	exec(t, db, `INSERT INTO tags (id, name, profile_id) VALUES ('t1', 'lead', 'prof'), ('t2', 'hot', 'prof')`)
	exec(t, db, `INSERT INTO people_tags (person_id, tag_id) VALUES ('l', 't1'), ('l', 't2'), ('w', 't2')`)
	exec(t, db, `INSERT INTO posts (id, person_id, platform, shortcode, url, scraped_at, profile_id) VALUES ('po', 'l', 'instagram', 's', 'u', 'now', 'prof')`)
	groups, _ := Find(ctx, db, "prof")
	if len(groups) != 1 || groups[0].Winner != "w" {
		t.Fatalf("groups = %+v", groups)
	}
	backup := undoFile(t)
	res, err := Apply(ctx, db, "prof", groups, backup)
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Err != "" || res[0].Merged != 1 {
		t.Fatalf("result = %+v", res)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM people WHERE id = 'l'`); n != 0 {
		t.Error("the loser must be gone")
	}
	if n := count(t, db, `SELECT COUNT(*) FROM person_messages WHERE person_id = 'w'`); n != 2 {
		t.Errorf("w has %d messages, want e2 (own) + e1 (moved); the duplicate e2 is dropped", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM people_tags WHERE person_id = 'w'`); n != 2 {
		t.Errorf("w has %d tags, want 2", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM posts WHERE person_id = 'w'`); n != 1 {
		t.Error("a post must follow the winner, not block the delete")
	}
	var headline string
	if err := db.QueryRow(`SELECT COALESCE(headline,'') FROM people WHERE id = 'w'`).Scan(&headline); err != nil || headline != "Dev" {
		t.Errorf("winner must fill its empty fields from the loser: %q %v", headline, err)
	}
	raw, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("backup file: %v", err)
	}
	var doc struct {
		Groups []struct {
			Removed []map[string]any `json:"removed"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || len(doc.Groups) != 1 || len(doc.Groups[0].Removed) != 1 {
		t.Fatalf("backup = %s (%v)", raw, err)
	}
	if info, _ := os.Stat(backup); info.Mode().Perm() != 0o600 {
		t.Errorf("backup mode %v", info.Mode().Perm())
	}
}

func TestNeverDemoteAnApprovedPerson(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	// The richer row is pending; the poorer one is approved with its own intro.
	person(t, db, "rich", "prof", "INSTAGRAM", "sam", "Sam Lee", "pending_approval")
	exec(t, db, `UPDATE people SET introduction = 'draft', headline = 'h' WHERE id = 'rich'`)
	person(t, db, "poor", "prof", "instagram", "sam", "", "approved")
	exec(t, db, `UPDATE people SET introduction = 'approved text' WHERE id = 'poor'`)
	groups, _ := Find(ctx, db, "prof")
	if _, err := Apply(ctx, db, "prof", groups, undoFile(t)); err != nil {
		t.Fatal(err)
	}
	var cat, intro string
	if err := db.QueryRow(`SELECT category, introduction FROM people WHERE platform_username = 'sam'`).Scan(&cat, &intro); err != nil {
		t.Fatal(err)
	}
	if cat != "approved" || intro != "approved text" {
		t.Fatalf("approved person demoted: %q %q", cat, intro)
	}
}

func TestApprovedAndRejectedAreLeftForTheUser(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	person(t, db, "a", "prof", "INSTAGRAM", "sam", "", "approved")
	person(t, db, "r", "prof", "instagram", "sam", "", "rejected")
	groups, _ := Find(ctx, db, "prof")
	if len(groups) != 1 || groups[0].Conflict == "" {
		t.Fatalf("groups = %+v", groups)
	}
	res, err := Apply(ctx, db, "prof", groups, undoFile(t))
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Merged != 0 || res[0].Skipped == "" || count(t, db, `SELECT COUNT(*) FROM people`) != 2 {
		t.Fatalf("a conflicting group must be skipped untouched: %+v", res)
	}
}

func TestEachGroupIsOneTransaction(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	person(t, db, "w", "prof", "INSTAGRAM", "sam", "Sam Lee", "")
	person(t, db, "l", "prof", "instagram", "sam", "", "")
	exec(t, db, `INSERT INTO tags (id, name, profile_id) VALUES ('t1', 'lead', 'prof')`)
	exec(t, db, `INSERT INTO people_tags (person_id, tag_id) VALUES ('l', 't1')`)
	person(t, db, "w2", "prof", "INSTAGRAM", "kim", "Kim", "")
	person(t, db, "l2", "prof", "instagram", "kim", "", "")
	// A trigger makes deleting l fail after the rows were re-pointed: that
	// group must roll back whole, and the next group still go through.
	exec(t, db, `CREATE TRIGGER boom BEFORE DELETE ON people WHEN OLD.id = 'l' BEGIN SELECT RAISE(ABORT, 'boom'); END`)
	groups, _ := Find(ctx, db, "prof")
	res, err := Apply(ctx, db, "prof", groups, undoFile(t))
	if err != nil {
		t.Fatal(err)
	}
	failed, merged := 0, 0
	for _, r := range res {
		if r.Err != "" {
			failed++
		}
		merged += r.Merged
	}
	if failed != 1 || merged != 1 {
		t.Fatalf("results = %+v", res)
	}
	if count(t, db, `SELECT COUNT(*) FROM people WHERE id = 'l'`) != 1 ||
		count(t, db, `SELECT COUNT(*) FROM people_tags WHERE person_id = 'l'`) != 1 ||
		count(t, db, `SELECT COUNT(*) FROM people_tags WHERE person_id = 'w'`) != 0 {
		t.Fatal("the failed group was partly applied")
	}
	if count(t, db, `SELECT COUNT(*) FROM people WHERE id = 'l2'`) != 0 {
		t.Fatal("the second group was not applied")
	}
}

func TestEmailWinnerIsLowercased(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	person(t, db, "b1", "prof", "email", "Foo@x.com", "Foo", "")
	person(t, db, "b2", "prof", "email", "foo@x.com", "", "")
	groups, _ := Find(ctx, db, "prof")
	if _, err := Apply(ctx, db, "prof", groups, undoFile(t)); err != nil {
		t.Fatal(err)
	}
	var u string
	if err := db.QueryRow(`SELECT platform_username FROM people`).Scan(&u); err != nil || u != "foo@x.com" {
		t.Fatalf("username = %q %v", u, err)
	}
}

func TestNoBackupNoApply(t *testing.T) {
	db := testDB(t)
	person(t, db, "w", "prof", "INSTAGRAM", "sam", "Sam", "")
	person(t, db, "l", "prof", "instagram", "sam", "", "")
	groups, _ := Find(context.Background(), db, "prof")
	bad := filepath.Join(t.TempDir(), "no", "such", "dir", "u.json")
	if _, err := Apply(context.Background(), db, "prof", groups, bad); err == nil {
		t.Fatal("an unwritable backup must stop before anything changes")
	}
	if count(t, db, `SELECT COUNT(*) FROM people`) != 2 {
		t.Fatal("people changed without a backup")
	}
}
