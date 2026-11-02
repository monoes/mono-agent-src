package storage

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/data"
)

const resTable = "task_session_reservations"

// resDB is a fully migrated database with a profile "p" and a session 1.
func resDB(t *testing.T) *Database {
	t.Helper()
	db, err := NewDatabase(filepath.Join(t.TempDir(), "res.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	seedResSession(t, db.DB)
	return db
}

func seedResSession(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, q := range []string{
		`INSERT INTO profiles (id, name) VALUES ('p', 'P')`,
		`INSERT INTO task_sessions (id, profile_id, title, created_by, created_at, updated_at) VALUES (1, 'p', 's', 'you', 'x', 'x')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

const goodTS = "2026-10-05T12:00:00Z"

// resRow is a good row; a test overrides some of its columns.
func resRow(over map[string]any) (string, []any) {
	row := map[string]any{"profile_id": "p", "session_id": 1, "worker_ref": "w", "owner_id": "o", "amount_usd": 1.0, "bounded": 1,
		"state": "live", "lease_until": goodTS, "heartbeat_at": goodTS, "created_at": goodTS}
	for k, v := range over {
		row[k] = v
	}
	var cols, marks []string
	var args []any
	for k, v := range row {
		cols, marks, args = append(cols, k), append(marks, "?"), append(args, v)
	}
	return "(" + strings.Join(cols, ",") + ") VALUES (" + strings.Join(marks, ",") + ")", args
}

func insertRes(db *sql.DB, verb string, over map[string]any) (sql.Result, error) {
	body, args := resRow(over)
	return db.Exec(verb+" INTO "+resTable+" "+body, args...)
}

func mustRes(t *testing.T, db *sql.DB, over map[string]any) int64 {
	t.Helper()
	r, err := insertRes(db, "INSERT", over)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := r.LastInsertId()
	return id
}

func wantRefused(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: accepted", what)
	}
}

func inf() float64 { z := 0.0; return 1 / z }

func TestReservationMigrationAppliesTwiceAndOnAFreshAndAHeadDatabase(t *testing.T) {
	// head-version database: everything but 149 applied, with data, then the pending one.
	db, err := NewDatabase(filepath.Join(t.TempDir(), "head.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	applyMigrationsBelow(t, db, 149)
	if _, err := db.DB.Exec(`CREATE TABLE keepme (v TEXT); INSERT INTO keepme VALUES ('precious')`); err != nil {
		t.Fatal(err)
	}
	seedResSession(t, db.DB)
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	var kept string
	if err := db.DB.QueryRow(`SELECT v FROM keepme`).Scan(&kept); err != nil || kept != "precious" {
		t.Fatalf("data after the migration: %q, %v", kept, err)
	}
	mustRes(t, db.DB, nil)
	// again: the runner is a no-op, and the file's own statements are idempotent when run over a live table.
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	raw, err := data.MigrationsFS.ReadFile("migrations/149_task_session_reservations.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range splitStatements(string(raw)) {
		if _, err := db.DB.Exec(s); err != nil {
			t.Fatalf("re-running %q: %v", s, err)
		}
	}
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM ` + resTable).Scan(&n); err != nil || n != 1 {
		t.Fatalf("rows after re-running: %d, %v", n, err)
	}
	// fresh
	fresh := resDB(t)
	mustRes(t, fresh.DB, nil)
}

func TestReservationTriggersSurviveEveryMigration(t *testing.T) {
	db := resDB(t)
	rows, err := db.DB.Query(`SELECT name FROM sqlite_master WHERE type = 'trigger' AND tbl_name = ?`, resTable)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var n string
		rows.Scan(&n)
		got = append(got, n)
	}
	sort.Strings(got)
	var want []string
	for _, s := range []string{"no_replace", "generation", "starts_live", "pinned", "state", "terminal", "grow", "expired_frozen", "no_delete", "profile_tie", "auto_id_only", "ended_live"} {
		want = append(want, "trg_"+resTable+"_"+s)
	}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("triggers = %v, want %v", got, want)
	}
}

