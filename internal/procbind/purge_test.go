package procbind

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

func count(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func has(t *testing.T, db *sql.DB, table, col, val string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE `+col+` = ?`, val).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func purge(t *testing.T, db *sql.DB) Purged {
	t.Helper()
	p, err := PurgeRetained(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// Rows are removed once their expiry is more than the window past, never before, in every table that carries one.
func TestPurgeRemovesOnlyRowsPastTheWindow(t *testing.T) {
	db := newDB(t)
	for _, c := range []struct {
		id      string
		expires string
		gone    bool
	}{
		{"future", fromNow(day), false},
		{"expired-1d", ago(day), false},
		{"just-inside", ago(RetentionDays*day - time.Hour), false},
		{"just-past", ago(RetentionDays*day + time.Hour), true},
		{"old", ago(30 * day), true},
	} {
		insertMissionToken(t, db, "m-"+c.id, c.expires)
		insertSessionToken(t, db, "s-"+c.id, c.expires)
		insertBinding(t, db, "b-"+c.id, c.expires)
	}
	p := purge(t, db)
	if p.MissionTokens != 2 || p.SessionTokens != 2 || p.Bindings != 2 {
		t.Errorf("purged %+v, want 2 of each", p)
	}
	for _, c := range []struct {
		id   string
		gone bool
	}{{"future", false}, {"expired-1d", false}, {"just-inside", false}, {"just-past", true}, {"old", true}} {
		for _, x := range []struct{ table, col, prefix string }{
			{"agent_tokens_v2", "id", "m-"}, {"coder_session_tokens_v2", "id", "s-"}, {"process_bindings", "token_id", "b-"},
		} {
			if got := has(t, db, x.table, x.col, x.prefix+c.id); got == c.gone {
				t.Errorf("%s %s: present=%v, want %v", x.table, c.id, got, !c.gone)
			}
		}
	}
	if again := purge(t, db); again.Total() != 0 {
		t.Errorf("a second pass removed %+v", again)
	}
}

// A revocation is judged by its token's expiry. A revocation that is the only thing marking an unexpired token dead stays
// (verification reads it); one whose token row is gone is judged by its own age.
func TestPurgeKeepsARevocationWhileItsTokenCouldStillBeUsed(t *testing.T) {
	db := newDB(t)
	// an unexpired token, revoked long ago in terms of the row's age: the revocation is what kills it
	insertMissionToken(t, db, "live", fromNow(day))
	insertRevocation(t, db, "live", ago(30*day))
	insertSessionToken(t, db, "live-s", fromNow(day))
	insertRevocationOf(t, db, "session", "live-s", ago(30*day))
	// a token that expired a day ago
	insertMissionToken(t, db, "recent", ago(day))
	insertRevocation(t, db, "recent", ago(30*day))
	// a token past the window: its revocation goes with it
	insertMissionToken(t, db, "old", ago(10*day))
	insertRevocation(t, db, "old", ago(30*day))
	insertSessionToken(t, db, "old-s", ago(10*day))
	insertRevocationOf(t, db, "session", "old-s", ago(30*day))
	// a revocation with no token row: its own age decides
	insertRevocation(t, db, "orphan-old", ago(10*day))
	insertRevocation(t, db, "orphan-new", ago(day))

	p := purge(t, db)
	if p.Revocations != 3 {
		t.Errorf("revocations purged = %d, want 3 (old, old-s, orphan-old)", p.Revocations)
	}
	for _, id := range []string{"live", "live-s", "recent", "orphan-new"} {
		if !has(t, db, "token_revocations_v2", "token_id", id) {
			t.Errorf("the revocation of %s was removed", id)
		}
	}
	for _, id := range []string{"old", "old-s", "orphan-old"} {
		if has(t, db, "token_revocations_v2", "token_id", id) {
			t.Errorf("the revocation of %s was kept", id)
		}
	}
	// the token a surviving revocation kills is itself still there
	if !has(t, db, "agent_tokens_v2", "id", "live") || !has(t, db, "agent_tokens_v2", "id", "recent") {
		t.Error("an unexpired or recently expired token was removed")
	}
	// the two kinds share nothing: a session revocation is judged by the session token, not a mission token of the same id
	insertMissionToken(t, db, "shared", ago(10*day))
	insertSessionToken(t, db, "shared", fromNow(day))
	insertRevocationOf(t, db, "session", "shared", ago(30*day))
	purge(t, db)
	if !has(t, db, "token_revocations_v2", "token_id", "shared") {
		t.Error("a session revocation was judged by a mission token of the same id")
	}
}

// The newest host row of a profile stays for good; an older one goes once it is superseded and past the window.
func TestPurgeKeepsTheNewestHostRowAndOnlyOldSupersededOnes(t *testing.T) {
	db := newDB(t)
	insertHost(t, db, 1, ago(30*day))
	insertHost(t, db, 2, ago(20*day))
	insertHost(t, db, 3, ago(3*day)) // superseded by 4 but inside the window
	insertHost(t, db, 4, ago(2*day)) // the newest
	mustExec(t, db, `INSERT INTO procbind_host (profile_id, seq, generation, started_at, mac) VALUES ('p2', 1, 'g', ?, 'mac')`, ago(60*day))
	p := purge(t, db)
	if p.HostRows != 2 {
		t.Errorf("host rows purged = %d, want 2 (seq 1 and 2 of p1)", p.HostRows)
	}
	var seqs []string
	rows, err := db.Query(`SELECT profile_id || ':' || seq FROM procbind_host ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		seqs = append(seqs, s)
	}
	rows.Close()
	if strings.Join(seqs, ",") != "p1:3,p1:4,p2:1" {
		t.Errorf("host rows left = %v, want p1:3 (inside the window), p1:4 (newest) and p2:1 (the only row of its profile, 60 days old)", seqs)
	}
	// a superseded row inside the window stays until it is past it
	if again := purge(t, db); again.Total() != 0 {
		t.Errorf("a second pass removed %+v", again)
	}
}

