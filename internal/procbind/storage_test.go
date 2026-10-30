package procbind

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/storage"
)

// The v2 tables of the peer-credential binding design (section 7): rows are insert-only, and a row
// may be deleted only once it is past retention (7 days after its expiry), with no exempt operation.

const tsLayout = "2006-01-02T15:04:05.000Z"

// okTS and okTS2 are well-formed stored timestamps (the retention rule compares them as text).
const (
	okTS  = "2026-10-30T00:00:00.000Z"
	okTS2 = "2026-10-31T00:00:00.000Z"
)

func ago(d time.Duration) string     { return time.Now().UTC().Add(-d).Format(tsLayout) }
func fromNow(d time.Duration) string { return time.Now().UTC().Add(d).Format(tsLayout) }

const day = 24 * time.Hour

func newDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db.DB
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func execErr(db *sql.DB, q string, args ...any) error {
	_, err := db.Exec(q, args...)
	return err
}

func insertMissionToken(t *testing.T, db *sql.DB, id, expires string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO agent_tokens_v2 (id, profile_id, token_hash, mission_owner, mission_session, mission_task, mission_role, mission_actor, created_at, expires_at, version, mac)
		VALUES (?, 'p1', ?, 'owner', 7, 3, 'worker', 'actor', ?, ?, 2, 'mac')`, id, "hash-"+id, ago(30*day), expires)
}

func insertSessionToken(t *testing.T, db *sql.DB, id, expires string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO coder_session_tokens_v2 (id, profile_id, token_hash, conversation_id, turn_id, created_at, expires_at, version, mac)
		VALUES (?, 'p1', ?, 'conv', 'turn', ?, ?, 2, 'mac')`, id, "hash-"+id, ago(30*day), expires)
}

func insertBinding(t *testing.T, db *sql.DB, id, expires string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO process_bindings (token_id, profile_id, scope, mode, worker_pid, worker_stamp, host_generation, version, created_at, expires_at, mac)
		VALUES (?, 'p1', 'session:7', 'peercred', 4242, 'start:1', 'gen-1', 2, ?, ?, 'mac')`, id, ago(30*day), expires)
}

func insertRevocation(t *testing.T, db *sql.DB, id, created string) {
	insertRevocationOf(t, db, "mission", id, created)
}

func insertRevocationOf(t *testing.T, db *sql.DB, kind, id, created string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO token_revocations_v2 (token_kind, token_id, profile_id, reason, created_at, mac) VALUES (?, ?, 'p1', 'owner ended', ?, 'mac')`, kind, id, created)
}