func TestReservationMigrationSplitsIntoTwelveWholeTriggers(t *testing.T) {
	raw, err := data.MigrationsFS.ReadFile("migrations/149_task_session_reservations.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := splitStatementsChecked(string(raw)); err != nil {
		t.Fatal(err)
	}
	triggers, tables := 0, 0
	for _, s := range splitStatements(string(raw)) {
		up := strings.ToUpper(s)
		switch {
		case strings.Contains(up, "CREATE TRIGGER"):
			triggers++
			if !strings.HasSuffix(up, "END") || strings.Count(up, "CREATE TRIGGER") != 1 {
				t.Errorf("a trigger was cut or glued: %q", s)
			}
		case strings.Contains(up, "CREATE TABLE"):
			tables++
		}
	}
	if triggers != 12 || tables != 1 {
		t.Errorf("%d triggers and %d tables, want 12 and 1", triggers, tables)
	}
}

// Every timestamp column holds the exact 20-byte form: ”, a NUL, a wrong length or shape, a wrong type is refused.
func TestReservationTimestampsAreHeldToTheirShape(t *testing.T) {
	db := resDB(t).DB
	bad := map[string]any{
		"empty":            "",
		"NUL only":         "\x00",
		"NUL in the shape": "2026-10-05T12:00:00\x00",
		"NUL then Z":       "2026-10-05T12:00:0\x00Z",
		"valid then NUL":   goodTS + "\x00",
		"NUL then valid":   "\x00" + goodTS,
		"valid then junk":  goodTS + "junk",
		"milliseconds":     "2026-10-05T12:00:00.000Z",
		"no Z":             "2026-10-05T12:00:00+",
		"letters":          "2026-10-05T12:00:0aZ",
		"date only":        "2026-10-05",
		"a number":         20261005120000,
		"a blob":           []byte(goodTS),
	}
	for _, col := range []string{"lease_until", "heartbeat_at", "created_at"} {
		for name, v := range bad {
			_, err := insertRes(db, "INSERT", map[string]any{col: v})
			wantRefused(t, col+" "+name, err)
		}
	}
	// ended_at may be '' (the default) but never another bad shape, on insert or update.
	for name, v := range bad {
		if name == "empty" {
			continue
		}
		_, err := insertRes(db, "INSERT", map[string]any{"ended_at": v})
		wantRefused(t, "ended_at "+name, err)
	}
	id := mustRes(t, db, nil)
	for name, v := range bad {
		for _, col := range []string{"lease_until", "heartbeat_at", "ended_at"} {
			if name == "empty" && col == "ended_at" {
				continue
			}
			_, err := db.Exec(`UPDATE `+resTable+` SET `+col+` = ? WHERE id = ?`, v, id)
			wantRefused(t, "update "+col+" "+name, err)
		}
	}
}

func TestReservationTextAndAmountColumnsAreChecked(t *testing.T) {
	db := resDB(t).DB
	long := strings.Repeat("x", 257)
	for name, over := range map[string]map[string]any{
		"owner empty":        {"owner_id": ""},
		"owner NUL":          {"owner_id": "a\x00b"},
		"owner too long":     {"owner_id": strings.Repeat("o", 129)},
		"worker empty":       {"worker_ref": ""},
		"worker NUL":         {"worker_ref": "a\x00"},
		"worker too long":    {"worker_ref": long},
		"end note NUL":       {"end_note": "a\x00b"},
		"negative amount":    {"amount_usd": -0.01},
		"huge amount":        {"amount_usd": 1e16},
		"huge charged":       {"charged_usd": 1e16},
		"infinite amount":    {"amount_usd": inf()},
		"negative charged":   {"charged_usd": -1},
		"bad state":          {"state": "nope"},
		"bounded 2":          {"bounded": 2},
		"unbounded w/amount": {"bounded": 0, "amount_usd": 1.0},
		"no session":         {"session_id": 99},
		"no profile":         {"profile_id": "ghost"},
	} {
		_, err := insertRes(db, "INSERT", over)
		wantRefused(t, name, err)
	}
	mustRes(t, db, map[string]any{"bounded": 0, "amount_usd": 0.0})
}

