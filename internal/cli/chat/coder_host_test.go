package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/daemonhb"
	"github.com/monoes/mono-agent/internal/storage"
)

func runSessionsCLI(t *testing.T, cfg *Deps, args ...string) (string, int) {
	t.Helper()
	cmd := newCoderSessionsCmd(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(args)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	err := cmd.Execute()
	return out.String(), exitCodeFor(err)
}

func pendingCommands(t *testing.T, dbPath string) []coderhost.Command {
	t.Helper()
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cmds, err := coderhost.NewStore(db.DB).Pending()
	if err != nil {
		t.Fatal(err)
	}
	return cmds
}

func TestSessionsSendQueuesForTheHostAndStartsOne(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	store := openTestChatStore(t, dbPath)
	conv, _ := store.CreateConversationMode("default", "agent", "general", "claude", "", "", ai.ModeCoder, t.TempDir())
	scoped, _ := store.CreateConversation("default", "agent", "general", "claude", "", "")
	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true})

	started := 0
	orig := startHostProcess
	t.Cleanup(func() { startHostProcess = orig })
	startHostProcess = func(c *Deps) error { // stands in for the detached host: takes the lock
		started++
		release, err := daemonhb.LockFile(hostLockPath(c))
		t.Cleanup(func() { release() })
		return err
	}

	if out, code := runSessionsCLI(t, cfg, "send", conv.ID, "do the thing"); code != 0 || !strings.Contains(out, `"turnId"`) {
		t.Fatalf("send: exit %d %s", code, out)
	}
	if started != 1 || !hostRunning(cfg) {
		t.Fatalf("host started %d times, running=%v", started, hostRunning(cfg))
	}
	if _, code := runSessionsCLI(t, cfg, "pause", conv.ID); code != 0 || started != 1 {
		t.Fatalf("a running host is not started again: exit %d, starts %d", code, started)
	}
	cmds := pendingCommands(t, dbPath) // in the order queued
	if len(cmds) != 2 || cmds[0].Kind != coderhost.KindSend || cmds[0].Message != "do the thing" || cmds[1].Kind != coderhost.KindPause {
		t.Fatalf("queued = %+v", cmds)
	}
	if _, code := runSessionsCLI(t, cfg, "send", scoped.ID, "x"); code == 0 {
		t.Fatal("a scoped chat is not a coder session")
	}
	if _, code := runSessionsCLI(t, cfg, "send", "no-such-chat", "x"); code != 4 && code == 0 {
		t.Fatalf("unknown conversation: exit %d", code)
	}
	if len(pendingCommands(t, dbPath)) != 2 {
		t.Fatal("a refused send was queued")
	}
}

