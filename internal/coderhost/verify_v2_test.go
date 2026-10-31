//go:build linux

package coderhost

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/procbind"
)

// sessRig is a store with a coder session (profile "p") holding one v2 session token bound to this process, and a real
// binding service that registers this process as the token's worker, so a verification from this process is allowed.
type sessRig struct {
	st     *Store
	secret string
	id     string
	conv   string
	turn   string
	scope  string
	sock   string
	reg    *sessRegistry
}

type sessRegistry struct {
	mu sync.Mutex
	m  map[string]bool
}

func (r *sessRegistry) Lookup(tokenID, profile, scope string) (procbind.Handle, bool) {
	r.mu.Lock()
	ok := r.m[tokenID+"|"+profile+"|"+scope]
	r.mu.Unlock()
	if !ok {
		return nil, false
	}
	h, err := procbind.OpenPidfd(os.Getpid())
	if err != nil {
		return nil, false
	}
	return h, true
}

func newSessRig(t *testing.T, mutate func(*procbind.MintToken)) *sessRig {
	t.Helper()
	if !procbind.PeerPidfdSupported() {
		t.Skip("SO_PEERPIDFD needs Linux 6.5; the binding service is unavailable on this kernel")
	}
	procs, err := procbind.NewLinuxProcs()
	if err != nil {
		t.Skipf("not this process's own procfs: %v", err)
	}
	st := newStoreT(t)
	m, _ := master(t, st, "coding")
	r := &sessRig{st: st, conv: m.ConversationID, turn: "t1", reg: &sessRegistry{m: map[string]bool{}}}
	running(t, st, r.conv, r.turn)
	r.scope = sessionScopeV2(r.conv, r.turn)
	ctx := t.Context()
	key, err := procbind.RowKey(ctx, st.db, "p")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	gen := "gen-1"
	if _, err := procbind.InsertHost(ctx, st.db, key, "p", gen, now); err != nil {
		t.Fatal(err)
	}
	secret, hash, err := procbind.NewSecret(procbind.SessionPrefixV2)
	if err != nil {
		t.Fatal(err)
	}
	id, err := procbind.NewTokenID()
	if err != nil {
		t.Fatal(err)
	}
	r.secret, r.id = secret, id
	tok := procbind.MintToken{Kind: procbind.KindSession, ID: id, Hash: hash, Scope: r.scope, Expires: now.Add(time.Hour), ConversationID: r.conv, TurnID: r.turn}
	if mutate != nil {
		mutate(&tok)
	}
	if err := procbind.MintTx(ctx, st.db, key, procbind.MintRequest{Profile: "p", WorkerPID: os.Getpid(), WorkerStamp: "stamp", HostGeneration: gen, Now: now, Tokens: []procbind.MintToken{tok}}); err != nil {
		t.Fatal(err)
	}
	r.reg.m[id+"|p|"+tok.Scope] = true

	ansKey, err := procbind.AnswerKey(ctx, st.db, "p", gen)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "v2s")
	if err != nil {
		t.Fatal(err)
	}
	r.sock = filepath.Join(dir, "s")
	if len(r.sock) > 100 {
		t.Skipf("socket path too long: %s", r.sock)
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: r.sock, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	srv := &procbind.Server{Profile: "p", Generation: gen, Key: ansKey, Registry: r.reg, Procs: procs, UID: os.Getuid(), Timeout: 3 * time.Second}
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = l.Close(); <-done; _ = os.Remove(r.sock); _ = os.Remove(dir) })
	st.SetV2Verifier(&procbind.V2Check{Timeout: 3 * time.Second, Dial: func(string) (net.Conn, error) { return procbind.DialUnix(r.sock) }})
	return r
}

func TestV2SessionTokenIsAcceptedWhenEveryCheckPasses(t *testing.T) {
	r := newSessRig(t, nil)
	c, err := r.st.VerifySessionToken(r.secret)
	if err != nil {
		t.Fatal(err)
	}
	if c != (Caller{ProfileID: "p", ConversationID: r.conv, TurnID: r.turn}) {
		t.Fatalf("caller = %+v", c)
	}
	if c2, err := r.st.CallerFor(r.secret, "p"); err != nil || c2 != c {
		t.Fatalf("CallerFor = %+v, %v", c2, err)
	}
	if _, err := r.st.CallerFor(r.secret, "other"); !errors.Is(err, ErrSessionToken) {
		t.Fatalf("a call naming another profile: %v", err)
	}
}

