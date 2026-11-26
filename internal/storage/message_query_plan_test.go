package storage

import (
	"strings"
	"testing"
)

// plan is EXPLAIN QUERY PLAN's detail lines for query.
func plan(t *testing.T, d *Database, query string, args ...any) string {
	t.Helper()
	rows, err := d.DB.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatalf("explain %s: %v", query, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		out = append(out, detail)
	}
	return strings.Join(out, " | ")
}

// The queries that filter on person_messages.profile_id are served by an index (the app keeps no planner
// statistics): the whole-profile listing reads in order from one (no sort), a person's listing goes by person (not
// by walking the profile), and the approval machinery still goes by hil_ref.
func TestMessageQueriesUseTheirIndexes(t *testing.T) {
	d := openMigrated156(t)
	check := func(name, query string, args []any, want []string, never ...string) {
		t.Helper()
		got := plan(t, d, query, args...)
		ok := false
		for _, w := range want {
			ok = ok || strings.Contains(got, w)
		}
		if !ok {
			t.Errorf("%s: plan %q, want one of %q", name, got, want)
		}
		for _, n := range append(never, "SCAN pm", "SCAN person_messages") {
			if strings.Contains(got, n) {
				t.Errorf("%s: plan %q has %q", name, got, n)
			}
		}
	}
	const instant = "USING INDEX idx_person_messages_profile_instant (profile_id=?)"
	for _, c := range []struct {
		name, source, status string
		unread               bool
		want                 []string
	}{
		{"all", "", "", false, []string{instant}},
		{"by source", "outlook", "", false, []string{instant}},
		{"by status", "", "pending_approval", false, []string{instant, "idx_person_messages_profile_status"}},
		{"unread", "", "", true, []string{instant, "idx_person_messages_unread"}},
	} {
		q, args := allMessagesQuery("p", c.source, c.status, c.unread, 100, 0)
		var never []string
		if c.status == "" && !c.unread {
			never = []string{"TEMP B-TREE"}
		}
		check(c.name, q, args, c.want, never...)
	}
	q, args := messagesByStatusQuery("p", "draft")
	check("status list", q, args, []string{"idx_person_messages_profile_status (profile_id=? AND status=?)"}, "TEMP B-TREE")
	for _, source := range []string{"", "outlook"} {
		q, args = personMessagesQuery("x", source, "p", 100, 0)
		check("person list "+source, q, args, []string{"(person_id=?"})
	}
	check("hil scrub", `UPDATE person_messages SET body = '' WHERE hil_ref = ? AND profile_id = ?`, []any{"h", "p"},
		[]string{"idx_person_messages_hil_ref"})
	check("hil text read", `SELECT body, subject FROM person_messages WHERE hil_ref = ? AND profile_id = ?`, []any{"h", "p"},
		[]string{"idx_person_messages_hil_ref"})
	check("assistant's person list", `SELECT id FROM person_messages WHERE profile_id = ? AND person_id = ? ORDER BY created_at DESC LIMIT ?`,
		[]any{"p", "x", 5}, []string{"(person_id=?", "profile_id=? AND person_id=?"})
	check("mark a person read", `UPDATE person_messages SET read_at = ? WHERE profile_id = ? AND direction = 'inbound' AND read_at IS NULL AND person_id = ?`,
		[]any{"t", "p", "x"}, []string{"(person_id=?", "profile_id=? AND person_id=?"})
	check("by id", `SELECT id FROM person_messages WHERE profile_id = ? AND id IN (?, ?)`, []any{"p", "a", "b"},
		[]string{"sqlite_autoindex_person_messages_1 (id=?)"})
	check("drafts count", `SELECT COUNT(*) FROM person_messages WHERE profile_id = ? AND status = 'draft'`, []any{"p"},
		[]string{"idx_person_messages_profile_status (profile_id=? AND status=?)"})
	check("unread count", `SELECT COUNT(*) FROM person_messages WHERE profile_id = ? AND direction = 'inbound' AND read_at IS NULL`, []any{"p"},
		[]string{"idx_person_messages_unread", "idx_person_messages_profile"})
	check("upsert read", `SELECT metadata FROM person_messages WHERE person_id = ? AND source = ? AND external_id = ?`, []any{"x", "s", "e"},
		[]string{"sqlite_autoindex_person_messages_2 (person_id=? AND source=? AND external_id=?)"})
}