func TestSessionsCommandsAreTheOperatorsOnly(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	conv, _ := openTestChatStore(t, dbPath).CreateConversationMode("default", "agent", "general", "claude", "", "", ai.ModeCoder, t.TempDir())
	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true})
	cfg.RequireOperator = func(what string) error { return errors.New("operator only: " + what) }
	orig := startHostProcess
	t.Cleanup(func() { startHostProcess = orig })
	startHostProcess = func(*Deps) error { t.Error("an agent started the host"); return nil }
	for _, args := range [][]string{{"send", conv.ID, "x"}, {"stop", conv.ID}, {"pause", conv.ID}, {"resume", conv.ID}} {
		if _, code := runSessionsCLI(t, cfg, args...); code == 0 {
			t.Errorf("%v was allowed for a non-operator", args)
		}
	}
	if len(pendingCommands(t, dbPath)) != 0 {
		t.Fatal("a refused command was queued")
	}
	cmd := newCoderHostCmd(cfg)
	cmd.SetArgs([]string{"run"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	if err := cmd.Execute(); err == nil || hostRunning(cfg) {
		t.Fatalf("host run for a non-operator: err=%v running=%v", err, hostRunning(cfg))
	}
}

// A host-started turn is detached, takes its prompt on stdin, is found again by
// pid and stamp after a host restart, and is stopped through its process group.
func TestExecLauncherStartsADetachedTurnAndStopsItsGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process groups")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "args.log")
	promptLog := filepath.Join(dir, "prompt.txt")
	bin := filepath.Join(dir, "fake-monoagentcli")
	script := "#!/bin/sh\necho \"$*\" >> '" + argsLog + "'\nif [ \"$4\" = \"chat\" ] && [ \"$5\" = \"history\" ]; then exit 0; fi\n" +
		"cat > '" + promptLog + "'\necho '{\"level\":\"warn\",\"error\":\"a library log line, not a refusal\"}'\necho '{\"admitted\":true,\"existed\":false,\"turn\":{\"id\":\"t1\"}}'\n" +
		"(trap '' TERM; exec sleep 60) &\necho $! > '" + filepath.Join(dir, "child.pid") + "'\nwait\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	l := &execLauncher{bin: bin, dbPath: "/db/x.db", instance: "host-test", logDir: filepath.Join(dir, "turns")}

	if _, err := l.Start("p", "-rf", "t1", "hi", nil); err == nil {
		t.Fatal("a conversation id that reads as a flag was accepted")
	}
	secret := strings.Repeat("a long prompt ", 20000) // 280 KB: over the 128 KiB argument limit
	turn, err := l.Start("p", "c1", "t1", secret, []string{"/tmp/a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if turn.PID() <= 0 || turn.Stamp() == "" {
		t.Fatalf("pid %d stamp %q", turn.PID(), turn.Stamp())
	}
	if l.Adopt(turn.PID(), turn.Stamp()) == nil {
		t.Fatal("a live turn was not found again")
	}
	if l.Adopt(turn.PID(), "start:0") != nil || l.Adopt(turn.PID(), "") != nil || l.Adopt(1, "start:1") != nil {
		t.Fatal("a pid with a different or no start time, or pid 1, was adopted")
	}
	b, _ := os.ReadFile(argsLog)
	if !strings.Contains(string(b), "--db-path /db/x.db --profile p --json chat --conversation c1 --turn t1 --instance host-test --prompt-stdin --attach=/tmp/a.txt") || strings.Contains(string(b), "a long prompt") {
		t.Fatalf("arguments = %s (the prompt must not be one)", b)
	}
	waitUntil(t, "the prompt to arrive on stdin", func() bool {
		got, _ := os.ReadFile(promptLog)
		return string(got) == secret
	})
	done := make(chan struct{})
	go func() { _ = turn.Wait(); close(done) }()
	turn.Stop()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("a stopped turn did not end")
	}
	if l.Adopt(turn.PID(), turn.Stamp()) != nil {
		t.Fatal("an ended turn was adopted")
	}
	childPID := 0
	if b, err := os.ReadFile(filepath.Join(dir, "child.pid")); err == nil {
		childPID, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	if childPID <= 1 {
		t.Fatalf("no child pid recorded")
	}
	waitUntil(t, "the child that ignores SIGTERM to be killed with its leader", func() bool { return !daemonhb.ProcessAlive(childPID) })
	if _, err := os.Stat(filepath.Join(dir, "turns", "t1.prompt")); !os.IsNotExist(err) {
		t.Fatalf("the prompt file was left behind: %v", err)
	}
	l.Finish("p", "c1", "t1", "gone")
	if b, _ := os.ReadFile(argsLog); !strings.Contains(string(b), "chat history finish c1 t1 --status failed --reason gone") {
		t.Fatalf("finish not run: %s", b)
	}
}

// A turn the process refuses before admitting it (the app owns the chat, no such
// conversation, a turn id used before) fails the command with the reason.
func TestExecLauncherReportsARefusedTurn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-monoagentcli")
	script := "#!/bin/sh\ncat > /dev/null\necho '{\"error\":\"chat: conversation has an active turn owned by another instance\",\"code\":\"invalid_input\"}'\nexit 3\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	l := &execLauncher{bin: bin, dbPath: "/db/x.db", instance: "host-test", logDir: filepath.Join(dir, "turns")}
	_, err := l.Start("p", "c1", "t1", "hello", nil)
	if err == nil || !strings.Contains(err.Error(), "owned by another instance") {
		t.Fatalf("Start = %v, want the refusal", err)
	}
}

