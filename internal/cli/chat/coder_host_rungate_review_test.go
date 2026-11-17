//go:build !windows

package chat

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/daemonhb"
)

const busyTurn = `INSERT INTO ai_chat_turns (id, conversation_id, profile_id, prompt, status, created_at, updated_at) VALUES ('t1','c1','default','p','active','x','x')`

func procAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// writeStaleInfo points host.json at pid with its real start time and no run-gate marker.
func writeStaleInfo(t *testing.T, cfg *Deps, pid int) {
	t.Helper()
	raw, _ := json.Marshal(hostInfo{PID: pid, Stamp: stampOf(pid), Instance: "host-old"})
	if err := os.WriteFile(filepath.Join(hostDir(cfg), hostInfoFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// M1: stop, pause and resume are not dispatches of a binding-gated turn; an operator must be able to stop a turn on a stale host
// (a busy one, by definition), so only a send is guarded.
func TestControlCommandsAreQueuedToAStaleHostThatIsBusy(t *testing.T) {
	cfg, stopped := staleHostFixture(t)
	execOn(t, cfg, busyTurn)
	for _, k := range []coderhost.Kind{coderhost.KindStop, coderhost.KindPause, coderhost.KindResume} {
		if _, err := enqueue(cfg, coderhost.Command{ProfileID: "default", ConversationID: "c1", Kind: k}, true); err != nil {
			t.Fatalf("%s on a stale host: %v", k, err)
		}
	}
	if *stopped != 0 {
		t.Fatal("a control command restarted the host")
	}
	if n := commandRows(t, cfg); n != 3 {
		t.Fatalf("%d control commands queued, want 3", n)
	}
}

// m1: the read-only availability check creates nothing: no coder-host directory, no lock file.
func TestStaleHostReasonCreatesNothingWhenNoHostEverRan(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default"})
	if why := StaleHostReason(dbPath, StaleThenStart); why != "" {
		t.Fatalf("no host: %q", why)
	}
	if _, err := os.Stat(hostDir(cfg)); !os.IsNotExist(err) {
		t.Fatalf("the check created %s (%v)", hostDir(cfg), err)
	}
}

// m3: a host.json left by a killed host (its process is gone) beside a new host that holds the lock but has not rewritten the file is
// a host that is starting, not a stale one.
func TestAKilledHostsInfoBesideAStartingHostIsNotStale(t *testing.T) {
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Skip("no true")
	}
	dbPath := hostWithInfo(t, "")
	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default"})
	raw, _ := json.Marshal(hostInfo{PID: gone.Process.Pid, Stamp: "123456", Instance: "host-dead"})
	if err := os.WriteFile(filepath.Join(hostDir(cfg), hostInfoFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if why := StaleHostReason(dbPath, StaleThenStart); why != "" {
		t.Fatalf("a starting host is stale: %q", why)
	}
}

// M2: under the restart lock the staleness and the busyness are read again; a host that is already current (another CLI restarted it
// first) or one that became busy is not signalled.
func TestStopStaleHostRechecksBeforeItSignals(t *testing.T) {
	cfg, stopped := staleHostFixture(t)
	raw, _ := json.Marshal(hostInfo{PID: os.Getpid(), Stamp: stampOf(os.Getpid()), Instance: "host-new", RunGate: runGateVersion})
	if err := os.WriteFile(filepath.Join(hostDir(cfg), hostInfoFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := stopStaleHost(cfg, 0); err != nil || *stopped != 0 {
		t.Fatalf("a current host: err=%v stops=%d", err, *stopped)
	}
	writeStaleInfo(t, cfg, os.Getpid())
	execOn(t, cfg, busyTurn)
	if err := stopStaleHost(cfg, 0); err == nil || *stopped != 0 {
		t.Fatalf("a host that became busy: err=%v stops=%d", err, *stopped)
	}
}

// M3 + m6: host.json is a plain file; one that names an unrelated process (right pid and start time, no run-gate marker) must never
// get that process signalled. The real stop runs here, against a real sleeper.
func TestAForgedHostJsonNeverSignalsAnUnrelatedProcess(t *testing.T) {
	dbPath := hostWithInfo(t, "")
	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default"})
	victim := reapedChild(t)
	writeStaleInfo(t, cfg, victim.Process.Pid)
	err := ensureCurrentHost(cfg)
	if err == nil || !strings.Contains(err.Error(), "coder host stop") {
		t.Fatalf("a forged host.json: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if !procAlive(victim.Process.Pid) {
		t.Fatal("an unrelated process was signalled")
	}
}

// m6: the real SIGTERM path against a fake host: a process started as `monoagentcli --db-path DB coder host run` (this test binary under
// that name) that leaves on SIGTERM and whose exit frees the host lock.
func TestAStaleHostThatIsReallyAHostIsEndedBySigterm(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default"})
	if err := os.MkdirAll(hostDir(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	release, err := daemonhb.LockFile(hostLockPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	t.Cleanup(func() { once.Do(release) })
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "monoagentcli")
	if err := os.Symlink(exe, link); err != nil {
		t.Fatal(err)
	}
	fake := exec.Command(link, "--db-path", dbPath, "--profile", "default", "coder", "host", "run")
	fake.Env = append(os.Environ(), "CHAT_FAKE_HOST=1")
	if err := fake.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fake.Process.Kill() })
	go func() { fake.Wait(); once.Do(release) }()
	time.Sleep(500 * time.Millisecond) // the helper installs its handler
	writeStaleInfo(t, cfg, fake.Process.Pid)
	if err := ensureCurrentHost(cfg); err != nil {
		t.Fatalf("a real stale host: %v", err)
	}
	if hostRunning(cfg) {
		t.Fatal("the host lock is still held")
	}
}

// Answering a parked question is not blocked by that same question, in ITS table (coder_session_questions) and its person item
// only; an unrelated ask with the same id, another open question and a plan waiting for the person still block the restart.
func TestAnswerIsNotRefusedBecauseOfTheQuestionItAnswers(t *testing.T) {
	question := func(id int) string {
		return fmt.Sprintf(`INSERT INTO coder_session_questions (id, profile_id, conversation_id, class, status, created_at) VALUES (%d,'default','c1','risky_action','open','x')`, id)
	}
	item := func(id int) string {
		return fmt.Sprintf(`INSERT INTO coder_session_person_items (profile_id, conversation_id, kind, ref_id, created_at) VALUES ('default','c1','parked_question',%d,'x')`, id)
	}
	cfg, stopped := staleHostFixture(t)
	execOn(t, cfg, question(7))
	execOn(t, cfg, item(7))
	if err := ensureCurrentHostExcept(cfg, 7); err != nil || *stopped != 1 {
		t.Fatalf("only the answered question is open: err=%v stops=%d", err, *stopped)
	}
	for name, stmts := range map[string][]string{
		"another open question":          {question(7), question(8)},
		"an ask that has the same id":    {`INSERT INTO coder_session_asks (id, profile_id, asker_session, asker_conv, ancestor_session, ancestor_conv, level, importance, question, bytes, status, created_at) VALUES (7,'default',1,'c1',2,'c2',1,'critical','q',1,'open','x')`},
		"another question's person item": {question(8), item(8)},
		"a plan waiting for the person":  {strings.Replace(draftPlan, "(5,", "(7,", 1), strings.Replace(planItem, ",5,", ",7,", 1)},
	} {
		cfg, stopped := staleHostFixture(t)
		for _, st := range stmts {
			execOn(t, cfg, st)
		}
		if err := ensureCurrentHostExcept(cfg, 7); err == nil || *stopped != 0 {
			t.Fatalf("%s must still block: err=%v stops=%d", name, err, *stopped)
		}
	}
}

// The process check: a coder host is `<monoagentcli binary> [global flags] coder host run` for THIS database.
func TestOnlyThisDatabasesCoderHostPassesTheProcessCheck(t *testing.T) {
	db := "/home/u/scratch/x/monoagent.db"
	ok := [][]string{
		{"/usr/bin/monoagentcli", "--db-path", db, "--profile", "p", "coder", "host", "run"},
		{"monoagentcli", "--db-path=" + db, "coder", "host", "run"},
	}
	for _, a := range ok {
		if err := verifyHostArgs(a, db); err != nil {
			t.Fatalf("%v: %v", a, err)
		}
	}
	bad := map[string][]string{
		"not our binary":        {"/bin/sh", "--db-path", db, "coder", "host", "run"},
		"a shell with the text": {"/bin/sh", "-c", "sleep 9 # coder host run"},
		"another database":      {"monoagentcli", "--db-path", "/other.db", "coder", "host", "run"},
		"the default database":  {"monoagentcli", "coder", "host", "run"},
		"extra words":           {"monoagentcli", "--db-path", db, "echo", "coder", "host", "run"},
		"a trailing argument":   {"monoagentcli", "--db-path", db, "coder", "host", "run", "x"},
		"an unknown flag":       {"monoagentcli", "--db-path", db, "--evil", "coder", "host", "run"},
	}
	for name, a := range bad {
		if err := verifyHostArgs(a, db); err == nil {
			t.Fatalf("%s passed: %v", name, a)
		}
	}
}

// A retry of a send that is already queued queues nothing: it is answered as the duplicate it is, even while the host is stale and
// busy (the command it repeats is why it is busy).
func TestAnIdempotentRetryOfAQueuedSendIsAnsweredBeforeTheStaleHostRefuses(t *testing.T) {
	cfg, _ := staleHostFixture(t)
	cmd := coderhost.Command{ProfileID: "default", ConversationID: "c1", Kind: coderhost.KindSend, TurnID: "t-1", Message: "go"}
	db, err := cfg.OpenDB()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coderhost.NewStore(db.DB).Enqueue(cmd); err != nil {
		t.Fatal(err)
	}
	db.Close()
	retry := cmd
	retry.TurnID = "t-2"
	out, deduped, err := enqueueBounded(cfg, retry, coderhost.SendBounds{MaxPending: 5, Within: time.Minute})
	if err != nil || !deduped || out.ID == 0 {
		t.Fatalf("the retry: out=%+v deduped=%v err=%v", out, deduped, err)
	}
	other := retry
	other.Message = "something else"
	if _, _, err := enqueueBounded(cfg, other, coderhost.SendBounds{MaxPending: 5, Within: time.Minute}); err == nil || !strings.Contains(err.Error(), "coder host stop") {
		t.Fatalf("a different message must still be refused: %v", err)
	}
}

// The refusal names the exact command, with --db-path when the database is not the default one, and what to do next in the place it
// is shown (a send, a start, the app).
func TestTheStaleHostTextNamesTheDatabaseAndTheNextStepOfItsPlace(t *testing.T) {
	dbPath := hostWithInfo(t, `{"pid":1,"instance":"host-old"}`)
	for _, then := range []string{StaleThenSend, StaleThenStart, StaleThenApp} {
		why := StaleHostReason(dbPath, then)
		if !strings.Contains(why, "--db-path") || !strings.Contains(why, dbPath) || !strings.Contains(why, "then "+then) {
			t.Fatalf("%q: %q", then, why)
		}
	}
	if got := hostStopCommand("~/.monoagent/monoagent.db"); got != "monoagentcli coder host stop" {
		t.Fatalf("the default database: %q", got)
	}
}

// The branches where the stop does not happen say what happened and what to do.
func TestAStaleHostThatCannotBeStoppedSaysWhatToDo(t *testing.T) {
	// a host too old to record its process: nothing to signal
	cfg, stopped := staleHostFixture(t)
	if err := os.WriteFile(filepath.Join(hostDir(cfg), hostInfoFile), []byte(`{"pid":1,"instance":"host-old"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	err := ensureCurrentHost(cfg)
	if err == nil || !strings.Contains(err.Error(), "names no process I can identify") || !strings.Contains(err.Error(), "coder host stop") || *stopped != 0 {
		t.Fatalf("no stamp: %v stops=%d", err, *stopped)
	}
	// the stop itself fails
	cfg, _ = staleHostFixture(t)
	stopStaleProcess = func(*Deps, hostInfo) (string, error) { return "", errors.New("operation not permitted") }
	if err := ensureCurrentHost(cfg); err == nil || !strings.Contains(err.Error(), "operation not permitted") || !strings.Contains(err.Error(), "coder host stop") {
		t.Fatalf("a failed stop: %v", err)
	}
	// the stop is accepted but the host stays
	cfg, _ = staleHostFixture(t)
	old := hostStopSettle
	hostStopSettle = 100 * time.Millisecond
	t.Cleanup(func() { hostStopSettle = old })
	stopStaleProcess = func(*Deps, hostInfo) (string, error) { return "", nil } // does not release the lock
	if err := ensureCurrentHost(cfg); err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("a host that stays: %v", err)
	}
}

// A host.json that cannot be read is a starting host's (it writes the file after taking its lock): not stale, nothing refused.
func TestACorruptHostJsonIsNotAStaleHost(t *testing.T) {
	dbPath := hostWithInfo(t, `{"pid": not json`)
	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default"})
	if why := StaleHostReason(dbPath, StaleThenSend); why != "" {
		t.Fatalf("corrupt: %q", why)
	}
	if err := ensureCurrentHost(cfg); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
}

// Only the lock TIMEOUT is "another restart in progress"; any other failure of the lock says what it is.
func TestOnlyALockTimeoutIsReportedAsAnotherRestart(t *testing.T) {
	cfg, _ := staleHostFixture(t)
	old := restartLockWait
	restartLockWait = 100 * time.Millisecond
	t.Cleanup(func() { restartLockWait = old })
	hold, err := daemonhb.LockFile(filepath.Join(hostDir(cfg), "host-restart.lock"))
	if err != nil {
		t.Fatal(err)
	}
	err = stopStaleHost(cfg, 0)
	hold()
	if err == nil || !strings.Contains(err.Error(), "another restart of the host is in progress") {
		t.Fatalf("a held lock: %v", err)
	}
	_ = os.Remove(filepath.Join(hostDir(cfg), "host-restart.lock"))
	if err := os.Mkdir(filepath.Join(hostDir(cfg), "host-restart.lock"), 0o700); err != nil { // a directory cannot be locked
		t.Fatal(err)
	}
	err = stopStaleHost(cfg, 0)
	if err == nil || strings.Contains(err.Error(), "another restart") {
		t.Fatalf("a lock that cannot be taken at all: %v", err)
	}
}
