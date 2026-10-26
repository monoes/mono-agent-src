package profile

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// runCaptured runs a profile command and returns what it printed to stdout.
func runCaptured(t *testing.T, dbPath string, args ...string) (out string, err error) {
	t.Helper()
	out = captureStdout(t, func() { _, err = runProfileCmd(t, dbPath, args...) })
	return out, err
}

func createTestProfile(t *testing.T, dbPath, name string) string {
	t.Helper()
	out, err := runCaptured(t, dbPath, "create", name)
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	var p profileJSON
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		t.Fatalf("create output %q: %v", out, err)
	}
	return p.ID
}

func TestProfileUpdateRenamesAndSetsIcon(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	id := createTestProfile(t, dbPath, "work")
	if _, err := runProfileCmd(t, dbPath, "update", id, "--name", "office", "--icon", "coder"); err != nil {
		t.Fatalf("update: %v", err)
	}
	out, err := runCaptured(t, dbPath, "get", id)
	if err != nil || !strings.Contains(out, `"name": "office"`) || !strings.Contains(out, `"icon": "coder"`) {
		t.Fatalf("get after update: %v %s", err, out)
	}
	createTestProfile(t, dbPath, "home")
	if _, err := runProfileCmd(t, dbPath, "update", id, "--name", "HOME"); err == nil {
		t.Fatal("renaming onto another profile's name should fail")
	}
	if _, err := runProfileCmd(t, dbPath, "update", id); err == nil {
		t.Fatal("update with no flags should fail")
	}
}