func waitUntil(t *testing.T, what string, f func() bool) {
	t.Helper()
	for i := 0; i < 400; i++ {
		if f() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The app's startup sweep must not interrupt a turn whose process the host still
// has, but must interrupt one whose process is gone (a host that crashed).
func TestReconcileSparesOnlyTurnsWhoseProcessIsAlive(t *testing.T) {
	if runtime.GOOS == "windows" || pidStamp(os.Getpid()) == "" {
		t.Skip("no process start times here")
	}
	dbPath := newChatCLITestDB(t)
	store := openTestChatStore(t, dbPath)
	conv := map[string]string{}
	for _, turn := range []string{"alive", "dead", "nostamp", "orphan"} {
		c, _ := store.CreateConversation("default", "agent", "general", "claude", "", "")
		store.CreateTurn(c.ID, "default", turn, "host-abc", "hi")
		conv[turn] = c.ID
	}
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Skip("no `true` to start a short-lived process")
	}
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	cs := coderhost.NewStore(db.DB)
	set := func(turn string, pid int, stamp string) {
		x, _ := cs.EnsureSession("default", conv[turn])
		x.State, x.TurnID, x.TurnPID, x.TurnStamp = coderhost.StateRunning, turn, pid, stamp
		if err := cs.SaveSession(x); err != nil {
			t.Fatal(err)
		}
	}
	set("alive", os.Getpid(), pidStamp(os.Getpid()))
	set("dead", gone.Process.Pid, "start:1") // a host that crashed: its turn's process is gone
	set("nostamp", os.Getpid(), "")          // cannot tell this process from a reused pid
	db.Close()

	if out, code := runChatHistory(t, dbPath, "default", "reconcile", "--except-owner", "me"); code != 0 {
		t.Fatalf("reconcile exit %d: %s", code, out)
	}
	for turn, want := range map[string]string{"alive": "active", "dead": "interrupted", "nostamp": "interrupted", "orphan": "interrupted"} {
		if got, _ := store.GetTurn(turn, "default"); got.Status != want {
			t.Errorf("turn %s = %q, want %q", turn, got.Status, want)
		}
	}
}

// A turn another host started has no waiter to remove its prompt: Finish does.
func TestFinishRemovesTheTurnsPromptFile(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	l := &execLauncher{bin: bin, dbPath: "/db/x.db", instance: "host-test", logDir: filepath.Join(dir, "turns")}
	os.MkdirAll(l.logDir, 0o700)
	prompt := filepath.Join(l.logDir, "t1.prompt")
	os.WriteFile(prompt, []byte("secret"), 0o600)
	l.Finish("p", "c1", "t1", "gone")
	if _, err := os.Stat(prompt); !os.IsNotExist(err) {
		t.Fatalf("prompt file left behind: %v", err)
	}
}

// While the host is still admitting a turn (recorded, no pid yet) the sweep
// spares it, for a while; a record that stays that way is an orphan. When the
// host's records cannot be read, no host turn is interrupted.
func TestHostHeldTurnsCoversTheAdmissionWindowAndFailsClosed(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	store := openTestChatStore(t, dbPath)
	fresh, _ := store.CreateConversation("default", "agent", "general", "claude", "", "")
	stale, _ := store.CreateConversation("default", "agent", "general", "claude", "", "")
	store.CreateTurn(fresh.ID, "default", "fresh", "host-abc", "hi")
	store.CreateTurn(stale.ID, "default", "stale", "host-abc", "hi")
	db, _ := storage.NewDatabase(dbPath)
	cs := coderhost.NewStore(db.DB)
	for _, c := range []struct{ conv, turn string }{{fresh.ID, "fresh"}, {stale.ID, "stale"}} {
		x, _ := cs.EnsureSession("default", c.conv)
		x.State, x.TurnID = coderhost.StateRunning, c.turn
		cs.SaveSession(x)
	}
	db.DB.Exec(`UPDATE coder_sessions SET updated_at = ? WHERE conversation_id = ?`, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano), stale.ID)
	db.Close()

	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default"})
	skip, closeSkip := hostHeldTurns(cfg)
	defer closeSkip()
	if !skip(ai.Turn{ID: "fresh", ConversationID: fresh.ID, ProfileID: "default"}) {
		t.Error("a turn the host is admitting right now was not spared")
	}
	if skip(ai.Turn{ID: "stale", ConversationID: stale.ID, ProfileID: "default"}) {
		t.Error("a turn stuck in admission for an hour was spared")
	}

	broken := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default"})
	broken.OpenDB = func() (*storage.Database, error) { return nil, errors.New("disk on fire") }
	skip, closeSkip2 := hostHeldTurns(broken)
	defer closeSkip2()
	if !skip(ai.Turn{ID: "x", OwnerInstanceID: "host-abc"}) || skip(ai.Turn{ID: "y", OwnerInstanceID: "11111111-2222-3333-4444-555555555555"}) {
		t.Error("with unreadable records the sweep must spare host-owned turns and only those")
	}
}

