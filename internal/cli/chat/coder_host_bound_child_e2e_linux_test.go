//go:build linux

package chat

import (
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/procbind"
	"github.com/monoes/mono-agent/internal/testhome"
)

// The end to end proof for a bound mission child: the real gate and bound launcher start a turn (this test binary run
// as the helper) that receives its task token in the credential frame and passes it through childTaskGate, the chat
// turn's own check: taskbind.Resolve -> VerifyMission -> the v2 verifier -> the binding service, which answers for the
// turn's own process. The child's task token exists only in agent_tokens_v2. When the run's tokens are retired the
// turn's next check fails.

// TestHelperBoundChildE2E is the turn of the test below; it does nothing in a normal run.
func TestHelperBoundChildE2E(t *testing.T) {
	if os.Getenv(e2eRole) != "child" {
		t.Skip("helper process only")
	}
	files := os.Getenv(e2eFiles)
	procbind.ForTestSetIssuance(t, true)                                // this process stands in for a build that issues bound tokens: its stores verify v2 secrets
	fmt.Println(`{"admitted":true,"existed":false,"turn":{"id":"t1"}}`) // the admission line the launcher waits for
	if err := coderhost.StashCredFrame(); err != nil {
		e2eWrite(files, "frame", "error: "+err.Error())
		return
	}
	token := coderhost.TakeMissionToken()
	if !strings.HasPrefix(token, procbind.TaskPrefixV2) {
		e2eWrite(files, "frame", "bad: no v2 task token in the frame")
		return
	}
	e2eWrite(files, "frame", "ok")
	sqldb, err := sql.Open("sqlite", "file:"+os.Getenv(e2eDB)+"?mode=ro")
	if err != nil {
		e2eWrite(files, "frame", "bad: database")
		return
	}
	defer sqldb.Close()
	gate := func(name string) {
		if _, err := childTaskGate(sqldb, "default", os.Getenv(e2eToken), token); err != nil {
			e2eWrite(files, name, "refused: "+err.Error())
			return
		}
		e2eWrite(files, name, "ok")
	}
	e2eWait(filepath.Join(files, "answerkey"))
	// the profile's wrapping key lives in the test keyring of the parent process (an in-memory mock): hand it over
	kek, _ := os.ReadFile(filepath.Join(files, "kek"))
	if err := keyring.Set("monoagent-vault", "kek-default", strings.TrimSpace(string(kek))); err != nil {
		e2eWrite(files, "gate1", "refused: seeding the keyring: "+err.Error())
		return
	}
	time.Sleep(200 * time.Millisecond)
	gate("gate1")
	e2eWait(filepath.Join(files, "go2"))
	gate("gate2")
	e2eWait(filepath.Join(files, "release"))
}

func TestABoundChildPassesItsOwnTaskGateAndIsRefusedOnceRetired(t *testing.T) {
	if !procbind.PeerPidfdSupported() {
		t.Skip("the machine cannot run the binding service")
	}
	procbind.ForTestSetIssuance(t, true)
	f := newChildFix(t)
	files := t.TempDir()
	runtimeDir, err := os.MkdirTemp("", "e2e")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(runtimeDir) })
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	setCoderSettings(t, f.m.dbPath, coderSettings{Enabled: true, PeercredBinding: true})

	bin := filepath.Join(files, "turn.sh")
	script := "#!/bin/sh\nexec " + os.Args[0] + " -test.run='^TestHelperBoundChildE2E$' -test.count=1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(testhome.KeepHomeEnv, "1") // the helper keeps this test's home: the profile's row key is wrapped by a key kept there
	t.Setenv(e2eRole, "child")
	t.Setenv(e2eDB, f.m.dbPath)
	t.Setenv(e2eFiles, files)
	t.Setenv(e2eToken, f.child) // the helper reads the child's conversation id here

	store := coderhost.NewStore(f.db.DB)
	x, err := store.EnsureSession("default", f.child)
	if err != nil {
		t.Fatal(err)
	}
	x.State, x.TurnID = coderhost.StateRunning, "t1"
	if err := store.SaveSession(x); err != nil {
		t.Fatal(err)
	}
	binding := &coderhost.BindingHost{DB: f.db.DB, Binder: procbind.NewBinder(time.Now)}
	t.Cleanup(binding.Shutdown)
	legacy := &execLauncher{bin: bin, dbPath: "/db/x.db", instance: "host-e2e", logDir: filepath.Join(files, "turns"), revokeBackoff: time.Millisecond,
		tokens: func(string, string, string) (string, error) { t.Error("the legacy token minter ran"); return "", nil },
		revoke: store.RevokeSessionTokensErr, child: f.mint}
	boundCopy := *legacy
	boundCopy.bound = newBoundLaunch(f.db.DB, binding, binding.Binder, store, f.mint)
	g := &gatedLauncher{db: f.db.DB, svc: binding, inner: legacy, bound: &boundCopy}

	turn, err := g.Start("default", f.child, "t1", "hello", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e2eWrite(files, "release", "x"); _ = turn.Wait() })

	gen, ok := binding.Generation("default")
	if !ok {
		t.Fatal("no running service")
	}
	key, err := procbind.AnswerKeyIfExists(t.Context(), f.db.DB, "default", gen)
	if err != nil {
		t.Fatal(err)
	}
	kek, err := keyring.Get("monoagent-vault", "kek-default")
	if err != nil {
		t.Fatal(err)
	}
	e2eWrite(files, "kek", kek)
	e2eWrite(files, "generation", gen)
	e2eWrite(files, "answerkey", hex.EncodeToString(key))
	read := func(name string) string {
		t.Helper()
		if !e2eWait(filepath.Join(files, name)) {
			t.Fatalf("the turn never wrote %s", name)
		}
		time.Sleep(50 * time.Millisecond)
		b, _ := os.ReadFile(filepath.Join(files, name))
		return strings.TrimSpace(string(b))
	}
	if got := read("frame"); got != "ok" {
		t.Fatalf("the turn's credential frame: %s", got)
	}
	// the child's task token is in the v2 table only
	var legacyRows int
	if err := f.db.DB.QueryRow(`SELECT COUNT(*) FROM agent_tokens WHERE mission_owner LIKE ?`, "%"+f.child+"%").Scan(&legacyRows); err != nil || legacyRows != 0 {
		t.Fatalf("legacy task tokens for the bound child: %d %v", legacyRows, err)
	}
	if got := read("gate1"); got != "ok" {
		t.Fatalf("the bound child's own task gate: %s", got)
	}
	if err := f.mint.RevokeChildTokens("default", f.child); err != nil {
		t.Fatal(err)
	}
	e2eWrite(files, "go2", "x")
	if got := read("gate2"); !strings.HasPrefix(got, "refused") {
		t.Fatalf("the task gate after the run's tokens were retired: %q, want refused", got)
	}
}
