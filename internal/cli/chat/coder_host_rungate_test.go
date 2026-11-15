package chat

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/daemonhb"
)

// hostWithInfo makes a host look running (its lock held) with the given host info file ("" writes none).
func hostWithInfo(t *testing.T, info string) string {
	t.Helper()
	dbPath := newChatCLITestDB(t)
	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true})
	if err := os.MkdirAll(hostDir(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	release, err := daemonhb.LockFile(hostLockPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	if info != "" {
		if err := os.WriteFile(filepath.Join(hostDir(cfg), hostInfoFile), []byte(info), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dbPath
}

// A running host that does not publish the run-gate marker is one built before the default-on binding: its dispatch gate reads an
// unset setting as OFF and refuses every turn of a run the new start made bound. The start must say so before it makes a run.
func TestAHostWithoutTheRunGateMarkerIsStaleForAnOpenEndedStart(t *testing.T) {
	dbPath := hostWithInfo(t, `{"pid":1,"instance":"host-old","childTokens":true}`)
	why := StaleHostReason(dbPath, StaleThenStart)
	for _, want := range []string{"older", "coder host stop", "doomsday start"} {
		if !strings.Contains(why, want) {
			t.Fatalf("reason %q lacks %q", why, want)
		}
	}
}

func TestAHostThatPublishesTheRunGateMarkerAndNoHostAreNotStale(t *testing.T) {
	raw, _ := json.Marshal(hostInfo{PID: 1, Instance: "host-new", RunGate: runGateVersion})
	if why := StaleHostReason(hostWithInfo(t, string(raw)), StaleThenSend); why != "" {
		t.Fatalf("a current host is stale: %q", why)
	}
	cfg := testDeps(&testConfig{DBPath: newChatCLITestDB(t), ProfileID: "default", JSONOutput: true})
	if why := StaleHostReason(cfg.DBPath(), StaleThenSend); why != "" {
		t.Fatalf("no host running is stale: %q", why)
	}
	// a host that holds the lock but has not written its info yet is not called stale (it is starting; the marker is written first)
	if why := StaleHostReason(hostWithInfo(t, ""), StaleThenSend); why != "" {
		t.Fatalf("a starting host is stale: %q", why)
	}
}

func commandRows(t *testing.T, cfg *Deps) int {
	t.Helper()
	db, err := cfg.OpenDB()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM coder_session_commands`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Every dispatch path that queues a command for the host goes through the same guard: with a stale host running, `coder sessions
// send` (a plain or a mission chat: enqueue), the bounded person's send and an answer (enqueueBounded) fail BEFORE the command is
// queued, with the exact command, instead of queueing a turn the old host fails ("placing a turn needs a bound launch").
func TestAStaleHostRefusesEveryQueuedDispatchBeforeItIsQueued(t *testing.T) {
	dbPath := hostWithInfo(t, `{"pid":1,"instance":"host-old","childTokens":true}`)
	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true})
	cmd := coderhost.Command{ProfileID: "default", ConversationID: "c1", Kind: coderhost.KindSend, Message: "based on my CV find 200 opportunities"}
	_, err := enqueue(cfg, cmd, true)
	if err == nil || !strings.Contains(err.Error(), "coder host stop") {
		t.Fatalf("enqueue: %v", err)
	}
	_, _, err = enqueueBounded(cfg, cmd, coderhost.SendBounds{})
	if err == nil || !strings.Contains(err.Error(), "coder host stop") {
		t.Fatalf("enqueueBounded: %v", err)
	}
	if n := commandRows(t, cfg); n != 0 {
		t.Fatalf("%d command(s) were queued for a stale host", n)
	}
}

// staleHostFixture is a stale host (lock held, info naming this test process) whose stop is a recording stand-in that releases the lock.
func staleHostFixture(t *testing.T) (cfg *Deps, stopped *int) {
	t.Helper()
	dbPath := newChatCLITestDB(t)
	cfg = testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true})
	if err := os.MkdirAll(hostDir(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	release, err := daemonhb.LockFile(hostLockPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	raw, _ := json.Marshal(hostInfo{PID: os.Getpid(), Stamp: stampOf(os.Getpid()), Instance: "host-old"})
	if err := os.WriteFile(filepath.Join(hostDir(cfg), hostInfoFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	n := 0
	oldStop := stopHost
	stopHost = func(*Deps, hostInfo) (string, error) { n++; release(); return "", nil }
	oldVerify := verifyHostProcess
	verifyHostProcess = func(int) error { return nil }
	t.Cleanup(func() { stopHost, verifyHostProcess = oldStop, oldVerify })
	return cfg, &n
}

func execOn(t *testing.T, cfg *Deps, q string) {
	t.Helper()
	db, err := cfg.OpenDB()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.DB.Conn(context.Background()) // one connection: the pragma and the statement must share it
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, stmt := range []string{`PRAGMA foreign_keys = OFF`, q} {
		if _, err := conn.ExecContext(context.Background(), stmt); err != nil {
			t.Fatal(err)
		}
	}
}

// A stale host with nothing running, queued, asked or open is restarted by the next dispatch; with ANY of those it is left alone
// and the person gets the actionable text. Revert proof: dropping a check from hostBusy turns its row red.
func TestAStaleHostIsRestartedOnlyWhenNothingDependsOnIt(t *testing.T) {
	cfg, stopped := staleHostFixture(t)
	if err := ensureCurrentHost(cfg); err != nil || *stopped != 1 {
		t.Fatalf("an idle stale host: err=%v stops=%d", err, *stopped)
	}
	busy := []struct{ name, sql string }{
		{"an open run", `INSERT INTO profiles (id, name) VALUES ('p1','P'); INSERT INTO doomsday_runs (profile, generation, charter_id, state, mac) VALUES ('p1', 1, 'c1', 'running', x'01')`},
		{"a running turn", `INSERT INTO ai_chat_turns (id, conversation_id, profile_id, prompt, status, created_at, updated_at) VALUES ('t1','c1','default','p','active','x','x')`},
		{"a queued command", `INSERT INTO coder_session_commands (profile_id, conversation_id, kind, message, status, created_at, updated_at) VALUES ('default','c1','send','m','pending','x','x')`},
		{"a pending ask", `INSERT INTO coder_session_asks (profile_id, asker_session, asker_conv, ancestor_session, ancestor_conv, level, importance, question, bytes, status, created_at) VALUES ('default',1,'c1',2,'c2',1,'critical','q',1,'open','x')`},
	}
	for _, b := range busy {
		t.Run(b.name, func(t *testing.T) {
			cfg, stopped := staleHostFixture(t)
			for _, stmt := range strings.Split(b.sql, "; ") {
				execOn(t, cfg, stmt)
			}
			err := ensureCurrentHost(cfg)
			if err == nil || !strings.Contains(err.Error(), "coder host stop") || *stopped != 0 {
				t.Fatalf("err=%v stops=%d", err, *stopped)
			}
		})
	}
}
