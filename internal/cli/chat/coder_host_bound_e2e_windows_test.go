//go:build windows

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
	winE2ERole  = "BOUND_WINE2E_ROLE"
	winE2EDB    = "BOUND_WINE2E_DB"
	winE2EOut   = "BOUND_WINE2E_OUT"
	winE2EFiles = "BOUND_WINE2E_FILES"
	winE2EToken = "BOUND_WINE2E_TOKEN"
)

func winE2EAsk(db *sql.DB, files, tokenID string) string {
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

func winE2EWait(path string) bool {
	for i := 0; i < 600; i++ {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func winE2EWrite(files, name, text string) {
	_ = os.WriteFile(filepath.Join(files, name), []byte(text), 0o600)
}

// init makes this test binary the turn (and the sibling and the descendant) of the test below when the role is set in
// its environment: it runs before the flags are read, so the host can start it with its own arguments as a real turn.
func init() {
	if role := os.Getenv(winE2ERole); role != "" {
		winE2EHelper(role)
		os.Exit(0)
	}
}

func winE2EHelper(role string) {
	keyring.MockInit() // this process never reaches the real credential store; the parent's wrapping key is handed over in a file
	files := os.Getenv(winE2EFiles)
	switch role {
	case "turn":
		fmt.Println(`{"admitted":true,"existed":false,"turn":{"id":"t1"}}`) // the admission line the launcher waits for
		if err := coderhost.StashCredFrame(); err != nil {
			winE2EWrite(files, "frame", "error: "+err.Error())
			return
		}
		secret := os.Getenv(coderhost.SessionTokenEnv)
		if !strings.HasPrefix(secret, procbind.SessionPrefixV2) || os.Getenv(coderhost.CredFDEnv) != "" {
			winE2EWrite(files, "frame", "bad: environment after the frame")
			return
		}
		winE2EWrite(files, "frame", "ok")
		sqldb, err := sql.Open("sqlite", "file:"+filepath.ToSlash(os.Getenv(winE2EDB))+"?mode=ro")
		if err != nil {
			winE2EWrite(files, "frame", "bad: database")
			return
		}
		defer sqldb.Close()
		var id string
		if err := sqldb.QueryRow(`SELECT id FROM coder_session_tokens_v2 WHERE token_hash = ?`, procbind.HashSecret(secret)).Scan(&id); err != nil {
			winE2EWrite(files, "frame", "bad: no row for the secret")
			return
		}
		winE2EWrite(files, "tokenid", id)
		winE2EWait(filepath.Join(files, "answerkey"))
		// the profile's wrapping key lives in the parent's in-memory test keyring: it is handed over through a file
		if kek, err := os.ReadFile(filepath.Join(files, "kek")); err == nil {
			_ = keyring.Set("monoagent-vault", "kek-default", strings.TrimSpace(string(kek)))
		}
		time.Sleep(200 * time.Millisecond)
		winE2EWrite(files, "self", winE2EAsk(sqldb, files, id))
		// the stores' own verify path (VerifySessionToken -> V2Check -> the service), from the turn itself
		verify := func(name string) {
			if _, err := coderhost.NewStore(sqldb).VerifySessionToken(secret); err != nil {
				reasons := ""
				_, _ = procbind.V2Check{DB: sqldb, Logf: func(f string, a ...any) { reasons += fmt.Sprintf(f, a...) + "; " }}.Verify(context.Background(), procbind.KindSession, secret)
				winE2EWrite(files, name+"-why", err.Error()+" | "+reasons)
				winE2EWrite(files, name, "deny")
				return
			}
			winE2EWrite(files, name, "allow")
		}
		verify("storeself")
		// a descendant of the turn is inside its process tree
		d := exec.Command(os.Args[0])
		d.Env = append(os.Environ(), winE2ERole+"=descendant", winE2EToken+"="+id)
		_ = d.Run()
		winE2EWait(filepath.Join(files, "go2"))
		winE2EWrite(files, "after", winE2EAsk(sqldb, files, id))
		verify("storeafter")
		winE2EWait(filepath.Join(files, "release"))
	case "sibling":
		winE2EWrite(files, "sibling", winE2EAsk(nil, files, os.Getenv(winE2EToken)))
	case "descendant":
		winE2EWrite(files, "descendant", winE2EAsk(nil, files, os.Getenv(winE2EToken)))
	}
}

func TestABoundTurnIsAllowedItsOwnTokenAndASiblingIsNot(t *testing.T) {
	if err := procbind.Ready(); err != nil {
		t.Skipf("the machine cannot run the binding service: %v", err)
	}
	files := t.TempDir()
	shortHome := os.Getenv("USERPROFILE") // the process's own test home: a Windows socket path holds 107 bytes, the per-test one is longer
	dbPath := newChatCLITestDB(t)
	t.Setenv("HOME", shortHome)
	t.Setenv("USERPROFILE", shortHome)
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	setCoderSettings(t, dbPath, coderSettings{Enabled: true, PeercredBinding: true})
	if home, err := os.UserHomeDir(); err != nil || os.MkdirAll(filepath.Join(home, ".monoagent"), 0o700) != nil { // the runtime root is made below it
		t.Fatalf("no home folder to place the socket under: %v", err)
	}

	// the turn: this test binary, run as the helper
	bin := filepath.Join(files, "turn.exe") // this test binary, which becomes the turn when the role is set
	self, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, self, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(winE2ERole, "turn")
	t.Setenv(winE2EDB, dbPath)
	t.Setenv(winE2EFiles, files)
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
	g := &gatedLauncher{db: db.DB, svc: binding, inner: legacy, bound: &boundCopy, logf: t.Logf}

	turn, err := g.Start("default", "c1", "t1", "hello", nil, "")
	if err != nil {
		if logs, _ := filepath.Glob(filepath.Join(files, "turns", "*")); len(logs) > 0 {
			for _, l := range logs {
				b, _ := os.ReadFile(l)
				t.Logf("turn log %s:\n%s", filepath.Base(l), b)
			}
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { winE2EWrite(files, "release", "x"); _ = turn.Wait() })

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
	winE2EWrite(files, "kek", kek)
	winE2EWrite(files, "generation", gen)
	winE2EWrite(files, "answerkey", hex.EncodeToString(key))
	read := func(name string) string {
		t.Helper()
		if !winE2EWait(filepath.Join(files, name)) {
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
	sib := exec.Command(os.Args[0])
	sib.Env = append(os.Environ(), winE2ERole+"=sibling", winE2EToken+"="+tokenID)
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
		why, _ := os.ReadFile(filepath.Join(files, "storeself-why"))
		t.Fatalf("the turn through the store's verify path: %q, want allow (%s)", got, why)
	}
	// the conversation's tokens are revoked: the registration is gone and the turn's next question is denied
	if err := store.RevokeSessionTokensErr("default", "c1"); err != nil {
		t.Fatal(err)
	}
	winE2EWrite(files, "go2", "x")
	if got := read("after"); got != "deny" {
		t.Fatalf("the turn asked after its token was revoked: %q, want deny", got)
	}
	if got := read("storeafter"); got != "deny" {
		t.Fatalf("the store's verify path after the token was revoked: %q, want deny", got)
	}
}
