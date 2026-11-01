package chat

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/daemonhb"
	"github.com/monoes/mono-agent/internal/dynorg"
	"github.com/monoes/mono-agent/internal/procbind"
	"github.com/monoes/mono-agent/internal/storage"
)

// The task token of a mission child (phase P-B2). The launcher mints it from the host's own child store, hands it
// over the inherited pipe, binds it to the turn's process, and revokes it when the run ends.

// childFix is a lead with an approved two-task mission whose task 0 has a session of its own.
type childFix struct {
	m     masterTurn
	sid   int64
	ids   []int64
	child string
	db    *storage.Database
	mint  *childTokenMinter
}

func newChildFix(t *testing.T) *childFix {
	t.Helper()
	m := newMasterTurn(t)
	sid, ids := missionOfMaster(t, m, 2, true)
	mc := missionReady(t, m)
	ctx := context.Background()
	if err := mc.Mark(ctx, sid, ids[0], "", 0); err != nil {
		t.Fatal(err)
	}
	if err := mc.Spawn(ctx, sid, ids[0], "the brief"); err != nil {
		t.Fatal(err)
	}
	f, err := mc.Facts(ctx, sid, ids[0])
	if err != nil || f.Child == "" {
		t.Fatalf("facts = %+v %v", f, err)
	}
	db, err := storage.NewDatabase(m.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &childFix{m: m, sid: sid, ids: ids, child: f.Child, db: db, mint: newChildTokenMinter(db.DB)}
}

func (f *childFix) verify(secret string) (agenttokens.Token, error) {
	return agenttokens.NewStore(f.db.DB).VerifyMission(context.Background(), "default", secret)
}

func TestTheLauncherMintsAChildTokenBoundToItsTaskWorkerRoleAndName(t *testing.T) {
	f := newChildFix(t)
	ct, err := f.mint.mint("default", f.child, "master-1")
	if err != nil || ct == nil {
		t.Fatalf("mint = %v %v", ct, err)
	}
	tok, err := f.verify(ct.secret) // not yet bound to a process: it verifies for anyone
	if err != nil || tok.Mission == nil {
		t.Fatalf("verify: %v", err)
	}
	m := tok.Mission
	if m.Session != f.sid || m.Task != f.ids[0] || m.Role != agenttokens.RoleWorker || m.Actor != dynorg.MissionWorkerName(f.sid, f.ids[0]) {
		t.Fatalf("binding = %+v, want worker of session %d task %d as %s", m, f.sid, f.ids[0], dynorg.MissionWorkerName(f.sid, f.ids[0]))
	}
	if m.Owner != dynorg.TurnOwner("default", f.child, "master-1") {
		t.Fatalf("owner = %q: a sweep by conversation could not find it", m.Owner)
	}
}

// A second run for the same child, or for a newer attempt, starts with the only live token.
func TestAChildHasOneLiveTaskTokenAtATime(t *testing.T) {
	f := newChildFix(t)
	first, err := f.mint.mint("default", f.child, "master-1")
	if err != nil || first == nil {
		t.Fatal(first, err)
	}
	second, err := f.mint.mint("default", f.child, "master-2")
	if err != nil || second == nil {
		t.Fatal(second, err)
	}
	if _, err := f.verify(first.secret); err == nil {
		t.Fatal("the first run's token survived the second run's mint")
	}
	if _, err := f.verify(second.secret); err != nil {
		t.Fatalf("the second token: %v", err)
	}
}

func TestNoTokenForAnOrdinarySessionAnEndedChildOrAMissionThatIsOver(t *testing.T) {
	f := newChildFix(t)
	if ct, err := f.mint.mint("default", f.m.master, "turn-9"); ct != nil || err != nil {
		t.Fatalf("the lead got a task token: %v %v", ct, err)
	}
	// the mission stopped: the child's turn must not start, and nothing is minted
	if _, err := f.db.DB.Exec(`UPDATE task_sessions SET state = 'stopped' WHERE id = ?`, f.sid); err != nil {
		t.Fatal(err)
	}
	if ct, err := f.mint.mint("default", f.child, "master-1"); ct != nil || !errors.Is(err, coderhost.ErrChildNotRunnable) {
		t.Fatalf("a stopped mission's child: %v %v", ct, err)
	}
	if _, err := f.db.DB.Exec(`UPDATE task_sessions SET state = 'running' WHERE id = ?`, f.sid); err != nil {
		t.Fatal(err)
	}
	if _, err := coderhost.NewStore(f.db.DB).End("default", f.child, coderhost.EndStopped); err != nil {
		t.Fatal(err)
	}
	if ct, err := f.mint.mint("default", f.child, "master-1"); ct != nil || err != nil {
		t.Fatalf("an ended child got a token: %v %v", ct, err)
	}
}

func TestRevokeChildTokensEndsEveryRunOfTheConversationAndNoOtherChilds(t *testing.T) {
	f := newChildFix(t)
	a, _ := f.mint.mint("default", f.child, "master-1")
	_, otherSecret, err := f.mint.tokens.CreateMission(context.Background(), "default", agenttokens.MissionBinding{Session: f.sid, Task: f.ids[1], Role: agenttokens.RoleWorker, Actor: "w", Owner: dynorg.TurnOwner("default", "another-child", "t")}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.mint.RevokeChildTokens("default", f.child); err != nil {
		t.Fatal(err)
	}
	if _, err := f.verify(a.secret); err == nil {
		t.Fatal("the child's token survived RevokeChildTokens")
	}
	if _, err := f.verify(otherSecret); err != nil {
		t.Fatalf("another conversation's token was revoked: %v", err)
	}
}

// A child run that holds only a v2 task token (a bound launch writes no legacy one) is retired like any other.
func TestRevokeChildTokensRetiresARunThatHoldsOnlyAV2Token(t *testing.T) {
	f := newChildFix(t)
	ctx := context.Background()
	key, err := procbind.RowKey(ctx, f.db.DB, "default")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	row := procbind.MissionTokenRow{ID: "v2-child-run", ProfileID: "default", TokenHash: "h-v2-child-run", Owner: dynorg.TurnOwner("default", f.child, "t1"),
		Session: f.sid, Task: f.ids[0], Role: "worker", Actor: "w", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := procbind.InsertMissionToken(ctx, f.db.DB, key, row); err != nil {
		t.Fatal(err)
	}
	if err := f.mint.RevokeChildTokens("default", f.child); err != nil {
		t.Fatal(err)
	}
	if rv, err := procbind.IsRevoked(ctx, f.db.DB, procbind.KindMission, row.ID); err != nil || !rv {
		t.Fatalf("the v2-only run was not retired: %v %v", rv, err)
	}
}

// fakeChildBin is a turn process that reads the token pipe, records what it was given, and runs until released.
func fakeChildBin(t *testing.T, dir string) (bin string) {
	t.Helper()
	bin = filepath.Join(dir, "fake-monoagentcli")
	script := "#!/bin/sh\necho \"$*\" >> '" + filepath.Join(dir, "args.log") + "'\n" +
		"if [ \"$6\" = \"chat\" ] && [ \"$7\" = \"history\" ]; then exit 0; fi\n" +
		"env > '" + filepath.Join(dir, "env.txt") + "'\n" +
		"if [ -n \"$MONOAGENT_MISSION_FD\" ]; then eval \"read -r tok <&$MONOAGENT_MISSION_FD\"; echo \"$tok\" > '" + filepath.Join(dir, "tok.txt") + "'; fi\n" +
		"cat > /dev/null\n" +
		"echo '{\"admitted\":true,\"existed\":false,\"turn\":{\"id\":\"t1\"}}'\n" +
		"while [ ! -e '" + filepath.Join(dir, "release") + "' ]; do sleep 0.05; done\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestTheChildTurnGetsItsTokenOverThePipeNotTheEnvironmentOrArguments(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script turn")
	}
	f := newChildFix(t)
	dir := t.TempDir()
	l := &execLauncher{bin: fakeChildBin(t, dir), dbPath: f.m.dbPath, instance: "host-test", logDir: filepath.Join(dir, "turns"), child: f.mint}
	turn, err := l.Start("default", f.child, "master-1", "hi", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the turn to record what it was given", func() bool { b, _ := os.ReadFile(filepath.Join(dir, "tok.txt")); return len(b) > 5 })
	b, _ := os.ReadFile(filepath.Join(dir, "tok.txt"))
	secret := strings.TrimSpace(string(b))
	if !strings.HasPrefix(secret, coderhost.TaskTokenPrefix) || coderhost.TaskTokenPrefix != agenttokens.Prefix {
		t.Fatalf("the pipe held %q", secret)
	}
	envText, _ := os.ReadFile(filepath.Join(dir, "env.txt"))
	argsText, _ := os.ReadFile(filepath.Join(dir, "args.log"))
	for name, text := range map[string]string{"environment": string(envText), "arguments": string(argsText)} {
		if strings.Contains(text, secret) {
			t.Errorf("the task token is in the child's %s", name)
		}
		if strings.Contains(text, f.m.token) {
			t.Errorf("the lead's session token is in the child's %s", name)
		}
	}
	if strings.Contains(string(envText), agenttokens.EnvMissionToken+"=") {
		t.Errorf("the launcher put the binding variable in the environment: the child sets it itself after reading the pipe")
	}
	// it is bound to the turn's process: this test process is outside its tree
	if agenttokens.MissionProcessSupported() {
		if _, err := f.verify(secret); !errors.Is(err, agenttokens.ErrNotInWorkerTree) {
			t.Fatalf("a caller outside the turn's tree was accepted: %v", err)
		}
	}
	// the run ends: the token is dead
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = turn.Wait()
	var tokErr error
	if _, tokErr = f.verify(secret); errors.Is(tokErr, agenttokens.ErrNotInWorkerTree) || tokErr == nil {
		t.Fatalf("the token still verifies after the run ended: %v", tokErr)
	}
}

func TestTheLauncherStartsNoTurnForAChildWhoseMissionIsOverOrWithAConfirmation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script turn")
	}
	f := newChildFix(t)
	dir := t.TempDir()
	l := &execLauncher{bin: fakeChildBin(t, dir), dbPath: f.m.dbPath, instance: "host-test", logDir: filepath.Join(dir, "turns"), child: f.mint}
	if _, err := l.Start("default", f.child, "master-1", "hi", nil, "5.abcdef"); err == nil {
		t.Fatal("a child's turn started carrying a mission confirmation")
	}
	if _, err := f.db.DB.Exec(`UPDATE task_sessions SET state = 'stopped' WHERE id = ?`, f.sid); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Start("default", f.child, "master-2", "hi", nil, ""); !errors.Is(err, coderhost.ErrChildNotRunnable) {
		t.Fatalf("a stopped mission's child started: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "args.log")); !os.IsNotExist(err) {
		t.Fatal("a process was started")
	}
	var live int
	_ = f.db.DB.QueryRow(`SELECT COUNT(*) FROM agent_tokens WHERE mission_session > 0 AND revoked_at IS NULL`).Scan(&live)
	if live != 0 {
		t.Fatalf("%d live task tokens after refused starts", live)
	}
}

func TestAnOrdinarySessionsTurnGetsNoPipeAtAll(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script turn")
	}
	f := newChildFix(t)
	dir := t.TempDir()
	l := &execLauncher{bin: fakeChildBin(t, dir), dbPath: f.m.dbPath, instance: "host-test", logDir: filepath.Join(dir, "turns"), child: f.mint}
	turn, err := l.Start("default", f.m.master, "turn-5", "hi", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the turn to start", func() bool { b, _ := os.ReadFile(filepath.Join(dir, "env.txt")); return len(b) > 0 })
	envText, _ := os.ReadFile(filepath.Join(dir, "env.txt"))
	if strings.Contains(string(envText), "MONOAGENT_MISSION_FD") {
		t.Fatal("the lead's turn was handed a pipe")
	}
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = turn.Wait()
}

// ---- the chat process's side ----

func TestTheChildTurnRunsOnlyWithTheTokenOfItsOwnTask(t *testing.T) {
	f := newChildFix(t)
	t.Setenv(agenttokens.EnvMissionToken, "")
	own, _ := f.mint.mint("default", f.child, "master-1")
	// a token for the other task of the same mission
	_, otherSecret, err := f.mint.tokens.CreateMission(context.Background(), "default", agenttokens.MissionBinding{Session: f.sid, Task: f.ids[1], Role: agenttokens.RoleWorker, Actor: "w", Owner: "o"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, verifierSecret, err := f.mint.tokens.CreateMission(context.Background(), "default", agenttokens.MissionBinding{Session: f.sid, Task: f.ids[0], Role: agenttokens.RoleVerifier, Actor: "v", Owner: "o"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct{ piped string }{
		"none":              {""},
		"another task":      {otherSecret},
		"a verifier":        {verifierSecret},
		"a confirmation":    {"5.abcdef"},
		"a made-up token":   {agenttokens.Prefix + "AAAA"},
		"a revoked/unknown": {agenttokens.Prefix + "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(agenttokens.EnvMissionToken, "")
			if _, err := childTaskGate(f.db.DB, "default", f.child, tc.piped); err == nil {
				t.Fatalf("the child ran with %s", name)
			}
			if v := os.Getenv(agenttokens.EnvMissionToken); v != "" {
				t.Fatalf("a refused turn left %q in the environment", v)
			}
		})
	}
	t.Setenv(agenttokens.EnvMissionToken, "")
	conf, err := childTaskGate(f.db.DB, "default", f.child, own.secret)
	if err != nil || conf != "" {
		t.Fatalf("own token: %q %v", conf, err)
	}
	if os.Getenv(agenttokens.EnvMissionToken) != own.secret {
		t.Fatal("the child's shells would not find their binding")
	}
}

func TestOnlyAChildTurnTakesATaskTokenAndAConfirmationStaysAConfirmation(t *testing.T) {
	f := newChildFix(t)
	t.Setenv(agenttokens.EnvMissionToken, "")
	conf, err := childTaskGate(f.db.DB, "default", f.m.master, "7.nonce")
	if err != nil || conf != "7.nonce" {
		t.Fatalf("the lead's confirmation: %q %v", conf, err)
	}
	if _, err := childTaskGate(f.db.DB, "default", f.m.master, agenttokens.Prefix+"xxxx"); err == nil {
		t.Fatal("a task token was accepted for the lead's own turn")
	}
	if os.Getenv(agenttokens.EnvMissionToken) != "" {
		t.Fatal("the lead's environment holds a binding")
	}
}

func TestAnEndedChildsLaterTurnIsNotBlockedByTheGate(t *testing.T) {
	f := newChildFix(t)
	t.Setenv(agenttokens.EnvMissionToken, "")
	if _, err := coderhost.NewStore(f.db.DB).End("default", f.child, coderhost.EndDone); err != nil {
		t.Fatal(err)
	}
	if conf, err := childTaskGate(f.db.DB, "default", f.child, ""); err != nil || conf != "" {
		t.Fatalf("an ended child: %q %v", conf, err)
	}
}

// ---- readiness ----

func resetChildReadiness(t *testing.T) {
	t.Helper()
	childMinterMu.Lock()
	childMinter, childHostProbe = nil, nil
	childMinterMu.Unlock()
	t.Cleanup(func() {
		childMinterMu.Lock()
		childMinter, childHostProbe = nil, nil
		childMinterMu.Unlock()
		coderhost.SetMissionChildrenReady(nil)
	})
	coderhost.SetMissionChildrenReady(nil)
}

func TestTheGateOpensOnlyWhenTheLauncherMintsAndStaysShutOtherwise(t *testing.T) {
	resetChildReadiness(t)
	if launcherMintsChildTokens() || coderhost.MissionChildrenReady() {
		t.Fatal("ready with no launcher")
	}
	RegisterChildTokenMinter(&childTokenMinter{}) // a launcher that cannot (no stores)
	if launcherMintsChildTokens() || coderhost.MissionChildrenReady() {
		t.Fatal("ready with a launcher that cannot mint")
	}
	f := newChildFix(t)
	RegisterChildTokenMinter(f.mint)
	if !launcherMintsChildTokens() || !coderhost.MissionChildrenReady() {
		t.Fatal("not ready with a launcher that mints")
	}
}

// The lead's chat process is not the host: it asks the running host, and an older or absent host is a no.
func TestAProcessOtherThanTheHostAsksTheRunningHost(t *testing.T) {
	resetChildReadiness(t)
	dbPath := newChatCLITestDB(t)
	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true})
	registerChildHostProbe(cfg)
	if launcherMintsChildTokens() {
		t.Fatal("ready with no host running")
	}
	if err := os.MkdirAll(hostDir(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	writeInfo := func(mints bool) {
		raw := `{"pid":1,"stamp":"s","instance":"host-x","startedAt":"now"`
		if mints {
			raw += `,"childTokens":true`
		}
		if err := os.WriteFile(filepath.Join(hostDir(cfg), hostInfoFile), []byte(raw+"}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeInfo(true)
	if launcherMintsChildTokens() {
		t.Fatal("ready from a stale info file with no host holding the lock")
	}
	release := holdHostLock(t, cfg)
	defer release()
	if !launcherMintsChildTokens() {
		t.Fatal("not ready though a running host says it mints")
	}
	writeInfo(false)
	if launcherMintsChildTokens() {
		t.Fatal("ready with a host that does not say it mints child tokens (built before this existed)")
	}
}

// With a fresh store and the default policy nothing is marked, so nothing spawns, even with a launcher that mints:
// the policy is stored only when someone writes it, and a lead marks tasks only by saying so in its plan.
func TestAFreshMissionSpawnsNothingEvenWithTheMinterRegistered(t *testing.T) {
	resetChildReadiness(t)
	f := newChildFix(t)
	RegisterChildTokenMinter(f.mint)
	// a second mission of the same lead with nothing marked
	if _, err := f.db.DB.Exec(`UPDATE task_sessions SET conversation_id = NULL WHERE id = ?`, f.sid); err != nil {
		t.Fatal(err)
	}
	sid2, ids2 := missionOfMaster(t, f.m, 2, true)
	mc := newMissionChildren(f.db.DB, "default", f.m.master)
	if !mc.Ready() {
		t.Fatal("the spawner should be ready with the minter registered")
	}
	cs := coderhost.NewStore(f.db.DB)
	if p, err := cs.GetPolicy("default", sid2); err != nil || p.Stored {
		t.Fatalf("a fresh mission has a stored policy: %+v %v", p, err)
	}
	for _, id := range ids2 {
		facts, err := mc.Facts(context.Background(), sid2, id)
		if err != nil || facts.Row != "" {
			t.Fatalf("facts = %+v %v: a task nobody marked has no row", facts, err)
		}
	}
	rows, err := cs.ChildrenOfSession("default", sid2)
	if err != nil || len(rows) != 0 {
		t.Fatalf("rows = %+v %v", rows, err)
	}
}

// holdHostLock stands in for a running host: it holds the lock a host holds for as long as it runs.
func holdHostLock(t *testing.T, cfg *Deps) func() {
	t.Helper()
	release, err := daemonhb.LockFile(hostLockPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	return release
}

// The chat process itself refuses a child's turn that has no task token, whoever started it: the desktop app, a
// shell, or a host built before the launcher minted tokens.
func TestAChildTurnStartedWithoutATaskTokenFailsBeforeAnythingRuns(t *testing.T) {
	f := newChildFix(t)
	t.Setenv(agenttokens.EnvMissionToken, "")
	bin, argsLog := writeArgsLoggingMonomind(t, `  echo '{"v":1,"type":"done","exit_code":0}'`)
	out, err := runChatCmd(t, f.m.dbPath, bin, "--conversation", f.child, "--turn", "master-77", "--instance", "inst-1", "--", "do the task")
	if err == nil || !strings.Contains(err.Error()+out, "task token") {
		t.Fatalf("the child's turn ran without a token: %v\n%s", err, out)
	}
	if _, serr := os.Stat(argsLog); !os.IsNotExist(serr) {
		t.Fatal("the runtime was executed for a child without a task token")
	}
}