// reapedChild starts a long-lived child and reaps it when it ends, so only a
// zombie the test makes on purpose is one.
func reapedChild(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skip("no sleep")
	}
	go cmd.Wait()
	t.Cleanup(func() { cmd.Process.Kill() })
	return cmd
}

// A start time that cannot be read for a moment (ps failed, /proc denied) is not
// an exit: reading it as one would start the next turn beside a running agent.
func TestAnAdoptedTurnWhoseStartTimeCannotBeReadIsStillRunning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process start times differ")
	}
	child := reapedChild(t)
	l := &execLauncher{}
	stamp := pidStamp(child.Process.Pid)
	if stamp == "" {
		t.Skip("no process start times here")
	}
	turn := l.Adopt(child.Process.Pid, stamp)
	if turn == nil {
		t.Fatal("a live turn was not adopted")
	}
	oldStamp, oldPoll := stampOf, adoptedPoll
	stampOf, adoptedPoll = func(int) string { return "" }, 20*time.Millisecond
	t.Cleanup(func() { stampOf, adoptedPoll = oldStamp, oldPoll })
	if l.Adopt(child.Process.Pid, stamp) == nil {
		t.Fatal("a live turn was dropped because its start time could not be read")
	}
	ended := make(chan struct{})
	go func() { _ = turn.Wait(); close(ended) }()
	select {
	case <-ended:
		t.Fatal("Wait returned for a live process whose start time could not be read")
	case <-time.After(300 * time.Millisecond):
	}
	child.Process.Kill()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after the process died")
	}
}

// A turn the host cannot identify could never be found again, spared by the
// startup sweep or stopped: it is not left running.
func TestAStartedTurnWithoutAReadableStartTimeIsStoppedAndFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	bin := filepath.Join(dir, "fake")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho $$ > '"+pidFile+"'\ncat > /dev/null\necho '{\"admitted\":true}'\nsleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldStamp := stampOf
	stampOf = func(int) string { return "" }
	t.Cleanup(func() { stampOf = oldStamp })
	l := &execLauncher{bin: bin, dbPath: "/db/x.db", instance: "host-test", logDir: filepath.Join(dir, "turns")}
	_, err := l.Start("p", "c1", "t1", "hello", nil)
	if err == nil || !strings.Contains(err.Error(), "identify") {
		t.Fatalf("Start = %v, want a failure that says the process could not be identified", err)
	}
	waitUntil(t, "the unidentifiable turn's process to be gone", func() bool {
		b, _ := os.ReadFile(pidFile)
		pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		return pid > 1 && !turnAlive(pid)
	})
}

// An exited leader nobody reaped is a zombie: kill(pid, 0) still accepts it, but
// it can never run again, so the turn is over.
func TestAZombieIsNotAliveForTheHost(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no zombies")
	}
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Skip("no true")
	}
	pid := cmd.Process.Pid
	waitUntil(t, "the child to become a zombie", func() bool { return processState(pid) == "Z" })
	defer cmd.Wait()
	if !daemonhb.ProcessAlive(pid) {
		t.Skip("this platform's ProcessAlive already says a zombie is dead")
	}
	if turnAlive(pid) {
		t.Fatal("a zombie counts as a running turn")
	}
	if (&execLauncher{}).Adopt(pid, "start:1@x") != nil {
		t.Fatal("a zombie was adopted")
	}
	zombie := &adoptedTurn{pid: pid, stamp: "start:1@x"}
	done := make(chan struct{})
	go func() { _ = zombie.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Wait never returned for a zombie leader")
	}
}