func TestReservationInsertMustStartLiveAndUncharged(t *testing.T) {
	db := resDB(t).DB
	for name, over := range map[string]map[string]any{
		"settled": {"state": "settled"}, "expired": {"state": "expired"}, "charged": {"charged_usd": 0.5}, "ended": {"ended_at": goodTS},
	} {
		_, err := insertRes(db, "INSERT", over)
		wantRefused(t, name, err)
	}
}

// Every form that replaces a row without firing the DELETE or UPDATE triggers is refused, and the row is untouched.
func TestReservationRowsCannotBeReplacedByAnyForm(t *testing.T) {
	db := resDB(t).DB
	id := mustRes(t, db, nil)
	mustRes(t, db, nil)
	check := func(what string, err error) {
		t.Helper()
		wantRefused(t, what, err)
		var owner string
		var amount float64
		if e := db.QueryRow(`SELECT owner_id, amount_usd FROM `+resTable+` WHERE id = ?`, id).Scan(&owner, &amount); e != nil || owner != "o" || amount != 1 {
			t.Errorf("%s changed the row: %q %v %v", what, owner, amount, e)
		}
	}
	over := map[string]any{"id": id, "owner_id": "thief", "amount_usd": 99.0}
	_, err := insertRes(db, "INSERT OR REPLACE", over)
	check("INSERT OR REPLACE", err)
	_, err = insertRes(db, "REPLACE", over)
	check("REPLACE INTO", err)
	body, args := resRow(over)
	_, err = db.Exec(`INSERT INTO `+resTable+` `+body+` ON CONFLICT(id) DO UPDATE SET owner_id = 'thief', amount_usd = 99`, args...)
	check("ON CONFLICT DO UPDATE", err)
	_, err = db.Exec(`UPDATE OR REPLACE `+resTable+` SET id = 2, owner_id = 'thief' WHERE id = ?`, id)
	check("UPDATE OR REPLACE onto another id", err)
	_, err = db.Exec(`UPDATE OR REPLACE `+resTable+` SET owner_id = 'thief' WHERE id = ?`, id)
	check("UPDATE OR REPLACE of the owner", err)
	_, err = db.Exec(`INSERT OR IGNORE INTO `+resTable+` `+body, args...)
	check("INSERT OR IGNORE", err)
}

// A generation is never reused, not even by an explicit id after the row's session was removed.
func TestReservationGenerationIsNeverReused(t *testing.T) {
	db := resDB(t).DB
	mustSession2(t, db)
	a := mustRes(t, db, map[string]any{"session_id": 2})
	if _, err := db.Exec(`DELETE FROM task_sessions WHERE id = 2`); err != nil {
		t.Fatal(err)
	}
	_, err := insertRes(db, "INSERT", map[string]any{"id": a})
	wantRefused(t, "the id of a removed row", err)
	_, err = insertRes(db, "INSERT OR REPLACE", map[string]any{"id": a - 1})
	wantRefused(t, "an id below the highest issued", err)
	if b := mustRes(t, db, nil); b <= a {
		t.Fatalf("next generation %d, not above %d", b, a)
	}
}

func TestReservationIdentityIsPinned(t *testing.T) {
	db := resDB(t).DB
	id := mustRes(t, db, nil)
	for col, v := range map[string]any{"id": 77, "profile_id": "p", "session_id": 1, "worker_ref": "z", "owner_id": "z", "amount_usd": 2.0, "bounded": 0, "created_at": goodTS} {
		_, err := db.Exec(`UPDATE `+resTable+` SET `+col+` = ? WHERE id = ?`, v, id) // even to the same value
		wantRefused(t, "update "+col, err)
	}
}

