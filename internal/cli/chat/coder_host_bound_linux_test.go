//go:build linux

package chat

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/procbind"
	"github.com/monoes/mono-agent/internal/storage"
)

type fakeBoundHost struct {
	gen string
	ok  bool
}

func (f fakeBoundHost) Ensure(context.Context, string) error { return nil }
func (f fakeBoundHost) Generation(string) (string, bool)     { return f.gen, f.ok }

type boundRig struct {
	t       *testing.T
	db      *storage.Database
	store   *coderhost.Store
	binder  *procbind.Binder
	l       *execLauncher
	dir     string
	frame   string
	envlog  string
	pidfile string
	release string
	legacy  int // calls of the legacy token minter
}

// newBoundRig makes a launcher with binding on whose turn is a shell script: it records its environment and pid, copies
// the frame from descriptor 3 to a file, then waits for the test to release it (so the test can look at the rows while
// the turn is alive).
func newBoundRig(t *testing.T, script string) *boundRig {
	t.Helper()
	db, err := storage.NewDatabase(newChatCLITestDB(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return newBoundRigOn(t, db, script)
}

// newBoundRigOn is newBoundRig over a database the test already has.
func newBoundRigOn(t *testing.T, db *storage.Database, script string) *boundRig {
	t.Helper()
	r := &boundRig{t: t, db: db, dir: t.TempDir()}
	r.frame, r.envlog, r.pidfile, r.release = filepath.Join(r.dir, "frame"), filepath.Join(r.dir, "env"), filepath.Join(r.dir, "pid"), filepath.Join(r.dir, "release")
	if script == "" {
		script = `echo $$ > '` + r.pidfile + `'
env | sort > '` + r.envlog + `'
cat <&3 > '` + r.frame + `'
while [ ! -e '` + r.release + `' ]; do sleep 0.05; done
`
	}
	bin := filepath.Join(r.dir, "fake-monoagentcli")
	body := "#!/bin/sh\necho '{\"admitted\":true,\"existed\":false,\"turn\":{\"id\":\"t1\"}}'\n" + script
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.WriteFile(r.release, []byte("x"), 0o600) }) // a turn left by a failing test ends
	r.store = coderhost.NewStore(db.DB)
	r.binder = procbind.NewBinder(time.Now)
	t.Cleanup(r.binder.Close)
	r.l = &execLauncher{bin: bin, dbPath: "/db/x.db", instance: "host-test", logDir: filepath.Join(r.dir, "turns"), revokeBackoff: time.Millisecond,
		tokens: func(string, string, string) (string, error) { r.legacy++; return "mst-legacy", nil },
		revoke: r.store.RevokeSessionTokensErr,
	}
	r.l.bound = newBoundLaunch(db.DB, fakeBoundHost{"gen-1", true}, r.binder, r.store, r.l.child)
	if db.DB != nil {
		r.session("default", "c1", "t1")
	}
	return r
}

// session makes the conversation's session row with turn as its current (running) turn.
func (r *boundRig) session(profile, conv, turn string) {
	r.t.Helper()
	x, err := r.store.EnsureSession(profile, conv)
	if err != nil {
		r.t.Fatal(err)
	}
	x.State, x.TurnID = coderhost.StateRunning, turn
	if err := r.store.SaveSession(x); err != nil {
		r.t.Fatal(err)
	}
}

func (r *boundRig) v2Rows() (tokens, bindings int) {
	r.t.Helper()
	if err := r.db.DB.QueryRow(`SELECT COUNT(*) FROM coder_session_tokens_v2`).Scan(&tokens); err != nil {
		r.t.Fatal(err)
	}
	if err := r.db.DB.QueryRow(`SELECT COUNT(*) FROM process_bindings`).Scan(&bindings); err != nil {
		r.t.Fatal(err)
	}
	return
}

