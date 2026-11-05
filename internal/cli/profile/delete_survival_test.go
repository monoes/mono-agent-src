package profile

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
)

// TestProfileDeleteLeavesAnotherProfilesDerivedRowsIntact seeds two profiles with a row in every
// derivedScope table (the list comes from derivedScope itself, so a table added later is seeded and
// checked without touching this test), deletes one, and requires that exactly the other's row is
// left in each: the deleted profile's row is gone and a rule that is too wide cannot take the
// survivor's row with it.
func TestProfileDeleteLeavesAnotherProfilesDerivedRowsIntact(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	doomed := createTestProfile(t, dbPath, "doomed")
	keeper := createTestProfile(t, dbPath, "keeper")

	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	conn, err := db.DB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}

	baseline := map[string]int{}
	for _, e := range derivedScope {
		var n int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM "`+e.Table+`"`).Scan(&n); err != nil {
			t.Fatalf("derivedScope names %s, which is not a table: %v", e.Table, err)
		}
		baseline[e.Table] = n
	}

	for _, p := range []string{doomed, keeper} {
		wf, ex := "wf-"+p, "ex-"+p
		if _, err := conn.ExecContext(ctx, `INSERT INTO workflows (id, name, profile_id) VALUES (?, ?, ?)`, wf, wf, p); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO workflow_executions (id, workflow_id, profile_id, status) VALUES (?, ?, ?, 'COMPLETED')`, ex, wf, p); err != nil {
			t.Fatal(err)
		}
		for _, e := range derivedScope {
			if e.Table == "settings" || e.Table == "configs" {
				continue // keyed by name, seeded below
			}
			// An open publication refuses the delete (TestProfileDeleteRefusesWhileAWorkflowChangeIsOpen),
			// so only the surviving profile gets one: it still catches a rule that is too wide.
			if e.Table == "workflow_publications" && p == doomed {
				continue
			}
			seedDerivedRow(t, ctx, conn, e.Table, p, wf, ex)
		}
		for _, kv := range [][2]string{{"task_agent_access:" + p, "1"}, {"jev." + p + ".chat.model", "m"}} {
			if _, err := conn.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?)`, kv[0], kv[1]); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO configs (name, config_data) VALUES (?, '{}')`, "agent_purposes."+p+".coding"); err != nil {
			t.Fatal(err)
		}
	}
	conn.Close()
	db.Close()

	if _, err := runProfileCmd(t, dbPath, "delete", doomed, "--yes-delete-profile", "doomed"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	db, err = storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, e := range derivedScope {
		var n int
		if err := db.DB.QueryRow(`SELECT COUNT(*) FROM "` + e.Table + `"`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		// settings and configs hold two rows per profile; the others one (the doomed profile's publication is not seeded)
		per := 1
		if e.Table == "settings" {
			per = 2
		}
		if want := baseline[e.Table] + per; n != want {
			t.Errorf("%s: %d row(s) after the delete, want %d (the other profile's rows only, on top of the %d that were there)", e.Table, n, want, baseline[e.Table])
		}
	}
	var kept string
	if err := db.DB.QueryRow(`SELECT COALESCE(GROUP_CONCAT(key), '') FROM settings WHERE key LIKE 'task_agent_access:%' OR key LIKE 'jev.%'`).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(kept, keeper) || strings.Contains(kept, doomed) {
		t.Errorf("settings left after the delete: %q, want the other profile's keys and not the deleted one's", kept)
	}
	var name string
	if err := db.DB.QueryRow(`SELECT COALESCE(GROUP_CONCAT(name), '') FROM configs WHERE name LIKE 'agent_purposes.%'`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "agent_purposes."+keeper+".coding" {
		t.Errorf("agent purpose rows left after the delete: %q, want only the other profile's", name)
	}
	var ws int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM workflows WHERE profile_id = ?`, keeper).Scan(&ws); err != nil || ws != 1 {
		t.Errorf("the other profile's workflow: %d row(s), err %v, want 1", ws, err)
	}
}

// seedDerivedRow inserts one row of the table for profile p, hanging under its workflow wf and
// execution ex: every NOT NULL column without a default gets a value (the profile's own where the
// column names a profile, workflow or execution), unique to p so two profiles never collide.
func seedDerivedRow(t *testing.T, ctx context.Context, conn *sql.Conn, table, p, wf, ex string) {
	t.Helper()
	ci, err := conn.QueryContext(ctx, fmt.Sprintf(`SELECT name, type, "notnull", dflt_value, pk FROM pragma_table_info('%s')`, table))
	if err != nil {
		t.Fatal(err)
	}
	defer ci.Close()
	var cols []string
	var vals []any
	for ci.Next() {
		var name, typ string
		var notnull, pk int
		var dflt *string
		if err := ci.Scan(&name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		_, forced := seedOverrides[table][name]
		own := map[string]string{"profile_id": p, "profile_key": p, "workflow_id": wf, "execution_id": ex, "parent_execution_id": ex, "child_execution_id": ex}[name]
		if own == "" && !forced && (notnull == 0 || dflt != nil) && pk == 0 {
			continue
		}
		if own == "" && !forced && pk > 0 && notnull == 0 && strings.EqualFold(typ, "INTEGER") {
			continue // the rowid
		}
		var v any = "x-" + p
		switch strings.ToUpper(typ) {
		case "INTEGER", "INT", "BOOLEAN", "REAL", "NUMERIC":
			v = 1
		case "BLOB":
			v = []byte{0}
		}
		if own != "" {
			v = own
		}
		if o, ok := seedOverrides[table][name]; ok {
			v = o
		}
		if table == "workflow_run_tokens" && name == "token_hash" { // unique, in the 64-hex form its CHECK accepts
			v = fmt.Sprintf("%x", sha256.Sum256([]byte(p)))
		}
		cols, vals = append(cols, name), append(vals, v)
	}
	ci.Close()
	q := fmt.Sprintf(`INSERT INTO "%s" ("%s") VALUES (%s)`, table, strings.Join(cols, `","`), strings.TrimSuffix(strings.Repeat("?,", len(cols)), ","))
	if _, err := conn.ExecContext(ctx, q, vals...); err != nil {
		t.Fatalf("seeding %s: %v (teach this test to fill it, so the delete is checked for it)", table, err)
	}
}
