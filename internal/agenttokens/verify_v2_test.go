//go:build linux

package agenttokens

import (
	"context"
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

// v2Rig is a store holding one v2 mission token bound to this process, with a real binding service that registers
// this process as the token's worker, so a verification from this process is allowed.
type v2Rig struct {
	s       *Store
	secret  string
	id      string
	profile string
	scope   string
	sock    string
	reg     *rigRegistry
}

type rigRegistry struct {
	mu sync.Mutex
	m  map[string]bool
}

func (r *rigRegistry) Lookup(tokenID, profile, scope string) (procbind.Handle, bool) {
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

func newV2Rig(t *testing.T, mutate func(*procbind.MintToken)) *v2Rig {
	t.Helper()
	if !procbind.PeerPidfdSupported() {
		t.Skip("SO_PEERPIDFD needs Linux 6.5; the binding service is unavailable on this kernel")
	}
	procs, err := procbind.NewLinuxProcs()
	if err != nil {
		t.Skipf("not this process's own procfs: %v", err)
	}
	ctx := context.Background()
	s := newStore(t)
	r := &v2Rig{s: s, profile: "default", scope: "mission:7:3", reg: &rigRegistry{m: map[string]bool{}}}
	key, err := procbind.RowKey(ctx, s.db, r.profile)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	gen := "gen-1"
	if _, err := procbind.InsertHost(ctx, s.db, key, r.profile, gen, now); err != nil {
		t.Fatal(err)
	}
	secret, hash, err := procbind.NewSecret(procbind.TaskPrefixV2)
	if err != nil {
		t.Fatal(err)
	}
	id, err := procbind.NewTokenID()
	if err != nil {
		t.Fatal(err)
	}
	r.secret, r.id = secret, id
	tok := procbind.MintToken{Kind: procbind.KindMission, ID: id, Hash: hash, Scope: r.scope, Expires: now.Add(time.Hour),
		Owner: "run-a", Role: RoleWorker, Actor: "w7-t3", Session: 7, Task: 3}
	if mutate != nil {
		mutate(&tok)
	}
	if err := procbind.MintTx(ctx, s.db, key, procbind.MintRequest{Profile: r.profile, WorkerPID: os.Getpid(), WorkerStamp: "stamp", HostGeneration: gen, Now: now, Tokens: []procbind.MintToken{tok}}); err != nil {
		t.Fatal(err)
	}
	r.reg.m[id+"|"+r.profile+"|"+r.scope] = true

	ansKey, err := procbind.AnswerKey(ctx, s.db, r.profile, gen)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "v2v")
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
	srv := &procbind.Server{Profile: r.profile, Generation: gen, Key: ansKey, Registry: r.reg, Procs: procs, UID: os.Getuid(), Timeout: 3 * time.Second}
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = l.Close(); <-done; _ = os.Remove(r.sock); _ = os.Remove(dir) })
	r.enable()
	return r
}

func (r *v2Rig) enable() {
	r.s.SetV2Verifier(&procbind.V2Check{Timeout: 3 * time.Second, Dial: func(string) (net.Conn, error) { return procbind.DialUnix(r.sock) }})
}

func (r *v2Rig) verify() (Token, error) {
	return r.s.VerifyMission(context.Background(), r.profile, r.secret)
}

func TestV2MissionTokenIsAcceptedWhenEveryCheckPasses(t *testing.T) {
	r := newV2Rig(t, nil)
	tok, err := r.verify()
	if err != nil {
		t.Fatal(err)
	}
	want := MissionBinding{Session: 7, Task: 3, Role: RoleWorker, Actor: "w7-t3", Owner: "run-a"}
	if tok.ID != r.id || tok.Mission == nil || *tok.Mission != want || tok.ProfileID != r.profile {
		t.Fatalf("token = %+v", tok)
	}
}