func TestProfileDeleteRemovesItsRowsOnly(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	id := createTestProfile(t, dbPath, "doomed")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{id, "default"} {
		if _, err := db.DB.Exec(`INSERT INTO tasks (profile_id, title, status, position, created_at, updated_at)
			VALUES (?, 'x', 'inbox', 1, '2026-10-05T12:00:00Z', '2026-10-05T12:00:00Z')`, p); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	if _, err := runProfileCmd(t, dbPath, "delete", id); err == nil {
		t.Fatal("delete without --yes should fail")
	}
	out, err := runCaptured(t, dbPath, "delete", id, "--summary")
	if err != nil || !strings.Contains(out, `"tasks"`) {
		t.Fatalf("summary: %v %s", err, out)
	}
	if _, err := runProfileCmd(t, dbPath, "delete", id, "--yes-delete-profile", "doomed"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	db, err = storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var gone, kept int
	db.DB.QueryRow(`SELECT COUNT(*) FROM tasks WHERE profile_id = ?`, id).Scan(&gone)
	db.DB.QueryRow(`SELECT COUNT(*) FROM tasks WHERE profile_id = 'default'`).Scan(&kept)
	if gone != 0 || kept != 1 {
		t.Fatalf("tasks left: deleted profile %d (want 0), default %d (want 1)", gone, kept)
	}
	if _, err := runProfileCmd(t, dbPath, "get", id); err == nil {
		t.Fatal("profile should be gone")
	}
}

func TestProfileDeleteMakesABackupAndKeepsTheFolder(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	id := createTestProfile(t, dbPath, "doomed")
	createTestProfile(t, dbPath, "other")
	folder := t.TempDir()
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`UPDATE profiles SET root_dir = ? WHERE id = ?`, folder, id); err != nil {
		t.Fatal(err)
	}
	db.Close()

	if _, err := runProfileCmd(t, dbPath, "delete", id, "--yes-delete-profile", "wrong"); err == nil {
		t.Fatal("a confirmation that is not the profile's name should fail")
	}
	out, err := runCaptured(t, dbPath, "delete", id, "--yes-delete-profile", "doomed")
	if err != nil {
		t.Fatal(err)
	}
	var res profileImpact
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if _, err := os.Stat(res.Backup); err != nil || !strings.Contains(res.Backup, "pre-delete-profile-") {
		t.Fatalf("backup %q: %v", res.Backup, err)
	}
	if _, err := os.Stat(folder); err != nil || !res.FolderKept {
		t.Fatalf("the profile's folder must be kept: %v", err)
	}
}

func TestProfileUpdateAndDeleteAreForTheOperatorOnly(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	id := createTestProfile(t, dbPath, "victim")
	createTestProfile(t, dbPath, "other")
	t.Setenv(workflow.EnvWorkflowRun, "exec-1")
	for _, args := range [][]string{
		{"update", id, "--name", "renamed"},
		{"delete", id, "--yes-delete-profile", "victim"},
	} {
		if _, err := runProfileCmd(t, dbPath, args...); err == nil || !strings.Contains(err.Error(), workflow.EnvWorkflowRun) {
			t.Errorf("%v from a workflow run: err %v, want a refusal naming %s", args, err, workflow.EnvWorkflowRun)
		}
	}
	// Reading what a delete would remove stays open.
	if _, err := runProfileCmd(t, dbPath, "delete", id, "--summary"); err != nil {
		t.Errorf("--summary from a workflow run: %v", err)
	}
	if _, err := runCaptured(t, dbPath, "get", id); err != nil {
		t.Fatalf("the profile must still exist: %v", err)
	}
}

// seedOverrides gives the columns whose CHECK constraint rejects the dummy value.
var seedOverrides = map[string]map[string]any{
	"applications":           {"kind": "job", "status": "pending"},
	"chat_memory_messages":   {"role": "human"},
	"application_status_log": {"actor": "user"},
	"workflow_executions":    {"status": "COMPLETED"},
	"person_links":           {"relation": "same", "status": "confirmed", "person_a": 1, "person_b": 2},
	"application_fills":      {"status": "filled"},
	"application_tabs":       {"state": "parked"},
	"application_links":      {"kind": "task"},
	"coder_session_commands": {"kind": "send"},
}

// notProfileOwned are tables with a column that looks like a link to a
// profile's workflow or execution but is not one; each needs a reason.
var notProfileOwned = map[string]string{}

// TestDeleteCoversEveryTableOfTheProfile seeds a row in every table the
// delete's scope names (profile_id tables, their cascading children, and the
// tables keyed by workflow or execution), plus settings entries, a workflow
// file and the vault key, deletes the profile, and fails if any of it is left.
// It also fails for a table that refers to a workflow, an execution or a
// profile and is in no delete rule, so a table added later cannot be missed.
func TestDeleteCoversEveryTableOfTheProfile(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	id := createTestProfile(t, dbPath, "doomed")
	createTestProfile(t, dbPath, "other")

	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	scope, err := profileScope(ctx, db.DB)
	if err != nil {
		t.Fatal(err)
	}
	covered := map[string]bool{"profiles": true}
	for _, e := range scope {
		covered[e.Table] = true
	}
	all, _ := queryStrings(ctx, db.DB, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	for _, tbl := range all {
		if covered[tbl] {
			continue
		}
		for _, col := range []string{"profile_id", "workflow_id", "execution_id", "parent_execution_id", "child_execution_id", "profile_key"} {
			if hasColumn(ctx, db.DB, tbl, col) && notProfileOwned[tbl] == "" {
				t.Errorf("table %s has a %s column but no delete rule covers it: add it to derivedScope (or to notProfileOwned with a reason)", tbl, col)
			}
		}
	}

	conn, err := db.DB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	before := map[string]int{}
	for _, e := range scope {
		conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM "`+e.Table+`"`).Scan(new(int))
		var n int
		conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM "`+e.Table+`"`).Scan(&n)
		before[e.Table] = n
		if e.Table == "settings" {
			continue
		}
		cols, vals := []string{}, []any{}
		var pkCols int // a lone INTEGER primary key is the rowid; part of a composite one needs a value
		conn.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM pragma_table_info('%s') WHERE pk > 0`, e.Table)).Scan(&pkCols)
		ci, err := conn.QueryContext(ctx, fmt.Sprintf(`SELECT name, type, "notnull", dflt_value, pk FROM pragma_table_info('%s')`, e.Table))
		if err != nil {
			t.Fatal(err)
		}
		for ci.Next() {
			var name, typ string
			var notnull, pk int
			var dflt *string
			ci.Scan(&name, &typ, &notnull, &dflt, &pk)
			isProfile := name == "profile_id" || (e.Table == "workflow_daily_counters" && name == "profile_key")
			_, forced := seedOverrides[e.Table][name]
			forced = forced || name == "workflow_id" || name == "execution_id"
			if !isProfile && !forced && ((notnull == 0 && pk == 0) || dflt != nil || (pk > 0 && pkCols == 1 && strings.EqualFold(typ, "INTEGER"))) {
				continue
			}
			var v any = "x"
			switch strings.ToUpper(typ) {
			case "INTEGER", "INT", "BOOLEAN", "REAL", "NUMERIC":
				v = 1
			case "BLOB":
				v = []byte{0}
			}
			if isProfile {
				v = id
			}
			if o, ok := seedOverrides[e.Table][name]; ok {
				v = o
			}
			cols, vals = append(cols, name), append(vals, v)
		}
		ci.Close()
		q := fmt.Sprintf(`INSERT INTO "%s" (%s) VALUES (%s)`, e.Table, `"`+strings.Join(cols, `","`)+`"`, strings.TrimSuffix(strings.Repeat("?,", len(cols)), ","))
		if _, err := conn.ExecContext(ctx, q, vals...); err != nil {
			t.Errorf("seeding %s: %v (teach this test to fill it, so the delete is checked for it)", e.Table, err)
		}
	}
	for _, kv := range [][2]string{{"task_agent_access:" + id, "1"}, {"jev." + id + ".chat.model", "m"}, {"jev.someone-else.chat.model", "keep"}} {
		if _, err := conn.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?)`, kv[0], kv[1]); err != nil {
			t.Fatal(err)
		}
	}
	conn.Close()
	db.Close()

	home := os.Getenv("HOME")
	wfDir := filepath.Join(home, ".monoagent", "workflows")
	if err := os.MkdirAll(wfDir, 0o700); err != nil {
		t.Fatal(err)
	}
	wfFile := filepath.Join(wfDir, "x.json")
	if err := os.WriteFile(wfFile, []byte(`{"id":"x","name":"w","profile_id":"`+id+`","nodes":[],"connections":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := keyring.Set("monoagent-vault", "kek-"+id, "00"); err != nil {
		t.Fatal(err)
	}

	if _, err := runProfileCmd(t, dbPath, "delete", id, "--yes-delete-profile", "doomed"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	db, err = storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, e := range scope {
		if e.Table == "settings" {
			continue
		}
		var n int
		if err := db.DB.QueryRow(`SELECT COUNT(*) FROM "` + e.Table + `"`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != before[e.Table] {
			t.Errorf("%s: %d row(s) after the delete, %d before the seeding: the profile's row was left behind", e.Table, n, before[e.Table])
		}
	}
	var keys []string
	keys, _ = queryStrings(ctx, db.DB, `SELECT key FROM settings WHERE key LIKE 'task_agent_access:%' OR key LIKE 'jev.%'`)
	if len(keys) != 1 || keys[0] != "jev.someone-else.chat.model" {
		t.Errorf("settings left after the delete: %v, want only another profile's jev key", keys)
	}
	if _, err := os.Stat(wfFile); !os.IsNotExist(err) {
		t.Errorf("the profile's workflow file was left behind: %v", err)
	}
	if _, err := keyring.Get("monoagent-vault", "kek-"+id); err != keyring.ErrNotFound {
		t.Errorf("the profile's vault key was left in the keychain: %v", err)
	}
}

func TestProfileNamesAndIconsAreValidated(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	id := createTestProfile(t, dbPath, "work")
	other := createTestProfile(t, dbPath, "home")
	bidi := []string{}
	for r := '\u2066'; r <= '\u2069'; r++ {
		bidi = append(bidi, "ab"+string(r)+"cd")
	}
	invisible := []string{"a\u200bb", "a\u200db", "a\u2028b", "\ufeffab", "a\u2060b", "\u200b\u200b", "a\u00a0b", "D\u0435fault"}
	for _, bad := range append(append([]string{}, bidi...), invisible...) {
		if _, err := runProfileCmd(t, dbPath, "update", id, "--name", bad); err == nil {
			t.Errorf("name %q was accepted", bad)
		}
	}
	if _, err := runProfileCmd(t, dbPath, "update", id, "--name", "ＨＯＭＥ"); err == nil {
		t.Error("a full-width spelling of another profile's name was accepted")
	}
	for _, bad := range []string{"", "a\x1b[31mb", "a\nb", "../../etc", "a/b", `a\b`, strings.Repeat("a", 65), "abc\u202edef", other} {
		if _, err := runProfileCmd(t, dbPath, "update", id, "--name", bad); err == nil {
			t.Errorf("name %q was accepted", bad)
		}
	}
	for _, bad := range []string{"../../../x<script>", "A B", "x/y", strings.Repeat("a", 65)} {
		if _, err := runProfileCmd(t, dbPath, "update", id, "--icon", bad); err == nil {
			t.Errorf("icon %q was accepted", bad)
		}
	}
	if _, err := runProfileCmd(t, dbPath, "update", id, "--name", "Büro 2", "--icon", ""); err != nil {
		t.Errorf("a plain name was refused: %v", err)
	}
}

func TestProfileBackupsPurge(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	id := createTestProfile(t, dbPath, "doomed")
	createTestProfile(t, dbPath, "other")
	out, err := runCaptured(t, dbPath, "delete", id, "--yes-delete-profile", "doomed")
	if err != nil {
		t.Fatal(err)
	}
	var res profileImpact
	json.Unmarshal([]byte(out), &res)
	if _, err := runProfileCmd(t, dbPath, "backups", "purge"); err == nil {
		t.Fatal("purge without --yes should fail")
	}
	t.Setenv(workflow.EnvWorkflowRun, "exec-1")
	if _, err := runProfileCmd(t, dbPath, "backups", "purge", "--yes"); err == nil {
		t.Fatal("purge from a workflow run should be refused")
	}
	os.Unsetenv(workflow.EnvWorkflowRun)
	if _, err := runProfileCmd(t, dbPath, "backups", "purge", "--yes"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(res.Backup); !os.IsNotExist(err) {
		t.Fatalf("the backup is still there: %v", err)
	}
}

func TestProfileDeleteRefusesRunningAndQueuedExecutions(t *testing.T) {
	for _, status := range []string{"RUNNING", "QUEUED"} {
		t.Run(status, func(t *testing.T) {
			dbPath := newProfileDocsCLITestDB(t)
			id := createTestProfile(t, dbPath, "busy")
			createTestProfile(t, dbPath, "other")
			db, err := storage.NewDatabase(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			for _, q := range []string{
				`INSERT INTO workflows (id, name, profile_id) VALUES ('w1', 'w', '` + id + `')`,
				`INSERT INTO workflow_executions (id, workflow_id, status, profile_id) VALUES ('e1', 'w1', '` + status + `', '` + id + `')`,
			} {
				if _, err := db.DB.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			defer db.Close()
			if _, err := runProfileCmd(t, dbPath, "delete", id, "--yes-delete-profile", "busy"); err == nil || !strings.Contains(err.Error(), "execution cancel") {
				t.Fatalf("err %v, want a refusal that says how to cancel", err)
			}
			// The check inside the transaction holds on its own: an execution that
			// started after the first check is not deleted under.
			if _, err := deleteProfile(context.Background(), db.DB, id); err == nil {
				t.Fatal("deleteProfile went ahead over a " + status + " execution")
			}
			var n int
			db.DB.QueryRow(`SELECT COUNT(*) FROM workflows WHERE profile_id = ?`, id).Scan(&n)
			if n != 1 {
				t.Errorf("the refused delete removed rows: %d workflows left, want 1", n)
			}
		})
	}
}

func TestWorkflowFilesGoByTheirProfileFieldNotTheirName(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	id := createTestProfile(t, dbPath, "doomed")
	other := createTestProfile(t, dbPath, "other")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO workflows (id, name, profile_id) VALUES ('theirs', 'w', ?), ('mine', 'w', ?)`, other, id); err != nil {
		t.Fatal(err)
	}
	db.Close()
	dir := filepath.Join(os.Getenv("HOME"), ".monoagent", "workflows")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := func(name, wfID, profile string) string {
		path := filepath.Join(dir, name)
		body := `{"id":"` + wfID + `","name":"w","profile_id":"` + profile + `","nodes":[],"connections":[]}`
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	mine := file("mine.json", "mine", id)
	copyOf := file("copyof.json", "mine", id)
	ghost := file("ghost.json", "ghost", id)
	decoy := file("decoy.json", "theirs", id) // claims the deleted profile, carries another profile's workflow
	theirs := file("theirs.json", "theirs", other)
	if _, err := runProfileCmd(t, dbPath, "delete", id, "--yes-delete-profile", "doomed"); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{mine, copyOf, ghost} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s was left behind: %v", filepath.Base(gone), err)
		}
	}
	for _, kept := range []string{decoy, theirs} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("%s was deleted but belongs to another profile's workflow: %v", filepath.Base(kept), err)
		}
	}
}