func insertHost(t *testing.T, db *sql.DB, seq int, started string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO procbind_host (profile_id, seq, generation, started_at, mac) VALUES ('p1', ?, ?, ?, 'mac')`, seq, fmt.Sprintf("gen-%d", seq), started)
}

func TestMigrationIsIdempotent(t *testing.T) {
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := 0; i < 2; i++ {
		if err := db.ApplyMigrations(); err != nil {
			t.Fatalf("pass %d: %v", i+1, err)
		}
	}
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name IN
		('agent_tokens_v2', 'coder_session_tokens_v2', 'process_bindings', 'token_revocations_v2', 'procbind_host')`).Scan(&n); err != nil || n != 5 {
		t.Errorf("tables after two passes: %d %v", n, err)
	}
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND (name LIKE 'trg_agent_tokens_v2_%' OR name LIKE 'trg_coder_session_tokens_v2_%'
		OR name LIKE 'trg_process_bindings_%' OR name LIKE 'trg_token_revocations_v2_%' OR name LIKE 'trg_procbind_host_%')`).Scan(&n); err != nil || n != 15 {
		t.Errorf("triggers after two passes: %d %v", n, err)
	}
}

func TestV2TablesRejectEveryUpdate(t *testing.T) {
	db := newDB(t)
	insertMissionToken(t, db, "m1", fromNow(day))
	insertSessionToken(t, db, "s1", fromNow(day))
	insertBinding(t, db, "m1", fromNow(day))
	insertRevocation(t, db, "m1", ago(day))
	insertHost(t, db, 1, ago(day))
	for table, set := range map[string]string{
		"agent_tokens_v2":         "expires_at = '2099-01-01T00:00:00.000Z'",
		"coder_session_tokens_v2": "expires_at = '2099-01-01T00:00:00.000Z'",
		"process_bindings":        "worker_pid = 1",
		"token_revocations_v2":    "reason = 'edited'",
		"procbind_host":           "generation = 'edited'",
	} {
		for _, verb := range []string{"UPDATE ", "UPDATE OR REPLACE ", "UPDATE OR IGNORE "} {
			err := execErr(db, verb+table+" SET "+set)
			if err == nil || !strings.Contains(err.Error(), "insert-only") {
				t.Errorf("%s%s: not rejected as insert-only: %v", verb, table, err)
			}
		}
	}
}

// A live row, and one that expired less than the window ago, cannot be deleted; one past it can.
func TestV2RowsAreDeletableOnlyPastRetention(t *testing.T) {
	cases := []struct {
		table  string
		insert func(t *testing.T, db *sql.DB, expires string)
		where  string
	}{
		{"agent_tokens_v2", func(t *testing.T, db *sql.DB, e string) { insertMissionToken(t, db, "x", e) }, "id = 'x'"},
		{"coder_session_tokens_v2", func(t *testing.T, db *sql.DB, e string) { insertSessionToken(t, db, "x", e) }, "id = 'x'"},
		{"process_bindings", func(t *testing.T, db *sql.DB, e string) { insertBinding(t, db, "x", e) }, "token_id = 'x'"},
	}
	for _, c := range cases {
		t.Run(c.table, func(t *testing.T) {
			for name, expires := range map[string]string{
				"live":                fromNow(day),
				"expired a day ago":   ago(day),
				"expired 6 days ago":  ago(6 * day),
				"expired just inside": ago(7*day - time.Hour),
			} {
				db := newDB(t)
				c.insert(t, db, expires)
				if err := execErr(db, "DELETE FROM "+c.table+" WHERE "+c.where); err == nil || !strings.Contains(err.Error(), "retention") {
					t.Errorf("%s: DELETE was not rejected as within retention: %v", name, err)
				}
			}
			db := newDB(t)
			c.insert(t, db, ago(8*day))
			if err := execErr(db, "DELETE FROM "+c.table+" WHERE "+c.where); err != nil {
				t.Errorf("a row expired 8 days ago could not be deleted: %v", err)
			}
		})
	}
}

// A revocation follows the token it revokes: it stays until that token is long expired, and a
// revocation whose token row is gone is judged by its own age.
func TestRevocationsAreDeletableOnlyPastTheirTokensRetention(t *testing.T) {
	db := newDB(t)
	const del = `DELETE FROM token_revocations_v2 WHERE token_kind = 'mission' AND token_id = ?`
	const delSession = `DELETE FROM token_revocations_v2 WHERE token_kind = 'session' AND token_id = ?`
	insertMissionToken(t, db, "live", fromNow(day))
	insertRevocation(t, db, "live", ago(30*day)) // old row, live token
	if err := execErr(db, del, "live"); err == nil {
		t.Error("the revocation of a live token was deleted")
	}
	insertSessionToken(t, db, "recent", ago(2*day))
	insertRevocationOf(t, db, "session", "recent", ago(30*day))
	if err := execErr(db, delSession, "recent"); err == nil {
		t.Error("the revocation of a token that expired 2 days ago was deleted")
	}
	insertMissionToken(t, db, "old", ago(9*day))
	insertRevocation(t, db, "old", ago(1*day))
	if err := execErr(db, del, "old"); err != nil {
		t.Errorf("the revocation of a long-expired mission token could not be deleted: %v", err)
	}
	insertSessionToken(t, db, "oldsess", ago(9*day))
	insertRevocationOf(t, db, "session", "oldsess", ago(1*day))
	if err := execErr(db, delSession, "oldsess"); err != nil {
		t.Errorf("the revocation of a long-expired session token could not be deleted: %v", err)
	}
	// no token row at all: the revocation's own age decides
	insertRevocation(t, db, "orphan-new", ago(2*day))
	if err := execErr(db, del, "orphan-new"); err == nil {
		t.Error("a recent orphan revocation was deleted")
	}
	insertRevocation(t, db, "orphan-old", ago(9*day))
	if err := execErr(db, del, "orphan-old"); err != nil {
		t.Errorf("an old orphan revocation could not be deleted: %v", err)
	}
}

// A mission token and a session token may share an id: a revocation is judged by the token of ITS kind.
func TestRevocationFollowsTheTokenOfItsOwnKind(t *testing.T) {
	db := newDB(t)
	insertMissionToken(t, db, "dup", ago(9*day))   // long expired
	insertSessionToken(t, db, "dup", fromNow(day)) // live
	insertRevocationOf(t, db, "session", "dup", ago(30*day))
	if err := execErr(db, `DELETE FROM token_revocations_v2 WHERE token_kind = 'session' AND token_id = 'dup'`); err == nil {
		t.Error("the revocation of a live session token was deleted because a mission token with the same id expired long ago")
	}
	insertRevocationOf(t, db, "mission", "dup", ago(1*day))
	if err := execErr(db, `DELETE FROM token_revocations_v2 WHERE token_kind = 'mission' AND token_id = 'dup'`); err != nil {
		t.Errorf("the revocation of the long-expired mission token could not be deleted: %v", err)
	}
	if err := execErr(db, `INSERT INTO token_revocations_v2 (token_kind, token_id, profile_id, reason, created_at, mac) VALUES ('other', 'x', 'p1', '', '`+okTS+`', 'm')`); err == nil {
		t.Error("a revocation of an unknown token kind was accepted")
	}
}

// The host log: the newest row is never deletable; an older, superseded one only past the window.
func TestHostLogKeepsTheNewestRowAndRecentHistory(t *testing.T) {
	db := newDB(t)
	const del = `DELETE FROM procbind_host WHERE profile_id = 'p1' AND seq = ?`
	insertHost(t, db, 1, ago(30*day))
	if err := execErr(db, del, 1); err == nil {
		t.Error("the only (newest) host row was deleted")
	}
	insertHost(t, db, 2, ago(2*day))
	if err := execErr(db, del, 2); err == nil {
		t.Error("the newest host row was deleted")
	}
	if err := execErr(db, del, 1); err != nil {
		t.Errorf("a superseded host row past the window could not be deleted: %v", err)
	}
	insertHost(t, db, 3, ago(1*day))
	if err := execErr(db, del, 2); err == nil {
		t.Error("a superseded host row inside the window was deleted")
	}
	// another profile's rows do not supersede this one's
	mustExec(t, db, `INSERT INTO procbind_host (profile_id, seq, generation, started_at, mac) VALUES ('p2', 1, 'g', ?, 'mac')`, ago(30*day))
	insertHost(t, db, 4, ago(30*day))
	if err := execErr(db, `DELETE FROM procbind_host WHERE profile_id = 'p2'`); err == nil {
		t.Error("another profile's newest row was deleted")
	}
}

func TestV2TablesEnforceIdentityAndShape(t *testing.T) {
	db := newDB(t)
	insertMissionToken(t, db, "m1", fromNow(day))
	if err := execErr(db, `INSERT INTO agent_tokens_v2 (id, profile_id, token_hash, created_at, expires_at, version, mac) VALUES ('m1', 'p1', 'h2', '`+okTS+`', '`+okTS2+`', 2, 'm')`); err == nil {
		t.Error("a second token with the same id was accepted")
	}
	if err := execErr(db, `INSERT INTO agent_tokens_v2 (id, profile_id, token_hash, created_at, expires_at, version, mac) VALUES ('m2', 'p1', 'hash-m1', '`+okTS+`', '`+okTS2+`', 2, 'm')`); err == nil {
		t.Error("a second token with the same hash was accepted")
	}
	insertBinding(t, db, "m1", fromNow(day))
	if err := execErr(db, `INSERT INTO process_bindings (token_id, profile_id, scope, mode, worker_pid, worker_stamp, host_generation, version, created_at, expires_at, mac)
		VALUES ('m1', 'p1', 's', 'peercred', 1, 's', 'g', 2, '`+okTS+`', '`+okTS2+`', 'm')`); err == nil {
		t.Error("a second binding for one token was accepted")
	}
	if err := execErr(db, `INSERT INTO process_bindings (token_id, profile_id, scope, mode, worker_pid, worker_stamp, host_generation, version, created_at, expires_at, mac)
		VALUES ('m9', 'p1', 's', 'proc', 1, 's', 'g', 2, '`+okTS+`', '`+okTS2+`', 'm')`); err == nil {
		t.Error("a binding in a mode other than peercred was accepted")
	}
	insertHost(t, db, 1, ago(day))
	if err := execErr(db, `INSERT INTO procbind_host (profile_id, seq, generation, started_at, mac) VALUES ('p1', 1, 'again', '`+okTS+`', 'm')`); err == nil {
		t.Error("a second host row with the same seq was accepted")
	}
	insertRevocation(t, db, "m1", ago(day))
	if err := execErr(db, `INSERT INTO token_revocations_v2 (token_kind, token_id, profile_id, reason, created_at, mac) VALUES ('mission', 'm1', 'p1', 'twice', '`+okTS+`', 'm')`); err == nil {
		t.Error("a token was revoked twice")
	}
}

// REPLACE, INSERT OR REPLACE and an upsert replace a row without firing the delete or update triggers
// (recursive triggers are off), so every table also refuses an insert whose key already exists.
func TestNoInsertConflictClauseReplacesARow(t *testing.T) {
	const t0 = "'" + okTS + "'"
	rows := []struct {
		table, insert, name string
		seed                func(t *testing.T, db *sql.DB)
	}{
		{"agent_tokens_v2", "(id, profile_id, token_hash, created_at, expires_at, version, mac) VALUES ('m1', 'p1', 'hash-m1', " + t0 + ", " + t0 + ", 2, 'NEW')", "same id and hash",
			func(t *testing.T, db *sql.DB) { insertMissionToken(t, db, "m1", fromNow(day)) }},
		{"agent_tokens_v2", "(id, profile_id, token_hash, created_at, expires_at, version, mac) VALUES ('m2', 'p1', 'hash-m1', " + t0 + ", " + t0 + ", 2, 'NEW')", "same hash other id",
			func(t *testing.T, db *sql.DB) { insertMissionToken(t, db, "m1", fromNow(day)) }},
		{"coder_session_tokens_v2", "(id, profile_id, token_hash, conversation_id, turn_id, created_at, expires_at, version, mac) VALUES ('s1', 'p1', 'hash-s1', 'c', 't', " + t0 + ", " + t0 + ", 2, 'NEW')", "same id and hash",
			func(t *testing.T, db *sql.DB) { insertSessionToken(t, db, "s1", fromNow(day)) }},
		{"coder_session_tokens_v2", "(id, profile_id, token_hash, conversation_id, turn_id, created_at, expires_at, version, mac) VALUES ('s2', 'p1', 'hash-s1', 'c', 't', " + t0 + ", " + t0 + ", 2, 'NEW')", "same hash other id",
			func(t *testing.T, db *sql.DB) { insertSessionToken(t, db, "s1", fromNow(day)) }},
		{"process_bindings", "(token_id, profile_id, scope, mode, worker_pid, worker_stamp, host_generation, version, created_at, expires_at, mac) VALUES ('m1', 'p1', 's', 'peercred', 1, 's', 'g', 2, " + t0 + ", " + t0 + ", 'NEW')", "same token",
			func(t *testing.T, db *sql.DB) { insertBinding(t, db, "m1", fromNow(day)) }},
		{"token_revocations_v2", "(token_kind, token_id, profile_id, reason, created_at, mac) VALUES ('mission', 'm1', 'p1', 'r', " + t0 + ", 'NEW')", "same kind and token",
			func(t *testing.T, db *sql.DB) { insertRevocation(t, db, "m1", ago(30*day)) }},
		{"procbind_host", "(profile_id, seq, generation, started_at, mac) VALUES ('p1', 1, 'gen', " + t0 + ", 'NEW')", "same profile and seq",
			func(t *testing.T, db *sql.DB) { insertHost(t, db, 1, ago(30*day)) }},
	}
	for _, r := range rows {
		for _, verb := range []string{"INSERT OR REPLACE INTO ", "REPLACE INTO ", "INSERT OR IGNORE INTO ", "INSERT INTO "} {
			t.Run(r.table+"/"+r.name+"/"+strings.TrimSpace(verb), func(t *testing.T) {
				db := newDB(t)
				r.seed(t, db)
				if err := execErr(db, verb+r.table+" "+r.insert); err == nil {
					t.Fatalf("%s%s replaced or duplicated an existing row", verb, r.table)
				}
				var n int
				if err := db.QueryRow(`SELECT COUNT(*) FROM ` + r.table + ` WHERE mac = 'NEW'`).Scan(&n); err != nil || n != 0 {
					t.Errorf("the existing row was overwritten: %d %v", n, err)
				}
			})
		}
	}
	// an upsert goes through the same insert trigger
	db := newDB(t)
	insertHost(t, db, 1, ago(30*day))
	if err := execErr(db, `INSERT INTO procbind_host (profile_id, seq, generation, started_at, mac) VALUES ('p1', 1, 'g', '`+okTS+`', 'NEW')
		ON CONFLICT (profile_id, seq) DO UPDATE SET mac = 'NEW'`); err == nil {
		t.Error("an upsert replaced a host row")
	}
}

// Retention compares timestamps as text, so a column that retention reads must hold exactly the
// stored form: anything else (empty, short, another layout) is refused at INSERT.
// lit renders text as an SQL string literal; an embedded NUL goes through char(0), as a statement cannot carry one.
func lit(s string) string {
	return "'" + strings.ReplaceAll(strings.ReplaceAll(s, "'", "''"), "\x00", "' || char(0) || '") + "'"
}

func TestMalformedTimestampsAreRefused(t *testing.T) {
	bad := []string{"", "x", "2026-10-30", "2026-10-30T00:00:00Z", "2026-10-30T00:00:00.000", "2026-10-30t00:00:00.000Z",
		"2026-10-30T00:00:00.000Z ", " 2026-10-30T00:00:00.000Z", "2026-10-30T00:00:00.0000Z", "ab26-10-30T00:00:00.000Z", "2026-10-30T00:00:00.00aZ",
		// SQLite's GLOB stops at an embedded NUL, so these match the pattern as far as it looks
		"2026-10-30T00:00:00.000Z\x00junk", "2026-10-30T00:00:00.000Z\x00", "2026-10-30T00:00:00.000Z" + strings.Repeat("0", 40),
		"2026-10-30T00:00:00.000Z\x00" + strings.Repeat("9", 40), "\x002026-10-30T00:00:00.000Z"}
	forms := map[string]func(ts string) string{
		"agent_tokens_v2.created_at": func(ts string) string {
			return "INSERT INTO agent_tokens_v2 (id, profile_id, token_hash, created_at, expires_at, version, mac) VALUES ('a', 'p1', 'h', " + lit(ts) + ", '" + okTS + "', 2, 'm')"
		},
		"agent_tokens_v2.expires_at": func(ts string) string {
			return "INSERT INTO agent_tokens_v2 (id, profile_id, token_hash, created_at, expires_at, version, mac) VALUES ('a', 'p1', 'h', '" + okTS + "', " + lit(ts) + ", 2, 'm')"
		},
		"coder_session_tokens_v2.created_at": func(ts string) string {
			return "INSERT INTO coder_session_tokens_v2 (id, profile_id, token_hash, conversation_id, turn_id, created_at, expires_at, version, mac) VALUES ('s', 'p1', 'h', 'c', 't', " + lit(ts) + ", '" + okTS + "', 2, 'm')"
		},
		"coder_session_tokens_v2.expires_at": func(ts string) string {
			return "INSERT INTO coder_session_tokens_v2 (id, profile_id, token_hash, conversation_id, turn_id, created_at, expires_at, version, mac) VALUES ('s', 'p1', 'h', 'c', 't', '" + okTS + "', " + lit(ts) + ", 2, 'm')"
		},
		"process_bindings.created_at": func(ts string) string {
			return "INSERT INTO process_bindings (token_id, profile_id, scope, mode, worker_pid, worker_stamp, host_generation, version, created_at, expires_at, mac) VALUES ('b', 'p1', 's', 'peercred', 1, 's', 'g', 2, " + lit(ts) + ", '" + okTS + "', 'm')"
		},
		"process_bindings.expires_at": func(ts string) string {
			return "INSERT INTO process_bindings (token_id, profile_id, scope, mode, worker_pid, worker_stamp, host_generation, version, created_at, expires_at, mac) VALUES ('b', 'p1', 's', 'peercred', 1, 's', 'g', 2, '" + okTS + "', " + lit(ts) + ", 'm')"
		},
		"token_revocations_v2.created_at": func(ts string) string {
			return "INSERT INTO token_revocations_v2 (token_kind, token_id, profile_id, reason, created_at, mac) VALUES ('mission', 'r', 'p1', '', " + lit(ts) + ", 'm')"
		},
		"procbind_host.started_at": func(ts string) string {
			return "INSERT INTO procbind_host (profile_id, seq, generation, started_at, mac) VALUES ('p1', 1, 'g', " + lit(ts) + ", 'm')"
		},
	}
	db := newDB(t)
	// every attempt runs in a transaction that is rolled back, so one database serves them all
	try := func(q string) error {
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		_, err = tx.Exec(q)
		return err
	}
	for name, q := range forms {
		if err := try(q(okTS)); err != nil {
			t.Fatalf("%s: a well-formed timestamp was refused: %v", name, err)
		}
		for _, ts := range bad {
			if err := try(q(ts)); err == nil {
				t.Errorf("%s: %q was accepted", name, ts)
			}
		}
	}
}

// The new tables do not touch the legacy ones: a legacy token table has no v2 rows in it.
func TestV2TokensAreNotInTheLegacyTables(t *testing.T) {
	db := newDB(t)
	insertMissionToken(t, db, "m1", fromNow(day))
	insertSessionToken(t, db, "s1", fromNow(day))
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM agent_tokens WHERE token_hash IN ('hash-m1', 'hash-s1')`).Scan(&n); err != nil || n != 0 {
		t.Errorf("v2 tokens appear in agent_tokens: %d %v", n, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM coder_session_tokens WHERE token_hash IN ('hash-m1', 'hash-s1')`).Scan(&n); err != nil || n != 0 {
		t.Errorf("v2 tokens appear in coder_session_tokens: %d %v", n, err)
	}
}