// The prompt file is gone as soon as the turn runs (it holds the open file), and
// the turn still reads the whole prompt.
func TestThePromptFileIsRemovedOnceTheTurnHoldsIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows removes it after the turn")
	}
	dir := t.TempDir()
	promptLog := filepath.Join(dir, "prompt.txt")
	bin := filepath.Join(dir, "fake")
	script := "#!/bin/sh\necho '{\"admitted\":true}'\nsleep 1\ncat > '" + promptLog + "'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	l := &execLauncher{bin: bin, dbPath: "/db/x.db", instance: "host-test", logDir: filepath.Join(dir, "turns")}
	turn, err := l.Start("p", "c1", "t1", "the whole prompt", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "turns", "t1.prompt")); !os.IsNotExist(err) {
		t.Fatalf("the prompt file is still on disk while the turn runs: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "turns")); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("log dir = %v, %v", fi, err)
	}
	_ = turn.Wait()
	if b, _ := os.ReadFile(promptLog); string(b) != "the whole prompt" {
		t.Fatalf("the turn read %q", b)
	}
}

func TestSweepPromptsRemovesOnlyOldFiles(t *testing.T) {
	dir := t.TempDir()
	oldF, newF, other := filepath.Join(dir, "old.prompt"), filepath.Join(dir, "new.prompt"), filepath.Join(dir, "keep.log")
	for _, f := range []string{oldF, newF, other} {
		os.WriteFile(f, []byte("x"), 0o600)
	}
	past := time.Now().Add(-3 * time.Hour)
	os.Chtimes(oldF, past, past)
	os.Chtimes(other, past, past)
	sweepPrompts(dir, time.Hour)
	for f, want := range map[string]bool{oldF: false, newF: true, other: true} {
		if _, err := os.Stat(f); (err == nil) != want {
			t.Errorf("%s exists=%v, want %v", filepath.Base(f), err == nil, want)
		}
	}
}

// host stop signals only a process it can tell is the host: a host.json with no
// start time (an older host, or one that could not read it) is refused.
func TestHostStopRefusesWhatItCannotIdentify(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true})
	child := reapedChild(t)
	if err := os.MkdirAll(hostDir(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	release, err := daemonhb.LockFile(hostLockPath(cfg)) // a "host" is running
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	raw, _ := json.Marshal(hostInfo{PID: child.Process.Pid, Stamp: "", Instance: "host-x"})
	os.WriteFile(filepath.Join(hostDir(cfg), hostInfoFile), raw, 0o600)
	cmd := newCoderHostCmd(cfg)
	cmd.SetArgs([]string{"stop"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err == nil {
		t.Fatal("host stop signalled a process it could not identify")
	}
	time.Sleep(100 * time.Millisecond)
	if !turnAlive(child.Process.Pid) {
		t.Fatal("the unidentified process was killed")
	}
}

// A turn the host gives up on is gone before Start returns, so the session it
// frees cannot overlap an agent that was slow to die.
func TestAGivenUpTurnIsGoneBeforeStartReturns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	bin := filepath.Join(dir, "fake")
	// ignores SIGTERM, so only the kill after the grace period ends it
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ntrap '' TERM\necho $$ > '"+pidFile+"'\ncat > /dev/null\nwhile true; do sleep 1; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldStamp, oldGrace := stampOf, hostTurnGrace
	stampOf, hostTurnGrace = func(int) string { return "" }, 300*time.Millisecond
	t.Cleanup(func() { stampOf, hostTurnGrace = oldStamp, oldGrace })
	l := &execLauncher{bin: bin, dbPath: "/db/x.db", instance: "host-test", logDir: filepath.Join(dir, "turns")}
	if _, err := l.Start("p", "c1", "t1", "hello", nil); err == nil {
		t.Fatal("Start succeeded without a start time")
	}
	b, _ := os.ReadFile(pidFile)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if pid <= 1 || turnAlive(pid) {
		t.Fatalf("pid %d is still alive when Start returned", pid)
	}
}

// A stop for an adopted turn is not lost because its start time could not be read
// at that moment.
func TestAStopForAnAdoptedTurnSurvivesAnUnreadableStartTime(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process groups")
	}
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = newSessionAttr()
	if err := cmd.Start(); err != nil {
		t.Skip("no sleep")
	}
	go cmd.Wait()
	t.Cleanup(func() { cmd.Process.Kill() })
	pid := cmd.Process.Pid
	stamp := pidStamp(pid)
	if stamp == "" {
		t.Skip("no process start times here")
	}
	turn := (&execLauncher{}).Adopt(pid, stamp)
	if turn == nil {
		t.Fatal("not adopted")
	}
	unreadable := 3
	oldStamp, oldInt, oldGrace := stampOf, signalRetryInterval, hostTurnGrace
	stampOf = func(p int) string {
		if unreadable > 0 {
			unreadable--
			return ""
		}
		return oldStamp(p)
	}
	signalRetryInterval, hostTurnGrace = 10*time.Millisecond, 5*time.Second
	t.Cleanup(func() { stampOf, signalRetryInterval, hostTurnGrace = oldStamp, oldInt, oldGrace })
	turn.Stop()
	waitUntil(t, "the adopted turn to be stopped despite the unreadable start time", func() bool { return !turnAlive(pid) })
}

// The last line of the log may have no newline when the process exits right after it.
func TestAnAdmissionLineWithoutANewlineCounts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ncat > /dev/null\nprintf '{\"admitted\":true}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	l := &execLauncher{bin: bin, dbPath: "/db/x.db", instance: "host-test", logDir: filepath.Join(dir, "turns")}
	if _, err := l.Start("p", "c1", "t1", "hello", nil); err != nil {
		t.Fatalf("Start = %v, want the unterminated admission line accepted", err)
	}
}