func (r *boundRig) waitFile(path string) []byte {
	r.t.Helper()
	for i := 0; i < 400; i++ {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			return b
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.t.Fatalf("%s never appeared", path)
	return nil
}

func (r *boundRig) gone(pid int) bool {
	for i := 0; i < 300; i++ {
		if err := syscall.Kill(pid, 0); err != nil {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// The bound launch: the turn has no token in its environment, only the frame pipe; the frame carries a v2 session
// secret; the token and its binding are in the database with the turn's own pid, stamp and the host's generation; the
// entry is active in the registry; the legacy minter was never called and the legacy table is empty. When the turn ends
// the token is revoked and the registration is gone.
func TestABoundTurnIsLaunchedBoundAndNothingIsInItsEnvironment(t *testing.T) {
	r := newBoundRig(t, "")
	turn, err := r.l.Start("default", "c1", "t1", "hello", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	raw := r.waitFile(r.frame)
	f, err := procbind.ReadCredFrame(bytes.NewReader(raw))
	if err != nil || !strings.HasPrefix(string(f.SessionToken), procbind.SessionPrefixV2) || f.TaskToken != nil || f.MissionConfirmation != nil {
		t.Fatalf("frame: %v %v", f, err)
	}
	secret := string(f.SessionToken)
	env := string(r.waitFile(r.envlog))
	if !strings.Contains(env, "MONOAGENT_CRED_FD=3\n") || strings.Contains(env, coderhost.SessionTokenEnv+"=") || strings.Contains(env, coderhost.MissionFDEnv+"=") || strings.Contains(env, secret) {
		t.Errorf("the turn's environment: %s", env)
	}
	if r.legacy != 0 {
		t.Error("the legacy token minter was called")
	}
	var legacyRows int
	if err := r.db.DB.QueryRow(`SELECT COUNT(*) FROM coder_session_tokens`).Scan(&legacyRows); err != nil || legacyRows != 0 {
		t.Errorf("legacy session tokens: %d %v", legacyRows, err)
	}
	// the rows
	ctx := context.Background()
	var id string
	if err := r.db.DB.QueryRow(`SELECT id FROM coder_session_tokens_v2`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	key, err := procbind.RowKeyIfExists(ctx, r.db.DB, "default")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := procbind.GetSessionToken(ctx, r.db.DB, key, "default", id)
	if err != nil || tok.TokenHash != procbind.HashSecret(secret) || tok.ConversationID != "c1" || tok.TurnID != "t1" {
		t.Fatalf("token row: %+v %v", tok, err)
	}
	bind, err := procbind.GetBinding(ctx, r.db.DB, key, procbind.KindSession, "default", id)
	if err != nil || bind.WorkerPID != turn.PID() || bind.WorkerStamp != coderhost.ProcStamp(turn.PID()) || bind.HostGeneration != "gen-1" || bind.Scope != procbind.SessionScope("c1", "t1") {
		t.Fatalf("binding row: %+v %v (pid %d)", bind, err, turn.PID())
	}
	lease, ok := r.binder.Acquire(id, "default", procbind.SessionScope("c1", "t1"))
	if !ok {
		t.Fatal("the entry is not active in the registry")
	}
	lease.Release()
	// the turn ends: the token is revoked and the registration is gone
	if err := os.WriteFile(r.release, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = turn.Wait()
	if rv, err := procbind.IsRevoked(ctx, r.db.DB, procbind.KindSession, id); err != nil || !rv {
		t.Errorf("the token survived the turn: %v %v", rv, err)
	}
	if l, ok := r.binder.Acquire(id, "default", procbind.SessionScope("c1", "t1")); ok {
		l.Release()
		t.Error("the registration survived the turn")
	}
}

// A mission-confirmation turn gets the confirmation in the frame (kind 3) and not through the legacy pipe.
func TestABoundTurnCarriesTheMissionConfirmationInTheFrame(t *testing.T) {
	r := newBoundRig(t, "")
	turn, err := r.l.Start("default", "c1", "t1", "mission", nil, "12.nonceNONCEnonce")
	if err != nil {
		t.Fatal(err)
	}
	f, err := procbind.ReadCredFrame(bytes.NewReader(r.waitFile(r.frame)))
	if err != nil || string(f.MissionConfirmation) != "12.nonceNONCEnonce" || f.SessionToken == nil {
		t.Fatalf("frame: %v %v", f, err)
	}
	if env := string(r.waitFile(r.envlog)); strings.Contains(env, coderhost.MissionFDEnv+"=") {
		t.Error("the legacy mission pipe marker was set as well")
	}
	_ = os.WriteFile(r.release, []byte("x"), 0o600)
	_ = turn.Wait()
}

// Every failure before the frame is written kills the turn through its pidfd, leaves no live registration and no
// unrevoked token, and delivers nothing.
func TestABoundLaunchThatFailsBeforeDeliveryKillsTheTurnAndLeavesNothing(t *testing.T) {
	cases := map[string]struct {
		prepare func(r *boundRig)
		want    string
	}{
		"the session's current turn is another": {func(r *boundRig) { r.session("default", "c1", "other-turn") }, "current turn"},
		"the session is gone": {func(r *boundRig) {
			if _, err := r.db.DB.Exec(`UPDATE coder_sessions SET conversation_id = 'renamed' WHERE conversation_id = 'c1'`); err != nil {
				t.Fatal(err)
			}
		}, "session is gone"},
		"no generation for the profile": {func(r *boundRig) { r.l.bound.host = fakeBoundHost{ok: false} }, "no generation"},
		"revoked between the mint and the activation": {func(r *boundRig) {
			r.l.bound.afterMint = func() {
				if err := r.store.RevokeSessionTokensErr("default", "c1"); err != nil {
					t.Error(err)
				}
			}
		}, "revoked before"},
	}
	// the turn has a descendant in its process group, which must die with it
	script := `d=$(dirname "$0")
echo $$ > "$d/pid"
sleep 300 &
echo $! > "$d/descendant"
cat <&3 > "$d/frame"
while [ ! -e "$d/release" ]; do sleep 0.05; done
`
	for name, c := range cases {
		r := newBoundRig(t, script)
		// the leader is reaped before the launcher's own stop arrives: only the mark made before the kill can end the group
		r.l.bound.afterKill = func() { time.Sleep(300 * time.Millisecond) }
		c.prepare(r)
		_, err := r.l.Start("default", "c1", "t1", "hi", nil, "")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want it to say %q", name, err, c.want)
			continue
		}
		if b, err := os.ReadFile(r.pidfile); err == nil {
			pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
			if pid > 0 && !r.gone(pid) {
				t.Errorf("%s: the turn (pid %d) is still running", name, pid)
			}
		}
		if b, err := os.ReadFile(filepath.Join(r.dir, "descendant")); err == nil {
			dpid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
			if dpid > 0 && !r.gone(dpid) {
				_ = syscall.Kill(dpid, syscall.SIGKILL)
				t.Errorf("%s: the turn's descendant (pid %d) outlived the failed delivery", name, dpid)
			}
		}
		if raw, err := os.ReadFile(r.frame); err == nil && len(raw) > 0 {
			t.Errorf("%s: a frame was delivered", name)
		}
		if st := r.binder.Stats(); st.Active != 0 || st.Pending != 0 {
			t.Errorf("%s: live registrations remain: %+v", name, st)
		}
		// whatever was inserted is revoked
		var live int
		if err := r.db.DB.QueryRow(`SELECT COUNT(*) FROM coder_session_tokens_v2 t WHERE NOT EXISTS (SELECT 1 FROM token_revocations_v2 r WHERE r.token_kind = 'session' AND r.token_id = t.id)`).Scan(&live); err != nil || live != 0 {
			t.Errorf("%s: %d unrevoked v2 token(s) remain (%v)", name, live, err)
		}
	}
}

// Descriptors do not leak: many bound turns, started and finished, leave this process with the descriptors it had.
func TestManyBoundTurnsLeakNoDescriptors(t *testing.T) {
	r := newBoundRig(t, "cat <&3 > /dev/null\n")
	count := func() int {
		ents, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Fatal(err)
		}
		return len(ents)
	}
	// the first turn warms anything lazily opened
	run := func(i int) {
		turn := "t" + strconv.Itoa(i)
		r.session("default", "c1", turn)
		tr, err := r.l.Start("default", "c1", turn, "hi", nil, "")
		if err != nil {
			t.Fatalf("turn %d: %v", i, err)
		}
		_ = tr.Wait()
	}
	run(0)
	before := count()
	for i := 1; i <= 150; i++ {
		run(i)
	}
	if after := count(); after > before+3 {
		t.Errorf("descriptors: %d before, %d after 150 bound turns", before, after)
	}
}

// The caller's own checks before a mint, which MintTx does not make: the profile whose key signs, a scope that belongs to
// the kind, a token that outlives the mint.
func TestValidateMintRefusesWhatMintTxWouldNotNotice(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	good := func() procbind.MintRequest {
		return procbind.MintRequest{Profile: "default", Now: now, Tokens: []procbind.MintToken{
			{Kind: procbind.KindSession, Scope: procbind.SessionScope("c1", "t1"), Expires: now.Add(time.Hour)},
			{Kind: procbind.KindMission, Scope: procbind.MissionScope(7, 3), Expires: now.Add(time.Hour)}}}
	}
	if err := validateMint(good(), []byte("k"), "default"); err != nil {
		t.Fatalf("a good mint: %v", err)
	}
	for name, c := range map[string]struct {
		mut     func(*procbind.MintRequest)
		key     []byte
		profile string
	}{
		"no key":                             {func(*procbind.MintRequest) {}, nil, "default"},
		"another profile":                    {func(r *procbind.MintRequest) { r.Profile = "other" }, []byte("k"), "default"},
		"a session scope on a mission token": {func(r *procbind.MintRequest) { r.Tokens[1].Scope = procbind.SessionScope("c1", "t1") }, []byte("k"), "default"},
		"a mission scope on a session token": {func(r *procbind.MintRequest) { r.Tokens[0].Scope = procbind.MissionScope(7, 3) }, []byte("k"), "default"},
		"already expired":                    {func(r *procbind.MintRequest) { r.Tokens[0].Expires = now }, []byte("k"), "default"},
	} {
		r := good()
		c.mut(&r)
		if err := validateMint(r, c.key, c.profile); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// After an error from the mint (which cannot tell whether the rows landed) the token is ended by its own id: a row that
// did land is revoked and its registration gone, and another turn's token of the same conversation is left alone.
func TestRevokeByIDEndsOnlyThatToken(t *testing.T) {
	r := newBoundRig(t, "")
	ctx := context.Background()
	key, err := procbind.RowKey(ctx, r.db.DB, "default")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	mint := func(turn string) string {
		id, _ := procbind.NewTokenID()
		_, hash, _ := procbind.NewSecret(procbind.SessionPrefixV2)
		if err := procbind.MintTx(ctx, r.db.DB, key, procbind.MintRequest{Profile: "default", WorkerPID: 4242, WorkerStamp: "s", HostGeneration: "g", Now: now,
			Tokens: []procbind.MintToken{{Kind: procbind.KindSession, ID: id, Hash: hash, Scope: procbind.SessionScope("c1", turn), Expires: now.Add(time.Hour), ConversationID: "c1", TurnID: turn}}}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	landed, other := mint("t1"), mint("t2")
	if err := r.l.bound.revokeByID("default", procbind.KindSession, landed); err != nil {
		t.Fatal(err)
	}
	if rv, _ := procbind.IsRevoked(ctx, r.db.DB, procbind.KindSession, landed); !rv {
		t.Error("the token that may have landed was not revoked")
	}
	if rv, _ := procbind.IsRevoked(ctx, r.db.DB, procbind.KindSession, other); rv {
		t.Error("another turn's token was revoked")
	}
}