// The sweep needs no key, no profile and no other table: rows of a profile that no longer exists go the same way.
func TestPurgeWorksWithoutAnyProfileOrKey(t *testing.T) {
	db := newDB(t)
	mustExec(t, db, `INSERT INTO agent_tokens_v2 (id, profile_id, token_hash, created_at, expires_at, version, mac) VALUES ('gone', 'deleted-profile', 'h', ?, ?, 2, 'm')`, ago(40*day), ago(30*day))
	if p := purge(t, db); p.MissionTokens != 1 {
		t.Errorf("purged %+v", p)
	}
}

// A backlog larger than one batch is removed completely, in bounded statements.
func TestPurgeRemovesABacklogInBatches(t *testing.T) {
	old := purgeBatch
	purgeBatch = 7
	defer func() { purgeBatch = old }()
	db := newDB(t)
	for i := 0; i < 30; i++ {
		insertMissionToken(t, db, fmt.Sprintf("m%d", i), ago(20*day))
		insertBinding(t, db, fmt.Sprintf("m%d", i), ago(20*day))
		insertRevocation(t, db, fmt.Sprintf("m%d", i), ago(20*day))
	}
	insertMissionToken(t, db, "keep", fromNow(day))
	p := purge(t, db)
	if p.MissionTokens != 30 || p.Bindings != 30 || p.Revocations != 30 {
		t.Errorf("purged %+v, want 30 of each", p)
	}
	if count(t, db, "agent_tokens_v2") != 1 || count(t, db, "process_bindings") != 0 || count(t, db, "token_revocations_v2") != 0 {
		t.Error("rows left over")
	}
}

// Storage after many issue and expire cycles stays at what the window holds: with a sweep after each one, never more
// than a window's worth of rows is left.
func TestStorageStaysBoundedAcrossCycles(t *testing.T) {
	db := newDB(t)
	for i := 20; i >= 1; i-- {
		insertMissionToken(t, db, fmt.Sprintf("c%d", i), ago(time.Duration(i)*day-30*time.Minute))
		purge(t, db)
		if n := count(t, db, "agent_tokens_v2"); n > RetentionDays {
			t.Fatalf("after cycle %d the table holds %d rows, more than the %d days of the window", i, n, RetentionDays)
		}
	}
	if n := count(t, db, "agent_tokens_v2"); n != RetentionDays {
		t.Errorf("rows left = %d, want %d (the tokens that expired within the window)", n, RetentionDays)
	}
}

// A trigger that refuses is an error the caller sees, not a panic, and what was removed before it is reported.
func TestPurgeReportsATriggerRefusal(t *testing.T) {
	db := newDB(t)
	insertRevocation(t, db, "r", ago(30*day))
	insertBinding(t, db, "b", ago(30*day))
	mustExec(t, db, `CREATE TRIGGER trg_test_refuse BEFORE DELETE ON process_bindings BEGIN SELECT RAISE(ABORT, 'refused'); END`)
	p, err := PurgeRetained(context.Background(), db)
	if err == nil || !strings.Contains(err.Error(), "process_bindings") {
		t.Fatalf("err = %v, want the refusal naming process_bindings", err)
	}
	if p.Revocations != 1 || p.Bindings != 0 || count(t, db, "process_bindings") != 1 {
		t.Errorf("purged %+v, the revocation should have gone and the binding stayed", p)
	}
}

// The sweep's window is the triggers' window: every retention literal in migration 148 equals RetentionDays, and the cutoff
// the sweep uses is built from the same number.
func TestPurgeWindowIsTheTriggersWindow(t *testing.T) {
	raw, err := os.ReadFile("../../data/migrations/148_procbind_v2_tables.sql")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`'now', '-(\d+) days'`)
	found := re.FindAllStringSubmatch(string(raw), -1)
	if len(found) < 5 {
		t.Fatalf("found %d retention literals in the migration, want one per table", len(found))
	}
	for _, m := range found {
		if m[1] != fmt.Sprint(RetentionDays) {
			t.Errorf("the migration says %s days, RetentionDays is %d", m[1], RetentionDays)
		}
	}
	if !strings.Contains(cutoffSQL, fmt.Sprintf("'-%d days'", RetentionDays)) || !strings.Contains(cutoffSQL, "'now'") {
		t.Errorf("cutoffSQL = %s", cutoffSQL)
	}
}

// Only the database's clock decides: the sweep's source names no Go time.
func TestPurgeUsesNoGoClock(t *testing.T) {
	src, err := os.ReadFile("purge.go")
	if err != nil {
		t.Fatal(err)
	}
	code := regexp.MustCompile(`(?m)^\s*//.*$`).ReplaceAllString(string(src), "")
	for _, bad := range []string{"time.", `"time"`} {
		if strings.Contains(code, bad) {
			t.Errorf("purge.go uses %s: eligibility must come from the database's clock only", bad)
		}
	}
}