// Without a verifier set, no v2 secret is accepted whatever the database holds: the path is dark.
func TestV2MissionTokenIsDeniedWhileTheVerifierIsNotSet(t *testing.T) {
	r := newV2Rig(t, nil)
	r.s.SetV2Verifier(nil)
	if _, err := r.verify(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

func TestV2MissionTokenIsDeniedWhenAnyCheckFails(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		mutate  func(*procbind.MintToken)
		prepare func(t *testing.T, r *v2Rig)
		present func(r *v2Rig) (profile, secret string)
	}{
		"unknown secret": {present: func(r *v2Rig) (string, string) {
			s, _, _ := procbind.NewSecret(procbind.TaskPrefixV2)
			return r.profile, s
		}},
		"another profile": {present: func(r *v2Rig) (string, string) { return "other", r.secret }},
		"expired":         {mutate: func(m *procbind.MintToken) { m.Expires = time.Now().Add(-time.Minute) }},
		"revoked": {prepare: func(t *testing.T, r *v2Rig) {
			key, err := procbind.RowKeyIfExists(ctx, r.s.db, r.profile)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := procbind.RevokeTx(ctx, r.s.db, func(string) ([]byte, error) { return key, nil },
				procbind.Selector{Kind: procbind.KindMission, Profile: r.profile, TokenID: r.id}, "revoked", time.Now()); err != nil {
				t.Fatal(err)
			}
		}},
		"the service does not know the token": {prepare: func(t *testing.T, r *v2Rig) {
			r.reg.mu.Lock()
			delete(r.reg.m, r.id+"|"+r.profile+"|"+r.scope)
			r.reg.mu.Unlock()
		}},
	} {
		t.Run(name, func(t *testing.T) {
			r := newV2Rig(t, tc.mutate)
			if tc.prepare != nil {
				tc.prepare(t, r)
			}
			profile, secret := r.profile, r.secret
			if tc.present != nil {
				profile, secret = tc.present(r)
			}
			if _, err := r.s.VerifyMission(ctx, profile, secret); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
}

// A binding service that is not there is a deny with its own error, never a pass and never the legacy path.
func TestV2MissionTokenIsDeniedWhenTheServiceDoesNotAnswer(t *testing.T) {
	r := newV2Rig(t, nil)
	r.s.SetV2Verifier(&procbind.V2Check{Timeout: time.Second, Dial: func(string) (net.Conn, error) { return nil, errors.New("no socket") }})
	_, err := r.verify()
	if !errors.Is(err, ErrBindingServiceDown) || !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrBindingServiceDown (and ErrInvalid)", err)
	}
}

// With no host row the service never started: deny, whatever else is in place.
func TestV2MissionTokenIsDeniedWithoutAHostRow(t *testing.T) {
	r := newV2Rig(t, nil)
	// A second profile holds a token but never had a host start.
	ctx := context.Background()
	key, err := procbind.RowKey(ctx, r.s.db, "nohost")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	secret, hash, _ := procbind.NewSecret(procbind.TaskPrefixV2)
	id, _ := procbind.NewTokenID()
	if err := procbind.MintTx(ctx, r.s.db, key, procbind.MintRequest{Profile: "nohost", WorkerPID: os.Getpid(), WorkerStamp: "s", HostGeneration: "g", Now: now,
		Tokens: []procbind.MintToken{{Kind: procbind.KindMission, ID: id, Hash: hash, Scope: "mission:1:1", Expires: now.Add(time.Hour), Owner: "o", Role: RoleWorker, Actor: "a", Session: 1, Task: 1}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.VerifyMission(ctx, "nohost", secret); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

// A v2-prefixed secret never reaches the legacy table: a legacy row whose hash equals the secret's hash is not found.
func TestV2PrefixedSecretNeverFallsBackToTheLegacyTable(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	tok, _, err := s.CreateMission(ctx, "default", missionB, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	secret, _, _ := procbind.NewSecret(procbind.TaskPrefixV2)
	if _, err := s.db.Exec(`UPDATE agent_tokens SET token_hash = ? WHERE id = ?`, Hash(secret), tok.ID); err != nil {
		t.Fatal(err)
	}
	for _, withVerifier := range []bool{false, true} {
		if withVerifier {
			s.SetV2Verifier(&procbind.V2Check{Dial: func(string) (net.Conn, error) { return nil, errors.New("no socket") }})
		}
		if _, err := s.VerifyMission(ctx, "default", secret); !errors.Is(err, ErrInvalid) {
			t.Fatalf("verifier=%v: err = %v, want ErrInvalid", withVerifier, err)
		}
	}
}

// The legacy path is untouched: a legacy mission token verifies as before with the v2 verifier set, and the legacy
// lookup itself does not know a v2 secret.
func TestLegacyMissionTokenIsUnchangedByTheV2Verifier(t *testing.T) {
	r := newV2Rig(t, nil)
	ctx := context.Background()
	tok, secret, err := r.s.CreateMission(ctx, "default", missionB, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.s.VerifyMission(ctx, "default", secret)
	if err != nil || got.ID != tok.ID {
		t.Fatalf("legacy verify = %+v, %v", got, err)
	}
	if _, err := r.s.verify(ctx, r.secret); !errors.Is(err, ErrInvalid) {
		t.Fatalf("the legacy lookup accepted a v2 secret: %v", err)
	}
}

// craft puts one more v2 mission token in the rig's store with its own token and binding expiry (or no binding at
// all), registered with the service as this process's, so that only the check under test can refuse it.
func (r *v2Rig) craft(t *testing.T, tokenExpires, bindingExpires time.Time, withBinding bool) string {
	t.Helper()
	ctx := context.Background()
	key, err := procbind.RowKey(ctx, r.s.db, r.profile)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	secret, hash, _ := procbind.NewSecret(procbind.TaskPrefixV2)
	id, _ := procbind.NewTokenID()
	if err := procbind.InsertMissionToken(ctx, r.s.db, key, procbind.MissionTokenRow{ID: id, ProfileID: r.profile, TokenHash: hash, Owner: "run-a", Session: 7, Task: 3,
		Role: RoleWorker, Actor: "w7-t3", CreatedAt: now.Add(-time.Hour), ExpiresAt: tokenExpires}); err != nil {
		t.Fatal(err)
	}
	if withBinding {
		if err := procbind.InsertBinding(ctx, r.s.db, key, procbind.BindingRow{Kind: procbind.KindMission, TokenID: id, ProfileID: r.profile, Scope: r.scope,
			WorkerPID: os.Getpid(), WorkerStamp: "stamp", HostGeneration: "gen-1", CreatedAt: now.Add(-time.Hour), ExpiresAt: bindingExpires}); err != nil {
			t.Fatal(err)
		}
	}
	r.reg.mu.Lock()
	r.reg.m[id+"|"+r.profile+"|"+r.scope] = true
	r.reg.mu.Unlock()
	return secret
}

// Each check refuses on its own: the service would allow all of these, so only the row checks stand between them and
// acceptance.
func TestV2MissionTokenEachRowCheckRefusesOnItsOwn(t *testing.T) {
	r := newV2Rig(t, nil)
	future, past := time.Now().UTC().Add(time.Hour).Truncate(time.Millisecond), time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	for name, secret := range map[string]string{
		"the token has expired":   r.craft(t, past, future, true),
		"the binding has expired": r.craft(t, future, past, true),
		"there is no binding row": r.craft(t, future, future, false),
	} {
		if _, err := r.s.VerifyMission(context.Background(), r.profile, secret); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if ok := r.craft(t, future, future, true); ok == "" {
		t.Fatal("no secret")
	} else if _, err := r.s.VerifyMission(context.Background(), r.profile, ok); err != nil {
		t.Fatalf("the same craft with every row valid is refused: %v", err)
	}
}

// The legacy verifier is frozen against the v2 family: a valid legacy mission row whose stored hash is the hash of a v2
// secret is not found by a v2 secret, and the control (a genuine legacy token of the same kind) verifies, so the row is
// genuinely valid. Removing the v2 dispatch makes the v2 secret verify through the legacy row.
func TestAV2MissionSecretWithAMatchingValidLegacyRowIsRefusedByTheLegacyVerifier(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	tok, control, err := s.CreateMission(ctx, "default", missionB, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyMission(ctx, "default", control); err != nil {
		t.Fatalf("the control legacy token: %v", err)
	}
	secret, _, err := procbind.NewSecret(procbind.TaskPrefixV2)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(secret, Prefix) {
		t.Fatalf("a v2 secret starts with the legacy prefix %q", Prefix)
	}
	if _, err := s.db.Exec(`UPDATE agent_tokens SET token_hash = ? WHERE id = ?`, Hash(secret), tok.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyMission(ctx, "default", secret); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a v2 secret verified through a matching legacy row: %v", err)
	}
}