func TestReservationStatesOnlyMoveForward(t *testing.T) {
	db := resDB(t).DB
	to := func(id int64, st string) error {
		_, err := db.Exec(`UPDATE `+resTable+` SET state = ? WHERE id = ?`, st, id)
		return err
	}
	for _, end := range []string{"settled", "released"} {
		id := mustRes(t, db, nil)
		if err := to(id, end); err != nil {
			t.Fatalf("live -> %s: %v", end, err)
		}
		for _, back := range []string{"live", "settled", "released", "expired", "resolved"} {
			wantRefused(t, end+" -> "+back, to(id, back))
		}
	}
	id := mustRes(t, db, nil)
	wantRefused(t, "live -> resolved", to(id, "resolved"))
	if err := to(id, "expired"); err != nil {
		t.Fatal(err)
	}
	wantRefused(t, "expired -> live", to(id, "live"))
	wantRefused(t, "expired -> settled", to(id, "settled"))
	wantRefused(t, "expired -> released", to(id, "released"))
	if err := to(id, "resolved"); err != nil {
		t.Fatalf("expired -> resolved: %v", err)
	}
	// terminal rows are frozen, any column
	for _, q := range []string{`UPDATE ` + resTable + ` SET end_note = 'x' WHERE id = ?`, `UPDATE ` + resTable + ` SET charged_usd = 5 WHERE id = ?`, `UPDATE ` + resTable + ` SET ended_at = '` + goodTS + `' WHERE id = ?`} {
		_, err := db.Exec(q, id)
		wantRefused(t, q, err)
	}
	wantRefused(t, "resolved -> live", to(id, "live"))
}

func TestReservationChargedAndLeaseOnlyGrow(t *testing.T) {
	db := resDB(t).DB
	id := mustRes(t, db, nil)
	if _, err := db.Exec(`UPDATE `+resTable+` SET charged_usd = 0.5, lease_until = '2026-10-05T12:02:00Z' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	_, err := db.Exec(`UPDATE `+resTable+` SET charged_usd = 0.4 WHERE id = ?`, id)
	wantRefused(t, "charged down", err)
	_, err = db.Exec(`UPDATE `+resTable+` SET lease_until = '2026-10-05T12:01:59Z' WHERE id = ?`, id)
	wantRefused(t, "lease back", err)
	if _, err := db.Exec(`UPDATE `+resTable+` SET charged_usd = 0.5, lease_until = '2026-10-05T12:02:00Z' WHERE id = ?`, id); err != nil {
		t.Fatalf("an unchanged value is not a decrease: %v", err)
	}
}

// An expired row is frozen except to be resolved: it keeps holding exactly what it held.
func TestReservationExpiredRowIsFrozenUntilResolved(t *testing.T) {
	db := resDB(t).DB
	id := mustRes(t, db, nil)
	if _, err := db.Exec(`UPDATE `+resTable+` SET state = 'expired' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`SET charged_usd = 0.5`, `SET lease_until = '2026-10-05T13:00:00Z'`, `SET heartbeat_at = '2026-10-05T13:00:00Z'`, `SET end_note = 'x'`, `SET state = 'expired'`} {
		_, err := db.Exec(`UPDATE `+resTable+` `+q+` WHERE id = ?`, id)
		wantRefused(t, q, err)
	}
	if _, err := db.Exec(`UPDATE `+resTable+` SET state = 'resolved', charged_usd = 0.5, ended_at = ?, end_note = 'ok' WHERE id = ?`, goodTS, id); err != nil {
		t.Fatalf("resolving: %v", err)
	}
}

// Removing a session or a profile removes its reservations (the intended way for the money to return).
func TestReservationsGoWithTheirSessionAndProfile(t *testing.T) {
	db := resDB(t).DB
	mustRes(t, db, nil)
	if _, err := db.Exec(`INSERT INTO task_sessions (id, profile_id, title, created_by, created_at, updated_at) VALUES (2, 'p', 's', 'you', 'x', 'x')`); err != nil {
		t.Fatal(err)
	}
	mustRes(t, db, map[string]any{"session_id": 2})
	if _, err := db.Exec(`DELETE FROM ` + "task_sessions" + ` WHERE id = 2`); err != nil {
		t.Fatal(err)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM ` + resTable).Scan(&n)
	if n != 1 {
		t.Fatalf("%d rows after removing one session, want 1", n)
	}
	if _, err := db.Exec(`DELETE FROM profiles WHERE id = 'p'`); err != nil {
		t.Fatal(err)
	}
	db.QueryRow(`SELECT COUNT(*) FROM ` + resTable).Scan(&n)
	if n != 0 {
		t.Fatalf("%d rows after removing the profile", n)
	}
}