func TestHostRunRefusesWithoutAStartTimeOfItsOwn(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true})
	oldStamp := stampOf
	stampOf = func(int) string { return "" }
	t.Cleanup(func() { stampOf = oldStamp })
	cmd := newCoderHostCmd(cfg)
	cmd.SetArgs([]string{"run"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second) // a host that does start would run until this ends
	defer cancel()
	if err := cmd.ExecuteContext(ctx); err == nil || !strings.Contains(err.Error(), "start time") {
		t.Fatalf("host run = %v, want a refusal that names the start time", err)
	}
	if hostRunning(cfg) {
		t.Fatal("the refused host kept the lock")
	}
}

func TestSessionSettingsGetSetAndTheirGuards(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	store := openTestChatStore(t, dbPath)
	conv, _ := store.CreateConversationMode("default", "agent", "general", "claude", "", "", ai.ModeCoder, t.TempDir())
	scoped, _ := store.CreateConversation("default", "agent", "general", "claude", "", "")
	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true})
	run := func(c *Deps, args ...string) (sessionSettingsView, int) {
		out, code := runSessionsCLI(t, c, args...)
		var v sessionSettingsView
		decodeChatJSON(t, out, &v)
		return v, code
	}

	v, code := run(cfg, "settings", "get", conv.ID)
	if code != 0 || v.Settings.CheckinMinutes != 15 || !v.Unattended || v.Role != "solo" || v.Access == "" || len(v.OperatorOnly) == 0 {
		t.Fatalf("get: exit %d %+v", code, v)
	}
	db, _ := storage.NewDatabase(dbPath)
	if _, ok, _ := coderhost.NewStore(db.DB).GetSession("default", conv.ID); ok {
		t.Fatal("reading settings created a session")
	}
	db.Close()

	v, code = run(cfg, "settings", "set", conv.ID, "--patch", `{"checkin_minutes": 5, "autonomy_preset": "ask-always"}`, "--unattended", "false")
	if code != 0 || v.Settings.CheckinMinutes != 5 || v.Settings.AutonomyPreset != "ask-always" || v.Unattended {
		t.Fatalf("set: exit %d %+v", code, v)
	}
	if strings.Join(v.Changed, ",") != "autonomy_preset,checkin_minutes,unattended" {
		t.Fatalf("changed = %v", v.Changed)
	}
	if v, _ = run(cfg, "settings", "get", conv.ID); v.Settings.CheckinMinutes != 5 || v.Unattended {
		t.Fatalf("not stored: %+v", v)
	}
	for _, bad := range [][]string{
		{"settings", "set", conv.ID, "--patch", `{"checkin_minutes": 0.5}`},
		{"settings", "set", conv.ID, "--patch", `{"nope": 1}`},
		{"settings", "set", conv.ID, "--unattended", "maybe"},
		{"settings", "set", conv.ID},
		{"settings", "set", scoped.ID, "--patch", `{"checkin_minutes": 5}`},
		{"settings", "get", scoped.ID},
	} {
		if _, code := run(cfg, bad...); code == 0 {
			t.Errorf("%v was accepted", bad)
		}
	}
	if v, _ = run(cfg, "settings", "get", conv.ID); v.Settings.CheckinMinutes != 5 {
		t.Fatalf("a refused change altered the settings: %+v", v)
	}
	agent := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true})
	agent.RequireOperator = func(what string) error { return errors.New("operator only: " + what) }
	if _, code := run(agent, "settings", "set", conv.ID, "--patch", `{"checkin_minutes": 1}`); code == 0 {
		t.Fatal("a non-operator changed a session's settings")
	}
	if v, _ = run(agent, "settings", "get", conv.ID); v.Settings.CheckinMinutes != 5 {
		t.Fatalf("a non-operator may read, and the value must be unchanged: %+v", v)
	}
}