// `create` holds names to the same rules as `update`, so a rename protection is
// not skipped by creating the name.
func TestProfileCreateValidatesNames(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	createTestProfile(t, dbPath, "Cape")
	createTestProfile(t, dbPath, "Home")
	for _, bad := range []string{
		"a​b", "Dеfault", "default", "Default", "ＨＯＭＥ", "../../etc", "a\x1b[31mb", strings.Repeat("a", 5000),
		"Саре",      // all Cyrillic, spelled like Cape
		"Ηομε",      // all Greek, spelled like Home
		"Home Ηομε", // a Latin word and a Greek word
		"Homeա",     // Latin with Hebrew, Armenian
		"ﬁnance",    // ligature
		"home",      // same as Home
	} {
		if _, err := runProfileCmd(t, dbPath, "create", bad); err == nil {
			t.Errorf("create accepted %q", bad)
		}
	}
	if _, err := runProfileCmd(t, dbPath, "create", "Büro 2"); err != nil {
		t.Errorf("create refused a plain name: %v", err)
	}
	if _, err := runProfileCmd(t, dbPath, "create", "Москва"); err != nil {
		t.Errorf("create refused an all-Cyrillic name that imitates nothing: %v", err)
	}
	if _, err := runProfileCmd(t, dbPath, "create", "Work", "--icon", "../../x"); err == nil {
		t.Error("create accepted a path as an icon")
	}
}