// Without a verifier set, no v2 secret is accepted whatever the database holds: the path is dark.
func TestV2SessionTokenIsDeniedWhileTheVerifierIsNotSet(t *testing.T) {
	r := newSessRig(t, nil)
	r.st.SetV2Verifier(nil)
	if _, err := r.st.VerifySessionToken(r.secret); !errors.Is(err, ErrSessionToken) {
		t.Fatalf("err = %v, want ErrSessionToken", err)
	}
}

func TestV2SessionTokenIsDeniedWhenAnyCheckFails(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate  func(*procbind.MintToken)
		prepare func(t *testing.T, r *sessRig)
		present func(r *sessRig) string
	}{
		"unknown secret": {present: func(r *sessRig) string {
			s, _, _ := procbind.NewSecret(procbind.SessionPrefixV2)
			return s
		}},
		"expired":                                {mutate: func(m *procbind.MintToken) { m.Expires = time.Now().Add(-time.Minute) }},
		"the binding's scope names another turn": {mutate: func(m *procbind.MintToken) { m.Scope = sessionScopeV2(m.ConversationID, "t-other") }},
		"the session has moved to another turn": {prepare: func(t *testing.T, r *sessRig) {
			running(t, r.st, r.conv, "t2")
		}},
		"the service does not know the token": {prepare: func(t *testing.T, r *sessRig) {
			r.reg.mu.Lock()
			delete(r.reg.m, r.id+"|p|"+r.scope)
			r.reg.mu.Unlock()
		}},
		"a mission token's secret": {present: func(r *sessRig) string {
			s, _, _ := procbind.NewSecret(procbind.TaskPrefixV2)
			return s
		}},
	} {
		t.Run(name, func(t *testing.T) {
			r := newSessRig(t, tc.mutate)
			if tc.prepare != nil {
				tc.prepare(t, r)
			}
			secret := r.secret
			if tc.present != nil {
				secret = tc.present(r)
			}
			if _, err := r.st.VerifySessionToken(secret); !errors.Is(err, ErrSessionToken) {
				t.Fatalf("err = %v, want ErrSessionToken", err)
			}
		})
	}
}

// A binding service that is not there is a deny with its own error, never a pass and never the legacy path.
func TestV2SessionTokenIsDeniedWhenTheServiceDoesNotAnswer(t *testing.T) {
	r := newSessRig(t, nil)
	r.st.SetV2Verifier(&procbind.V2Check{Timeout: time.Second, Dial: func(string) (net.Conn, error) { return nil, errors.New("no socket") }})
	_, err := r.st.VerifySessionToken(r.secret)
	if !errors.Is(err, ErrSessionServiceDown) || !errors.Is(err, ErrSessionToken) {
		t.Fatalf("err = %v, want ErrSessionServiceDown (and ErrSessionToken)", err)
	}
}

// A legacy session token verifies as before with the v2 verifier set, and the v2 secret is not a legacy one.
func TestLegacySessionTokenIsUnchangedByTheV2Verifier(t *testing.T) {
	r := newSessRig(t, nil)
	m, _ := master(t, r.st, "coding")
	running(t, r.st, m.ConversationID, "t1")
	tok := mint(t, r.st, m.ConversationID, "t1")
	if _, err := r.st.VerifySessionToken(tok); err != nil {
		t.Fatalf("legacy verify: %v", err)
	}
}

// The legacy session verifier is frozen against the v2 family: a v2 secret does not start with the legacy prefix, and
// with no v2 verifier set the store denies it without looking at any table.
func TestAV2SessionSecretIsNotALegacySessionSecret(t *testing.T) {
	secret, _, err := procbind.NewSecret(procbind.SessionPrefixV2)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(secret, SessionTokenPrefix) || strings.HasPrefix(procbind.SessionPrefixV2, SessionTokenPrefix) {
		t.Fatalf("a v2 secret %q starts with the legacy prefix %q", secret[:6], SessionTokenPrefix)
	}
	st := newStoreT(t)
	if _, err := st.VerifySessionToken(secret); !errors.Is(err, ErrSessionToken) {
		t.Fatalf("err = %v", err)
	}
}
