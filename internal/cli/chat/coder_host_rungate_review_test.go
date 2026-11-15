//go:build !windows

package chat

import (
	"encoding/json"
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
	if why := StaleHostReason(dbPath); why != "" {
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
	if why := StaleHostReason(dbPath); why != "" {
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

// m6: the real SIGTERM path against a fake host: a process whose command line is `... coder host run`, that leaves on SIGTERM
// and whose exit frees the host lock.
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
	fake := exec.Command("/bin/sh", "-c", `trap 'exit 0' TERM; while :; do sleep 0.1; done`, "sh", "coder", "host", "run")
	if err := fake.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fake.Process.Kill() })
	go func() { fake.Wait(); once.Do(release) }()
	time.Sleep(300 * time.Millisecond) // the shell installs its trap
	writeStaleInfo(t, cfg, fake.Process.Pid)
	if err := ensureCurrentHost(cfg); err != nil {
		t.Fatalf("a real stale host: %v", err)
	}
	if hostRunning(cfg) {
		t.Fatal("the host lock is still held")
	}
}

// m4b: answering a question is not blocked by that same question: the only open ask is the one being answered.
func TestAnswerIsNotRefusedBecauseOfTheQuestionItAnswers(t *testing.T) {
	cfg, stopped := staleHostFixture(t)
	execOn(t, cfg, `INSERT INTO coder_session_asks (id, profile_id, asker_session, asker_conv, ancestor_session, ancestor_conv, level, importance, question, bytes, status, created_at) VALUES (7,'default',1,'c1',2,'c2',1,'critical','q',1,'open','x')`)
	if err := ensureCurrentHostExcept(cfg, 7); err != nil || *stopped != 1 {
		t.Fatalf("err=%v stops=%d", err, *stopped)
	}
	cfg, stopped = staleHostFixture(t)
	execOn(t, cfg, `INSERT INTO coder_session_asks (id, profile_id, asker_session, asker_conv, ancestor_session, ancestor_conv, level, importance, question, bytes, status, created_at) VALUES (8,'default',1,'c1',2,'c2',1,'critical','q',1,'open','x')`)
	if err := ensureCurrentHostExcept(cfg, 7); err == nil || *stopped != 0 {
		t.Fatalf("another open question must still count: err=%v stops=%d", err, *stopped)
	}
}