func TestProfileUpdateRefusesLookAlikes(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	createTestProfile(t, dbPath, "Cape")
	id := createTestProfile(t, dbPath, "work")
	for _, bad := range []string{"Саре", "сас", "Ηομε", "Cape ", "Cаpe"} {
		if _, err := runProfileCmd(t, dbPath, "update", id, "--name", bad); err == nil && skeleton(bad) == skeleton("Cape") {
			t.Errorf("update accepted %q, a look-alike of Cape", bad)
		}
	}
}

// A refused delete takes no backup: the check before the backup stands on its own.
func TestRefusedDeleteTakesNoBackup(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	id := createTestProfile(t, dbPath, "busy")
	createTestProfile(t, dbPath, "other")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO workflows (id, name, profile_id) VALUES ('w1', 'w', '` + id + `')`,
		`INSERT INTO workflow_executions (id, workflow_id, status, profile_id) VALUES ('e1', 'w1', 'RUNNING', '` + id + `')`,
	} {
		if _, err := db.DB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	defer db.Close()
	if _, err := runProfileCmd(t, dbPath, "delete", id, "--yes-delete-profile", "busy"); err == nil {
		t.Fatal("the delete went ahead")
	}
	if backups, _ := db.ProfileDeleteBackups(); len(backups) != 0 {
		t.Fatalf("a refused delete left backups: %v", backups)
	}
}

// A file that names no profile goes with a workflow the profile owns in the
// database, and stays when no row of the profile has its id.
func TestWorkflowFileWithoutAProfileFollowsItsRow(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	id := createTestProfile(t, dbPath, "doomed")
	createTestProfile(t, dbPath, "other")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO workflows (id, name, profile_id) VALUES ('owned', 'w', ?)`, id); err != nil {
		t.Fatal(err)
	}
	db.Close()
	dir := filepath.Join(os.Getenv("HOME"), ".monoagent", "workflows")
	os.MkdirAll(dir, 0o700)
	write := func(name, wfID string) string {
		path := filepath.Join(dir, name)
		os.WriteFile(path, []byte(`{"id":"`+wfID+`","name":"w","nodes":[],"connections":[]}`), 0o600)
		return path
	}
	owned, stranger := write("owned.json", "owned"), write("stranger.json", "stranger")
	if _, err := runProfileCmd(t, dbPath, "delete", id, "--yes-delete-profile", "doomed"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(owned); !os.IsNotExist(err) {
		t.Errorf("the profile-less file of the profile's own workflow was left: %v", err)
	}
	if _, err := os.Stat(stranger); err != nil {
		t.Errorf("a profile-less file with no row of the profile was deleted: %v", err)
	}
}

