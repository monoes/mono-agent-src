//go:build linux

package chat

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"github.com/monoes/mono-agent/internal/testhome"
	"github.com/zalando/go-keyring"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/procbind"
	"github.com/monoes/mono-agent/internal/storage"
)

// The end to end proof of the bound launch, with the issuance switch standing in for the flipped constant: the real gate,
// the real bound launcher, the real binding service on a real socket, and a real turn process (this test binary
// re-executed as the turn). The turn reads its credential frame the way the real binary does (StashCredFrame, first),
// then asks the service about its own token: allowed. A sibling process of the same user, which is not a descendant of the
// turn, asks about the same token: denied. When the conversation's tokens are revoked the registration is gone and the
// turn's next question is denied.

const (
	e2eRole  = "BOUND_E2E_ROLE"
	e2eDB    = "BOUND_E2E_DB"
	e2eOut   = "BOUND_E2E_OUT"
	e2eFiles = "BOUND_E2E_FILES"
	e2eToken = "BOUND_E2E_TOKEN"
)

func e2eAsk(db *sql.DB, files, tokenID string) string {
	keyHex, err1 := os.ReadFile(filepath.Join(files, "answerkey"))
	gen, err2 := os.ReadFile(filepath.Join(files, "generation"))
	if err1 != nil || err2 != nil {
		return "nokey"
	}
	key, _ := hex.DecodeString(strings.TrimSpace(string(keyHex)))
	conn, err := procbind.DialProfile("default")
	if err != nil {
		return "nodial"
	}
	defer conn.Close()
	v, err := procbind.Client{Key: key, Profile: "default", Generation: strings.TrimSpace(string(gen)), Timeout: 3 * time.Second}.Verify(conn, tokenID, procbind.SessionScope("c1", "t1"))
	if err != nil {
		return "error"
	}
	return string(v)
}