func TestSessionRoleSetsTheMasterChildStructureWithTheStoreRules(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	store := openTestChatStore(t, dbPath)
	mk := func() string {
		c, _ := store.CreateConversationMode("default", "agent", "general", "claude", "", "", ai.ModeCoder, t.TempDir())
		return c.ID
	}
	m, k := mk(), mk()
	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true})
	run := func(args ...string) (sessionSettingsView, int) {
		out, code := runSessionsCLI(t, cfg, append([]string{"settings", "set"}, args...)...)
		var v sessionSettingsView
		decodeChatJSON(t, out, &v)
		return v, code
	}
	if _, code := run(k, "--role", "child", "--parent", m); code == 0 {
		t.Fatal("a child of a session that is not a master was accepted")
	}
	if v, code := run(m, "--role", "master"); code != 0 || v.Role != "master" {
		t.Fatalf("master: exit %d %+v", code, v)
	}
	if v, code := run(k, "--role", "child", "--parent", m); code != 0 || v.Role != "child" {
		t.Fatalf("child: exit %d %+v", code, v)
	}
	for _, bad := range [][]string{{k, "--parent", m}, {k, "--role", "boss"}, {m, "--role", "solo"}} {
		if _, code := run(bad...); code == 0 {
			t.Errorf("%v was accepted", bad)
		}
	}
	agent := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true})
	agent.RequireOperator = func(what string) error { return errors.New("operator only: " + what) }
	if _, code := runSessionsCLI(t, agent, "settings", "set", k, "--role", "solo"); code == 0 {
		t.Fatal("a non-operator changed a role")
	}
}

func TestSettingsViewSaysWhenAMasterWaitsAtItsCheckinLimit(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	store := openTestChatStore(t, dbPath)
	conv, _ := store.CreateConversationMode("default", "agent", "general", "claude", "", "", ai.ModeCoder, t.TempDir())
	solo, _ := store.CreateConversationMode("default", "agent", "general", "claude", "", "", ai.ModeCoder, t.TempDir())
	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true})
	get := func(id string) sessionSettingsView {
		out, code := runSessionsCLI(t, cfg, "settings", "get", id)
		var v sessionSettingsView
		decodeChatJSON(t, out, &v)
		if code != 0 {
			t.Fatalf("get %s: exit %d %s", id, code, out)
		}
		return v
	}
	turn := func(id string) {
		t.Helper()
		if _, _, err := store.CreateTurn(conv.ID, "default", id, "host-x", "digest"); err != nil {
			t.Fatal(err)
		}
		store.FinalizeTurn("default", conv.ID, id, chatevents.StatusCompleted, "", nil, true)
	}
	if v := get(conv.ID); v.Checkin != nil {
		t.Fatalf("a session that is not a master has no check-in state: %+v", v.Checkin)
	}
	if _, code := runSessionsCLI(t, cfg, "settings", "set", conv.ID, "--role", "master", "--patch", `{"checkin_limit": 2}`); code != 0 {
		t.Fatal("could not make a master")
	}
	turn("checkin-1")
	if v := get(conv.ID); v.Checkin == nil || v.Checkin.Streak != 1 || v.Checkin.Limit != 2 || v.Checkin.Waiting {
		t.Fatalf("after one: %+v", v.Checkin)
	}
	turn("checkin-2")
	if v := get(conv.ID); v.Checkin == nil || v.Checkin.Streak != 2 || v.Checkin.Waiting {
		t.Fatalf("at the limit but with no child to check in on, nothing waits: %+v", v.Checkin)
	}
	child, _ := store.CreateConversationMode("default", "agent", "general", "claude", "", "", ai.ModeCoder, t.TempDir())
	if _, code := runSessionsCLI(t, cfg, "settings", "set", child.ID, "--role", "child", "--parent", conv.ID); code != 0 {
		t.Fatal("could not link a child")
	}
	if v := get(conv.ID); v.Checkin == nil || !v.Checkin.Waiting {
		t.Fatalf("at the limit with a child: %+v", v.Checkin)
	}
	if _, code := runSessionsCLI(t, cfg, "settings", "set", conv.ID, "--unattended", "false"); code != 0 {
		t.Fatal("could not turn unattended off")
	}
	if v := get(conv.ID); v.Checkin == nil || v.Checkin.Waiting {
		t.Fatalf("a master that is not unattended is never checked in on, so it is not waiting: %+v", v.Checkin)
	}
	runSessionsCLI(t, cfg, "settings", "set", conv.ID, "--unattended", "true")
	turn("person-1") // a person wrote to it
	if v := get(conv.ID); v.Checkin == nil || v.Checkin.Streak != 0 || v.Checkin.Waiting {
		t.Fatalf("after a person wrote: %+v", v.Checkin)
	}
	if v := get(solo.ID); v.Checkin != nil {
		t.Fatalf("a solo session has none: %+v", v.Checkin)
	}
}