// Ids with LIKE wildcards must not widen the settings delete to another profile.
func TestSettingsDeleteEscapesWildcardsInTheProfileID(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	createTestProfile(t, dbPath, "other")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO profiles (id, name, created_at) VALUES ('a_b', 'under', '2026-10-17T00:00:00Z'), ('axb', 'plain', '2026-10-17T00:00:00Z'), ('p%', 'pct', '2026-10-17T00:00:00Z'), ('pq', 'pq', '2026-10-17T00:00:00Z')`,
		`INSERT INTO settings (key, value) VALUES ('jev.a_b.chat.m', '1'), ('jev.axb.chat.m', '2'), ('jev.p%.chat.m', '3'), ('jev.pq.chat.m', '4')`,
	} {
		if _, err := db.DB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	for _, c := range [][2]string{{"a_b", "under"}, {"p%", "pct"}} {
		if _, err := runProfileCmd(t, dbPath, "delete", c[0], "--yes-delete-profile", c[1]); err != nil {
			t.Fatal(err)
		}
	}
	db, _ = storage.NewDatabase(dbPath)
	defer db.Close()
	keys, _ := queryStrings(context.Background(), db.DB, `SELECT key FROM settings WHERE key LIKE 'jev.%' ORDER BY key`)
	if strings.Join(keys, ",") != "jev.axb.chat.m,jev.pq.chat.m" {
		t.Fatalf("settings left: %v, want only the other profiles' keys", keys)
	}
}

func TestDeleteUnbindsTheProfilesBrowsers(t *testing.T) {
	for _, tc := range []struct {
		name     string
		unbind   func(context.Context, string) (int, error)
		wantN    int
		wantWarn bool
	}{
		{"unbound", nil, 2, false},
		{"fails", nil, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dbPath := newProfileDocsCLITestDB(t)
			id := createTestProfile(t, dbPath, "doomed")
			createTestProfile(t, dbPath, "other")
			var got string
			cfg := &testConfig{DBPath: dbPath, JSONOutput: true}
			d := testDeps(cfg)
			d.UnbindBrowsers = func(_ context.Context, profileID string) (int, error) {
				got = profileID
				if tc.wantWarn {
					return 0, fmt.Errorf("bridge unreachable")
				}
				return 2, nil
			}
			cmd := NewCommand(d)
			cmd.SetArgs([]string{"delete", id, "--yes-delete-profile", "doomed"})
			var runErr error
			out := captureStdout(t, func() { runErr = cmd.Execute() })
			if runErr != nil {
				t.Fatal(runErr)
			}
			var res profileImpact
			if err := json.Unmarshal([]byte(out), &res); err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			if got != id || res.BrowsersUnbound != tc.wantN || (len(res.Warnings) > 0) != tc.wantWarn {
				t.Errorf("unbind called with %q, unbound %d, warnings %v; want %q, %d, warning %v", got, res.BrowsersUnbound, res.Warnings, id, tc.wantN, tc.wantWarn)
			}
		})
	}
}

func TestValidateProfileNameUnits(t *testing.T) {
	for _, ok := range []string{"José", "Zoë", "O'Brien", "O’Brien", "2024", "Q3-2024 plan", "Toyota トヨタ", "Li 李", "iPhone用户", "Ahmed أحمد", "漢 한", "Москва", "Ελλάδα", "ホーム", "Büro 2", "Work 🚀"} {
		if err := validateProfileName(ok); err != nil {
			t.Errorf("%q was refused: %v", ok, err)
		}
	}
	for _, c := range []struct{ name, why string }{
		{"default", "default"}, {"Default", "default"}, {"DEFAULT", "default"},
		{"a\u00a0b", "unusual space"}, {"a\u3000b", "unusual space"}, {"a\u1680b", "unusual space"},
		{"Acme™", "normalization"}, {"Team²", "normalization"}, {"µTorrent", "normalization"}, {"ﬁnance", "normalization"},
		{"Ω-Tech", "mix"}, {"D\u0435fault", "mix"}, {"Home Москва", "mix"}, {"Homeա", "mix"}, {"Home\u13a0", "mix"}, {"Hello\u2c80", "mix"}, {"Hello\ua4d0", "mix"},
	} {
		err := validateProfileName(c.name)
		if err == nil {
			t.Errorf("%q was accepted", c.name)
		} else if !strings.Contains(err.Error(), c.why) {
			t.Errorf("%q refused for another reason than %q: %v", c.name, c.why, err)
		}
	}
}

// The app sends the name together with a new icon: a profile whose own name is
// "default" (or any name an older version allowed) must still take the icon.
func TestUpdateKeepsAnUnchangedNameAndTakesTheIcon(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	var def profileJSON
	out, err := runCaptured(t, dbPath, "get", "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(out), &def); err != nil {
		t.Fatal(err)
	}
	if skeleton(def.Name) != "default" {
		t.Skipf("the seeded profile is named %q, not default", def.Name)
	}
	if _, err := runProfileCmd(t, dbPath, "update", def.ID, "--name", def.Name, "--icon", "coder"); err != nil {
		t.Fatalf("an unchanged name with a new icon: %v", err)
	}
	if _, err := runProfileCmd(t, dbPath, "update", def.ID, "--name", "Default2"); err != nil {
		t.Fatalf("renaming it: %v", err)
	}
	other := createTestProfile(t, dbPath, "work")
	if _, err := runProfileCmd(t, dbPath, "update", other, "--name", "default"); err == nil {
		t.Fatal("renaming another profile to default was accepted")
	}
}

func TestCreateNormalizesToNFC(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	out, err := runCaptured(t, dbPath, "create", "Jose\u0301")
	if err != nil {
		t.Fatalf("an NFD name was refused: %v", err)
	}
	var p profileJSON
	json.Unmarshal([]byte(out), &p)
	if p.Name != "José" {
		t.Fatalf("stored %q, want the NFC form José", p.Name)
	}
}