func e2eWait(path string) bool {
	for i := 0; i < 600; i++ {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func e2eWrite(files, name, text string) {
	_ = os.WriteFile(filepath.Join(files, name), []byte(text), 0o600)
}

// TestHelperBoundE2E is the turn (and the sibling) of the test below; it does nothing in a normal run.
func TestHelperBoundE2E(t *testing.T) {
	role := os.Getenv(e2eRole)
	if role == "" {
		t.Skip("helper process only")
	}
	files := os.Getenv(e2eFiles)
	switch role {
	case "turn":
		fmt.Println(`{"admitted":true,"existed":false,"turn":{"id":"t1"}}`) // the admission line the launcher waits for
		if err := coderhost.StashCredFrame(); err != nil {
			e2eWrite(files, "frame", "error: "+err.Error())
			return
		}
		secret := os.Getenv(coderhost.SessionTokenEnv)
		if !strings.HasPrefix(secret, procbind.SessionPrefixV2) || os.Getenv(coderhost.CredFDEnv) != "" {
			e2eWrite(files, "frame", "bad: environment after the frame")
			return
		}
		e2eWrite(files, "frame", "ok")
		sqldb, err := sql.Open("sqlite", "file:"+os.Getenv(e2eDB)+"?mode=ro")
		if err != nil {
			e2eWrite(files, "frame", "bad: database")
			return
		}
		defer sqldb.Close()
		var id string
		if err := sqldb.QueryRow(`SELECT id FROM coder_session_tokens_v2 WHERE token_hash = ?`, procbind.HashSecret(secret)).Scan(&id); err != nil {
			e2eWrite(files, "frame", "bad: no row for the secret")
			return
		}
		e2eWrite(files, "tokenid", id)
		e2eWait(filepath.Join(files, "answerkey"))
		// the profile's wrapping key lives in the parent's in-memory test keyring: it is handed over through a file
		if kek, err := os.ReadFile(filepath.Join(files, "kek")); err == nil {
			_ = keyring.Set("monoagent-vault", "kek-default", strings.TrimSpace(string(kek)))
		}
		procbind.ForTestSetIssuance(t, true) // this process stands in for a build that issues bound tokens
		time.Sleep(200 * time.Millisecond)
		e2eWrite(files, "self", e2eAsk(sqldb, files, id))
		// the stores' own verify path (VerifySessionToken -> V2Check -> the service), from the turn itself
		verify := func(name string) {
			if _, err := coderhost.NewStore(sqldb).VerifySessionToken(secret); err != nil {
				e2eWrite(files, name, "deny")
				return
			}
			e2eWrite(files, name, "allow")
		}
		verify("storeself")
		// a descendant of the turn is inside its process tree
		d := exec.Command(os.Args[0], "-test.run=^TestHelperBoundE2E$", "-test.count=1")
		d.Env = append(os.Environ(), e2eRole+"=descendant", e2eToken+"="+id)
		_ = d.Run()
		e2eWait(filepath.Join(files, "go2"))
		e2eWrite(files, "after", e2eAsk(sqldb, files, id))
		verify("storeafter")
		e2eWait(filepath.Join(files, "release"))
	case "sibling":
		e2eWrite(files, "sibling", e2eAsk(nil, files, os.Getenv(e2eToken)))
	case "descendant":
		e2eWrite(files, "descendant", e2eAsk(nil, files, os.Getenv(e2eToken)))
	}
}

func TestABoundTurnIsAllowedItsOwnTokenAndASiblingIsNot(t *testing.T) {
	if !procbind.PeerPidfdSupported() {
		t.Skip("the machine cannot run the binding service")
	}
	procbind.ForTestSetIssuance(t, true)
	files := t.TempDir()
	runtimeDir, err := os.MkdirTemp("", "e2e")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(runtimeDir) })
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir) // the service and every verifier derive the socket directory from it
	dbPath := newChatCLITestDB(t)
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	setCoderSettings(t, dbPath, coderSettings{Enabled: true, PeercredBinding: true})

	// the turn: this test binary, run as the helper
	bin := filepath.Join(files, "turn.sh")
	script := "#!/bin/sh\nexec " + os.Args[0] + " -test.run='^TestHelperBoundE2E$' -test.count=1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(e2eRole, "turn")
	t.Setenv(e2eDB, dbPath)
	t.Setenv(e2eFiles, files)
	t.Setenv(testhome.KeepHomeEnv, "1") // the helper keeps this test's home

	store := coderhost.NewStore(db.DB)
	if x, err := store.EnsureSession("default", "c1"); err != nil {
		t.Fatal(err)
	} else {
		x.State, x.TurnID = coderhost.StateRunning, "t1"
		if err := store.SaveSession(x); err != nil {
			t.Fatal(err)
		}
	}
	binding := &coderhost.BindingHost{DB: db.DB, Binder: procbind.NewBinder(time.Now)}
	t.Cleanup(binding.Shutdown)
	legacy := &execLauncher{bin: bin, dbPath: "/db/x.db", instance: "host-e2e", logDir: filepath.Join(files, "turns"), revokeBackoff: time.Millisecond,
		tokens: func(string, string, string) (string, error) { t.Error("the legacy token minter ran"); return "", nil },
		revoke: store.RevokeSessionTokensErr}
	boundCopy := *legacy
	boundCopy.bound = newBoundLaunch(db.DB, binding, binding.Binder, store, nil)
	g := &gatedLauncher{db: db.DB, svc: binding, inner: legacy, bound: &boundCopy}

	turn, err := g.Start("default", "c1", "t1", "hello", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e2eWrite(files, "release", "x"); _ = turn.Wait() })

	// the verifier side: the answer key of the running service's generation
	gen, ok := binding.Generation("default")
	if !ok {
		t.Fatal("no running service")
	}
	key, err := procbind.AnswerKeyIfExists(context.Background(), db.DB, "default", gen)
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
	if got := read("self"); got != "allow" {
		t.Fatalf("the turn asked about its own token: %q, want allow", got)
	}
	// a sibling: a process of the same user that is not a descendant of the turn
	tokenID := read("tokenid")
	sib := exec.Command(os.Args[0], "-test.run=^TestHelperBoundE2E$", "-test.count=1")
	sib.Env = append(os.Environ(), e2eRole+"=sibling", e2eToken+"="+tokenID)
	if out, err := sib.CombinedOutput(); err != nil {
		t.Fatalf("the sibling: %v\n%s", err, out)
	}
	if got := read("sibling"); got != "deny" {
		t.Fatalf("a sibling asked about the turn's token: %q, want deny", got)
	}
	if got := read("descendant"); got != "allow" {
		t.Fatalf("a descendant of the turn asked about its token: %q, want allow", got)
	}
	if got := read("storeself"); got != "allow" {
		t.Fatalf("the turn through the store's verify path: %q, want allow", got)
	}
	// the conversation's tokens are revoked: the registration is gone and the turn's next question is denied
	if err := store.RevokeSessionTokensErr("default", "c1"); err != nil {
		t.Fatal(err)
	}
	e2eWrite(files, "go2", "x")
	if got := read("after"); got != "deny" {
		t.Fatalf("the turn asked after its token was revoked: %q, want deny", got)
	}
	if got := read("storeafter"); got != "deny" {
		t.Fatalf("the store's verify path after the token was revoked: %q, want deny", got)
	}
}