// list counts every command that waits, also the sends held behind a running turn or a pause.
func TestSessionsListCountsSendsWaitingBehindAPause(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	conv, _ := openTestChatStore(t, dbPath).CreateConversationMode("default", "agent", "general", "claude", "", "", ai.ModeCoder, t.TempDir())
	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true})
	db, _ := storage.NewDatabase(dbPath)
	cs := coderhost.NewStore(db.DB)
	cs.EnsureSession("default", conv.ID)
	x, _, _ := cs.GetSession("default", conv.ID)
	x.State = coderhost.StatePaused
	cs.SaveSession(x)
	db.Close()
	if _, code := runSessionsCLI(t, cfg, "send", conv.ID, "waits behind the pause", "--no-host"); code != 0 {
		t.Fatal("send failed")
	}
	out, _ := runSessionsCLI(t, cfg, "list")
	var res struct {
		Sessions []sessionRow `json:"sessions"`
	}
	decodeChatJSON(t, out, &res)
	if len(res.Sessions) != 1 || res.Sessions[0].State != coderhost.StatePaused || res.Sessions[0].Waiting != 1 {
		t.Fatalf("list = %s", out)
	}
}

// Deleting the children of a master lets it go solo again, and a refused delete says how to stop the session.
func TestDeletingChildConversationsLetsTheMasterGoSolo(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	store := openTestChatStore(t, dbPath)
	mk := func() string {
		c, _ := store.CreateConversationMode("default", "agent", "general", "claude", "", "", ai.ModeCoder, t.TempDir())
		return c.ID
	}
	m, k := mk(), mk()
	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true})
	set := func(args ...string) int {
		_, code := runSessionsCLI(t, cfg, append([]string{"settings", "set"}, args...)...)
		return code
	}
	if set(m, "--role", "master") != 0 || set(k, "--role", "child", "--parent", m) != 0 {
		t.Fatal("could not link")
	}
	if set(m, "--role", "solo") == 0 {
		t.Fatal("a master with a child went solo")
	}
	if _, code := runChatHistory(t, dbPath, "default", "delete", k); code != 0 {
		t.Fatal("delete failed")
	}
	if set(m, "--role", "solo") != 0 {
		t.Fatal("a master whose only child was deleted cannot go solo: the child's row was left behind")
	}
	out, _ := runSessionsCLI(t, cfg, "list")
	if strings.Contains(out, k) {
		t.Fatalf("a deleted conversation is still listed: %s", out)
	}
}

func TestDeleteRefusedForARunningTurnNamesTheStopCommand(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	store := openTestChatStore(t, dbPath)
	c, _ := store.CreateConversationMode("default", "agent", "general", "claude", "", "", ai.ModeCoder, t.TempDir())
	store.CreateTurn(c.ID, "default", "t1", "host-1a2b3c4d", "hi")
	cmd := newChatCmd(testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true}))
	cmd.SetArgs([]string{"history", "delete", c.ID})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	err := cmd.Execute()
	if err == nil || exitCodeFor(err) != 3 || !strings.Contains(err.Error(), "coder sessions stop") {
		t.Fatalf("err = %v", err)
	}
	if _, gerr := store.GetConversation(c.ID, "default"); gerr != nil {
		t.Fatal("a refused delete removed the conversation")
	}
}
