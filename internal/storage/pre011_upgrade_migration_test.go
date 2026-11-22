package storage

import (
	"context"
	"path/filepath"
	"testing"
)

// openPre011 is a database last opened by a release from before profiles
// existed (v0.7.0 and older): every migration below 011 applied, with the
// given rows written at that schema.
func openPre011(t *testing.T, rows ...string) *Database {
	t.Helper()
	db, err := NewDatabase(filepath.Join(t.TempDir(), "pre011.db"))
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	applyMigrationsBelow(t, db, 11)
	for _, stmt := range rows {
		if _, err := db.DB.Exec(stmt); err != nil {
			t.Fatalf("writing the old data: %v", err)
		}
	}
	return db
}

// upgrade runs what an upgraded binary runs on open.
func upgrade(t *testing.T, db *Database) {
	t.Helper()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations on the old database: %v", err)
	}
	if err := db.ReconcileSchema(context.Background()); err != nil {
		t.Fatalf("ReconcileSchema: %v", err)
	}
}

// A database from before profiles with data in it keeps an owner for that
// data: 011 gives every old row the owner 'default', so the profile
// 'default' must exist after the upgrade.
func TestPre011DatabaseWithDataGetsTheDefaultProfileThatOwnsIt(t *testing.T) {
	for name, c := range map[string]struct {
		rows  []string
		owner string
	}{
		"a workflow": {[]string{`INSERT INTO workflows (id, name) VALUES ('wf-old', 'Old workflow')`},
			`SELECT profile_id FROM workflows WHERE id = 'wf-old'`},
		"a person": {[]string{`INSERT INTO people (id, platform_username, platform) VALUES ('p-old', 'ada', 'linkedin')`},
			`SELECT profile_id FROM people WHERE id = 'p-old'`},
		"a post with no person": {[]string{`INSERT INTO posts (id, platform, shortcode, url, scraped_at) VALUES ('post-old', 'instagram', 'abc', 'https://example.test/p/abc', '2026-01-01T00:00:00Z')`},
			`SELECT profile_id FROM posts WHERE id = 'post-old'`},
		// v0.7.0 created this table at runtime, outside the migrations; 013 copies its rows to 'default'.
		"OAuth app config only": {[]string{oldOAuthTable,
			`INSERT INTO platform_oauth_credentials (platform, client_id, client_secret, updated_at) VALUES ('outlook', 'app-id', 's', '2026-01-01T00:00:00Z')`},
			`SELECT profile_id FROM platform_oauth_credentials WHERE platform = 'outlook'`},
	} {
		t.Run(name, func(t *testing.T) {
			db := openPre011(t, c.rows...)
			upgrade(t, db)
			var owner string
			if err := db.DB.QueryRow(c.owner).Scan(&owner); err != nil {
				t.Fatalf("reading the old row's owner: %v", err)
			}
			if owner != "default" {
				t.Errorf("owner = %q, want default", owner)
			}
			var id, pname string
			if err := db.DB.QueryRow(`SELECT id, name FROM profiles`).Scan(&id, &pname); err != nil {
				t.Fatalf("the owner profile is missing: %v", err)
			}
			if id != "default" || pname != "Default" {
				t.Errorf("profile = (%q, %q), want (default, Default)", id, pname)
			}
			if n := countProfiles(t, db); n != 1 {
				t.Errorf("profiles = %d, want 1", n)
			}
		})
	}
}

// oldOAuthTable is the OAuth app config table as v0.7.0's application code
// created it, outside the migrations.
const oldOAuthTable = `CREATE TABLE IF NOT EXISTS platform_oauth_credentials (
	platform      TEXT NOT NULL,
	client_id     TEXT NOT NULL,
	client_secret TEXT NOT NULL DEFAULT '',
	updated_at    TEXT NOT NULL
)`

// A database from before profiles that holds no user data is as good as new:
// the upgrade makes no profile, also where the old app had made its OAuth
// table without writing to it.
func TestPre011DatabaseWithoutDataGetsNoProfile(t *testing.T) {
	for name, rows := range map[string][]string{
		"nothing at all":       nil,
		"an empty OAuth table": {oldOAuthTable},
	} {
		t.Run(name, func(t *testing.T) {
			db := openPre011(t, rows...)
			upgrade(t, db)
			if n := countProfiles(t, db); n != 0 {
				t.Errorf("profiles after upgrading an empty old database = %d, want 0", n)
			}
		})
	}
}
