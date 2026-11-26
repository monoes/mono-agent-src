package storage

import (
	"fmt"
	"strings"
	"testing"
)

func autoTool(alias, extra string) string {
	return `[{"tool":"automation_` + alias + `","alias":"` + alias + `","workflow_id":"wf1",` + extra + `}]`
}

func seed172(t *testing.T) *Database {
	t.Helper()
	d := open156(t)
	applyMigrationsBelow(t, d, 172)
	add := func(id, profile, org, role, tools, orgTools, created string, revoked any) {
		t.Helper()
		if _, err := d.DB.Exec(`INSERT INTO org_grants (id, profile_id, org_name, role_id, tools_json, org_tools_json, created_at, updated_at, revoked_at)
			VALUES (?,?,?,?,?,?,?,?,?)`, id, profile, org, role, tools, orgTools, created, created, revoked); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	// Two live grants of one alias: the older one is stricter and pins the workflow.
	add("a_old", "p", "o", "r", autoTool("x", `"workflow_hash":"h1","approval":"required","tier":"irreversible","max_calls_per_run":2,"max_calls_per_day":50,"max_output_bytes":100`), `[]`, "2026-01-01", nil)
	add("a_new", "p", "o", "r", autoTool("x", `"approval":"none","tier":"routine","max_calls_per_run":20,"max_calls_per_day":10,"max_output_bytes":900,"extra":9007199254740993`), `[]`, "2026-02-01", nil)
	// The same alias for another role or profile, another alias, and a revoked twin are not duplicates.
	add("b_role", "p", "o", "r2", autoTool("x", `"approval":"none"`), `[]`, "2026-01-01", nil)
	add("b_alias", "p", "o", "r", autoTool("y", `"approval":"none"`), `[]`, "2026-01-01", nil)
	add("b_profile", "q", "o", "r", autoTool("x", `"approval":"none"`), `[]`, "2026-01-01", nil)
	add("b_revoked", "p", "o", "r", autoTool("x", `"approval":"none"`), `[]`, "2025-01-01", "2025-02-01")
	// Org-tool duplicates: only what both hold survives, on the oldest row.
	add("t_old", "p", "o", "boss", `[]`, `[{"tool":"org_start","orgs":["s","m"]},{"tool":"org_stop","orgs":["s"]}]`, "2026-01-01", nil)
	add("t_new", "p", "o", "boss", `[]`, `[{"tool":"org_start","orgs":["s","z"]},{"tool":"org_report","orgs":["s"]}]`, "2026-02-01", nil)
	// Nothing in common: all revoked. Unreadable: all revoked.
	add("n_old", "p", "o", "none", `[]`, `[{"tool":"org_start","orgs":["s"]}]`, "2026-01-01", nil)
	add("n_new", "p", "o", "none", `[]`, `[{"tool":"org_stop","orgs":["s"]}]`, "2026-02-01", nil)
	add("u_old", "p", "o", "bad", `[]`, `[{"tool":"org_start","orgs":["s"]}]`, "2026-01-01", nil)
	add("u_new", "p", "o", "bad", `null`, `{broken`, "2026-02-01", nil)
	// A single row is left byte for byte, and rows with no slot are left alone.
	add("s_one", "p", "o", "solo", `[]`, `[ {"tool":"org_start", "orgs":["s"]} ]`, "2026-01-01", nil)
	add("w_bad1", "p", "o", "odd", `{broken`, `[]`, "2026-01-01", nil)
	add("w_bad2", "p", "o", "odd", `{broken`, `[]`, "2026-01-01", nil)
	return d
}

// Migration 172 leaves one live row per slot, never gives the kept row more than the duplicates agreed on, deletes
// nothing, records what it changed, leaves every other row as it was, and running it again changes nothing.
func TestMigration172MergesDuplicateGrants(t *testing.T) {
	d := seed172(t)
	const all = `SELECT id, profile_id, org_name, role_id, tools_json, org_tools_json, created_at, updated_at, revoked_at FROM org_grants ORDER BY id`
	const untouched = `SELECT id, tools_json, org_tools_json, updated_at, revoked_at FROM org_grants
		WHERE id LIKE 'b%' OR id IN ('s_one','w_bad1','w_bad2') ORDER BY id`
	beforeRows, beforeSame := dumpRows(t, d, all), dumpRows(t, d, untouched)

	if err := d.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	live := strings.Join(dumpRows(t, d, `SELECT id FROM org_grants WHERE revoked_at IS NULL ORDER BY id`), "")
	if want := "[a_new][b_alias][b_profile][b_role][s_one][t_old][w_bad1][w_bad2]"; live != want {
		t.Fatalf("live = %s, want %s", live, want)
	}
	if got := dumpRows(t, d, `SELECT id FROM org_grants`); len(got) != len(beforeRows) {
		t.Errorf("%d rows, had %d: nothing may be deleted", len(got), len(beforeRows))
	}
	if got := dumpRows(t, d, untouched); strings.Join(got, "\n") != strings.Join(beforeSame, "\n") {
		t.Errorf("rows that shared no slot changed:\n%v\nwas\n%v", got, beforeSame)
	}
	value := func(query string, args ...any) string {
		t.Helper()
		var s string
		if err := d.DB.QueryRow(query, args...).Scan(&s); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return s
	}
	for field, want := range map[string]string{
		"approval": "required", "tier": "irreversible", "max_calls_per_run": "2", "max_calls_per_day": "10",
		"max_output_bytes": "100", "workflow_hash": "h1", "extra": "9007199254740993", "alias": "x",
	} {
		if got := value(`SELECT CAST(json_extract(tools_json, ?) AS TEXT) FROM org_grants WHERE id = 'a_new'`, "$[0]."+field); got != want {
			t.Errorf("kept grant %s = %s, want %s", field, got, want)
		}
	}
	if got, want := value(`SELECT org_tools_json FROM org_grants WHERE id = 't_old'`), `[{"tool":"org_start","orgs":["s"]}]`; got != want {
		t.Errorf("merged org tools = %s, want %s", got, want)
	}
	log := strings.Join(dumpRows(t, d, `SELECT grant_id, action, COALESCE(kept_grant_id,'-') FROM org_grant_dedupe_log ORDER BY grant_id, action`), "")
	wantLog := "[a_new rewritten a_new][a_old revoked a_new][n_new revoked -][n_old revoked -][t_new revoked t_old][t_old rewritten t_old][u_new revoked -][u_old revoked -]"
	if log != wantLog {
		t.Errorf("log = %s\nwant %s", log, wantLog)
	}
	if old := value(`SELECT org_tools_json FROM org_grant_dedupe_log WHERE grant_id = 't_old'`); !strings.Contains(old, "org_stop") {
		t.Errorf("the log of t_old lost its former tools: %s", old)
	}

	after := dumpRows(t, d, all)
	if err := d.ApplyMigrations(); err != nil {
		t.Fatalf("second run: %v", err)
	}
	tx, err := d.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := dedupeOrgGrants(t.Context(), tx); err != nil {
		t.Fatalf("second merge: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, s := range migrationStatements(t, "172_org_grants_live_unique.sql") {
		if _, err := d.DB.Exec(s); err != nil {
			t.Fatalf("replay: %v", err)
		}
	}
	if got := dumpRows(t, d, all); strings.Join(got, "\n") != strings.Join(after, "\n") {
		t.Error("a second run changed rows")
	}
	if n := value(`SELECT COUNT(*) FROM org_grant_dedupe_log`); n != "8" {
		t.Errorf("a second run recorded more: %s rows", n)
	}

	// The database now refuses a second live row per slot, and only then.
	const ins = `INSERT INTO org_grants (id, profile_id, org_name, role_id, tools_json, org_tools_json, created_at, updated_at) VALUES (?, 'p', 'o', ?, ?, '[]', 'n', 'n')`
	for _, c := range [][3]string{{"d1", "r", autoTool("x", `"mode":"run"`)}, {"d2", "boss", `[]`}, {"d3", "boss", `null`}} {
		if _, err := d.DB.Exec(ins, c[0], c[1], c[2]); err == nil {
			t.Errorf("%s: a second live row was accepted", c[0])
		}
	}
	for _, c := range [][3]string{{"e1", "r", autoTool("z", `"mode":"run"`)}, {"e2", "none", `[]`}, {"e3", "bad", autoTool("x", `"mode":"run"`)}} {
		if _, err := d.DB.Exec(ins, c[0], c[1], c[2]); err != nil {
			t.Errorf("%s: %v", c[0], err)
		}
	}
	if _, err := d.DB.Exec(`UPDATE org_grants SET revoked_at = NULL WHERE id = 'a_old'`); err == nil {
		t.Error("reviving a duplicate was accepted")
	}
}

// A database with no duplicates (the usual case) is not touched.
func TestMigration172LeavesCleanGrantsAlone(t *testing.T) {
	d := open156(t)
	applyMigrationsBelow(t, d, 172)
	if _, err := d.DB.Exec(`INSERT INTO org_grants (id, profile_id, org_name, role_id, tools_json, org_tools_json, created_at, updated_at)
		VALUES ('g1','p','o','r',?,'[]','c','u'), ('g2','p','o','r','[]','[]','c','u')`, autoTool("x", `"mode":"run"`)); err != nil {
		t.Fatal(err)
	}
	before := dumpRows(t, d, `SELECT * FROM org_grants ORDER BY id`)
	if err := d.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	after := dumpRows(t, d, `SELECT * FROM org_grants ORDER BY id`)
	if fmt.Sprint(after) != fmt.Sprint(before) {
		t.Errorf("rows changed: %v\nwas %v", after, before)
	}
	if got := dumpRows(t, d, `SELECT COUNT(*) FROM org_grant_dedupe_log`); fmt.Sprint(got) != "[[0]]" {
		t.Errorf("log: %v", got)
	}
}
