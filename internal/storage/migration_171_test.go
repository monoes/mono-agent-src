package storage

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/data"
)

// execNoFK runs stmts on one connection with foreign key enforcement off: the only way to seed a message whose
// person no longer exists, which an older database can hold.
func execNoFK(t *testing.T, d *Database, stmts ...string) {
	t.Helper()
	ctx := context.Background()
	conn, err := d.DB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	defer conn.ExecContext(ctx, "PRAGMA foreign_keys = ON")
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		t.Fatal(err)
	}
	for _, s := range stmts {
		if _, err := conn.ExecContext(ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

// dumpRows reads query as one string per row, so two dumps can be compared whole.
func dumpRows(t *testing.T, d *Database, query string, args ...any) []string {
	t.Helper()
	rows, err := d.DB.Query(query, args...)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%v", vals))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// Every column of person_messages but the one migration 171 repairs, as text so odd values survive the read.
const messagesButProfile = `SELECT id, person_id, source, external_id, direction, quote(sender), quote(subject),
	quote(body), quote(metadata), status, quote(sent_at), quote(created_at), quote(read_at), quote(hil_ref)
	FROM person_messages ORDER BY id`

func seed171(t *testing.T) *Database {
	t.Helper()
	d := open156(t)
	applyMigrationsBelow(t, d, 171)
	execNoFK(t, d,
		`INSERT INTO people (id, platform_username, platform, profile_id) VALUES
			('pa', 'a', 'email', 'a'), ('pb', 'b', 'email', 'b'), ('pd', 'd', 'email', 'default'), ('pnone', 'n', 'email', '')`,
		`INSERT INTO person_messages (id, person_id, source, external_id, direction, subject, body, status, sent_at, profile_id, hil_ref) VALUES
			('ok',            'pa',    'email', '1', 'inbound',  's', 'b', 'sent',  '2026-10-01 10:00:00 +0000 UTC', 'a', NULL),
			('empty',         'pa',    'email', '2', 'inbound',  's', 'b', 'sent',  '2026-10-02T10:00:00Z',          '',  NULL),
			('wrongdefault',  'pb',    'email', '3', 'inbound',  's', 'b', 'draft', 1750000000,                      'default', NULL),
			('wrongother',    'pa',    'email', '4', 'outbound', 's', 'b', 'draft', '',                              'b', 'h1'),
			('oddnow',        'pd',    'email', '5', 'inbound',  's', 'b', 'sent',  'now',                           'a', NULL),
			('oddtext',       'pd',    'email', '6', 'inbound',  's', 'b', 'sent',  'not a time',                    'default', NULL),
			('oddblob',       'pd',    'email', '7', 'inbound',  's', 'b', 'sent',  x'00ff',                         'default', NULL),
			('personnoowner', 'pnone', 'email', '8', 'inbound',  's', 'b', 'sent',  NULL,                            'a', NULL),
			('orphan',        'gone',  'email', '9', 'inbound',  's', 'b', 'sent',  NULL,                            'b', NULL),
			('orphanempty',   'gone',  'email', '10','inbound',  's', 'b', 'sent',  NULL,                            '',  NULL)`,
	)
	return d
}

var want171 = map[string]string{
	"ok": "a", "empty": "a", "wrongdefault": "b", "wrongother": "a",
	"oddnow": "default", "oddtext": "default", "oddblob": "default",
	// No owner to give: kept exactly as stored.
	"personnoowner": "a", "orphan": "b", "orphanempty": "",
}

func profiles171(t *testing.T, d *Database) map[string]string {
	t.Helper()
	rows, err := d.DB.Query(`SELECT id, profile_id FROM person_messages`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, p string
		if err := rows.Scan(&id, &p); err != nil {
			t.Fatal(err)
		}
		out[id] = p
	}
	return out
}

// Migration 171 gives every message its person's profile without losing a row or changing anything else, records
// what it changed, leaves orphans alone, keeps every message visible in its person's profile and in no other, and
// running it again changes nothing.
func TestMigration171RepairsMessageProfiles(t *testing.T) {
	d := seed171(t)
	before := dumpRows(t, d, messagesButProfile)
	// What the person-joined listings showed before the migration: the messages of a profile's people.
	visibleBefore := map[string][]string{}
	for _, p := range []string{"a", "b", "default"} {
		visibleBefore[p] = dumpRows(t, d, `SELECT pm.id FROM person_messages pm JOIN people p ON p.id = pm.person_id
			WHERE p.profile_id = ? ORDER BY pm.id`, p)
		if len(visibleBefore[p]) == 0 {
			t.Fatalf("seed: nothing visible in %s", p)
		}
	}

	for run := 1; run <= 2; run++ {
		if err := d.ApplyMigrations(); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		if got := fmt.Sprint(profiles171(t, d)); got != fmt.Sprint(want171) {
			t.Fatalf("run %d: profiles = %s\nwant %v", run, got, want171)
		}
		if got := dumpRows(t, d, messagesButProfile); strings.Join(got, "\n") != strings.Join(before, "\n") {
			t.Fatalf("run %d: rows changed beyond profile_id:\n%s\nwas:\n%s", run, strings.Join(got, "\n"), strings.Join(before, "\n"))
		}
	}
	// The statements themselves are idempotent too.
	for _, s := range migrationStatements(t, "171_person_messages_profile.sql") {
		if _, err := d.DB.Exec(s); err != nil {
			t.Fatalf("replaying %q: %v", truncate(s, 60), err)
		}
	}
	if got := fmt.Sprint(profiles171(t, d)); got != fmt.Sprint(want171) {
		t.Fatalf("after a replay: %s", got)
	}

	repairs := dumpRows(t, d, `SELECT message_id, was_profile, now_profile FROM person_message_profile_repairs ORDER BY message_id`)
	wantRepairs := "[empty  a] [oddnow a default] [wrongdefault default b] [wrongother b a]"
	if got := strings.Join(repairs, " "); got != wantRepairs {
		t.Errorf("recorded repairs = %s, want %s", got, wantRepairs)
	}

	for _, p := range []string{"a", "b", "default"} {
		all, err := d.ListAllPersonMessagesByStatus(p, "", "", false, 100, 0)
		if err != nil {
			t.Fatalf("list all %s: %v", p, err)
		}
		var ids []string
		byStatus := map[string]bool{}
		for _, st := range []string{"sent", "draft"} {
			ms, err := d.ListPersonMessagesByStatus(p, st)
			if err != nil {
				t.Fatalf("by status %s: %v", p, err)
			}
			for _, m := range ms {
				byStatus["["+m.ID+"]"] = true
			}
		}
		for _, m := range all {
			ids = append(ids, "["+m.ID+"]")
			one, err := d.ListPersonMessages(m.PersonID, "", p, 100, 0)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, o := range one {
				found = found || o.ID == m.ID
			}
			if !found || !byStatus["["+m.ID+"]"] {
				t.Errorf("%s/%s: person listing %v, status listing %v", p, m.ID, found, byStatus["["+m.ID+"]"])
			}
		}
		sort.Strings(ids)
		if strings.Join(ids, " ") != strings.Join(visibleBefore[p], " ") {
			t.Errorf("profile %s sees %v, saw %v before", p, ids, visibleBefore[p])
		}
		if len(byStatus) != len(visibleBefore[p]) {
			t.Errorf("profile %s: status listings show %d, want %d", p, len(byStatus), len(visibleBefore[p]))
		}
	}
}

// After 171 the database itself refuses a message in another profile than its person's, and fills in a profile that
// is left empty.
func TestMigration171KeepsProfilesEqual(t *testing.T) {
	d := seed171(t)
	if err := d.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	profileOf := func(id string) string { return profiles171(t, d)[id] }
	ins := `INSERT INTO person_messages (id, person_id, source, external_id, profile_id) VALUES (?, ?, 'x', ?, ?)`

	for _, bad := range []struct{ person, profile string }{{"pa", "b"}, {"pb", "default"}} {
		if _, err := d.DB.Exec(ins, "bad", bad.person, "bad", bad.profile); err == nil || !strings.Contains(err.Error(), "profile") {
			t.Errorf("insert for %s in %s: err = %v, want refused", bad.person, bad.profile, err)
		}
		if _, err := d.DB.Exec(strings.Replace(ins, "INSERT", "INSERT OR IGNORE", 1), "bad", bad.person, "bad", bad.profile); err == nil {
			t.Errorf("INSERT OR IGNORE for %s in %s was accepted", bad.person, bad.profile)
		}
	}
	if profileOf("bad") != "" {
		t.Error("a refused insert left a row")
	}
	if _, err := d.DB.Exec(ins, "filled", "pb", "filled", ""); err != nil || profileOf("filled") != "b" {
		t.Errorf("an empty profile: %q, %v, want b", profileOf("filled"), err)
	}
	if _, err := d.DB.Exec(`UPDATE person_messages SET profile_id = 'b' WHERE id = 'ok'`); err == nil {
		t.Error("moving a message to another profile was accepted")
	}
	if _, err := d.DB.Exec(`UPDATE person_messages SET profile_id = '' WHERE id = 'ok'`); err == nil {
		t.Error("emptying a message's profile was accepted")
	}
	if _, err := d.DB.Exec(`UPDATE person_messages SET person_id = 'pb' WHERE id = 'ok'`); err == nil {
		t.Error("giving a message to a person of another profile was accepted")
	}
	// Rows with no owner to compare with stay writable.
	for _, id := range []string{"orphan", "orphanempty", "personnoowner"} {
		if _, err := d.DB.Exec(`UPDATE person_messages SET status = 'sent', profile_id = profile_id WHERE id = ?`, id); err != nil {
			t.Errorf("updating %s: %v", id, err)
		}
	}
	// A person that changes profile takes its messages along.
	if _, err := d.DB.Exec(`UPDATE people SET profile_id = 'c' WHERE id = 'pa'`); err != nil {
		t.Fatal(err)
	}
	if profileOf("ok") != "c" || profileOf("wrongother") != "c" || profileOf("orphan") != "b" {
		t.Errorf("after the person moved: %v", profiles171(t, d))
	}
	// A re-sync of a stored message still updates it.
	msg := &PersonMessage{PersonID: "pb", Source: "x", ExternalID: "filled", Body: "again"}
	if err := d.UpsertPersonMessage(msg, "b"); err != nil {
		t.Errorf("upsert: %v", err)
	}
}

// Reads filter on the message's own profile as well as the person's: should a row ever disagree (an older binary,
// a hand edit), it shows in neither profile.
func TestMessageReadsNeverCrossProfiles(t *testing.T) {
	d := seed171(t)
	if err := d.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	for _, trig := range dumpRows(t, d, `SELECT name FROM sqlite_master WHERE type = 'trigger' AND name LIKE 'person%profile%'`) {
		if _, err := d.DB.Exec("DROP " + "TRIGGER " + strings.Trim(trig, "[]")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.DB.Exec(`UPDATE person_messages SET profile_id = 'b' WHERE id = 'ok'`); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"a", "b"} {
		all, err := d.ListAllPersonMessagesByStatus(p, "", "", false, 100, 0)
		if err != nil {
			t.Fatal(err)
		}
		sent, err := d.ListPersonMessagesByStatus(p, "sent")
		if err != nil {
			t.Fatal(err)
		}
		one, err := d.ListPersonMessages("pa", "", p, 100, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range append(all, sent...) {
			if m.ID == "ok" {
				t.Errorf("profile %s lists the mismatched message", p)
			}
		}
		for _, m := range one {
			if m.ID == "ok" {
				t.Errorf("profile %s lists the mismatched message under its person", p)
			}
		}
	}
}

func migrationStatements(t *testing.T, file string) []string {
	t.Helper()
	b, err := data.MigrationsFS.ReadFile("migrations/" + file)
	if err != nil {
		t.Fatal(err)
	}
	st, err := splitStatementsChecked(string(b))
	if err != nil {
		t.Fatal(err)
	}
	return st
}
