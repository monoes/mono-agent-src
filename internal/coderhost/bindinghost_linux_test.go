//go:build linux

package coderhost

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/procbind"
)

func bindingRig(t *testing.T) (*BindingHost, string, *Store) {
	t.Helper()
	if !procbind.PeerPidfdSupported() {
		t.Skip("SO_PEERPIDFD needs Linux 6.5; the binding service is unavailable on this kernel")
	}
	st := newStoreT(t)
	root, err := os.MkdirTemp("", "bh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	h := &BindingHost{DB: st.db, Binder: procbind.NewBinder(time.Now), Root: root}
	t.Cleanup(h.Shutdown)
	return h, root, st
}

// hostRow is the newest host row of the profile, read with its row key.
func hostRow(t *testing.T, st *Store, profile string) procbind.HostRow {
	t.Helper()
	ctx := context.Background()
	key, err := procbind.RowKey(ctx, st.db, profile)
	if err != nil {
		t.Fatal(err)
	}
	row, err := procbind.CurrentHost(ctx, st.db, key, profile)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

// register puts the test process itself in the Binder as the worker of a token and returns what a verifier needs.
func register(t *testing.T, h *BindingHost, profile string) (token, scope string) {
	t.Helper()
	procs, err := procbind.NewLinuxProcs()
	if err != nil {
		t.Fatal(err)
	}
	self, err := procs.Open(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	token, scope = "tok-"+profile, "session:7"
	gen, err := h.Binder.Register(procbind.EntrySpec{TokenID: token, Kind: procbind.KindSession, Profile: profile, Scope: scope, Session: "conv", Turn: "turn", Expires: time.Now().Add(time.Hour)}, self)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Binder.Activate(profile, token, gen); err != nil {
		t.Fatal(err)
	}
	return token, scope
}

// verify asks the profile's service the way a verifier does: it dials the checked socket and checks the answer under
// the answer key of the generation the host row names.
func verify(t *testing.T, st *Store, root, profile, generation, token, scope string) (procbind.Verdict, error) {
	t.Helper()
	ctx := context.Background()
	key, err := procbind.AnswerKey(ctx, st.db, profile, generation)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := procbind.DialSecure(procbind.SocketOptions{Root: root, Profile: profile})
	if err != nil {
		return "", err
	}
	defer conn.Close()
	return procbind.Client{Key: key, Profile: profile, Generation: generation, Timeout: 3 * time.Second}.Verify(conn, token, scope)
}

func TestTheBindingServiceStartsRecordsItsStartAndAnswers(t *testing.T) {
	h, root, st := bindingRig(t)
	ctx := context.Background()
	if h.Running("p") {
		t.Fatal("running before it was started")
	}
	if err := h.Ensure(ctx, "p"); err != nil {
		t.Fatal(err)
	}
	if !h.Running("p") {
		t.Fatal("not running after Ensure")
	}
	row := hostRow(t, st, "p")
	if row.Seq != 1 || row.Generation == "" {
		t.Fatalf("host row %+v", row)
	}
	token, scope := register(t, h, "p")
	if v, err := verify(t, st, root, "p", row.Generation, token, scope); err != nil || v != procbind.VerdictAllow {
		t.Fatalf("the worker's own process: %v %v", v, err)
	}
	// an unregistered token is denied
	if v, err := verify(t, st, root, "p", row.Generation, "nobody", scope); err != nil || v != procbind.VerdictDeny {
		t.Fatalf("an unknown token: %v %v", v, err)
	}
	// a second Ensure changes nothing: no new host row, the same generation
	if err := h.Ensure(ctx, "p"); err != nil {
		t.Fatal(err)
	}
	if again := hostRow(t, st, "p"); again.Seq != 1 || again.Generation != row.Generation {
		t.Errorf("a second Ensure restarted the service: %+v", again)
	}
}

// Shutdown revokes every registration and closes the socket; a restart has a new generation, so an answer made
// under the old one no longer verifies.
func TestShutdownRevokesEverythingAndARestartHasANewGeneration(t *testing.T) {
	h, root, st := bindingRig(t)
	ctx := context.Background()
	if err := h.Ensure(ctx, "p"); err != nil {
		t.Fatal(err)
	}
	old := hostRow(t, st, "p")
	token, scope := register(t, h, "p")
	h.Shutdown()
	if h.Running("p") {
		t.Error("running after Shutdown")
	}
	if lease, ok := h.Binder.Acquire(token, "p", scope); ok {
		lease.Release()
		t.Error("a registration survived Shutdown")
	}
	if _, err := verify(t, st, root, "p", old.Generation, token, scope); err == nil {
		t.Error("the socket still answered after Shutdown")
	}
	if err := h.Ensure(ctx, "p"); err == nil {
		t.Error("Ensure worked after Shutdown")
	}

	// a new host over the same database and root
	h2 := &BindingHost{DB: st.db, Binder: procbind.NewBinder(time.Now), Root: root}
	t.Cleanup(h2.Shutdown)
	if err := h2.Ensure(ctx, "p"); err != nil {
		t.Fatal(err)
	}
	now := hostRow(t, st, "p")
	if now.Seq != 2 || now.Generation == old.Generation {
		t.Fatalf("restart: %+v then %+v", old, now)
	}
	token2, scope2 := register(t, h2, "p")
	if v, err := verify(t, st, root, "p", now.Generation, token2, scope2); err != nil || v != procbind.VerdictAllow {
		t.Fatalf("the new generation: %v %v", v, err)
	}
	if v, err := verify(t, st, root, "p", old.Generation, token2, scope2); err == nil && v == procbind.VerdictAllow {
		t.Error("an answer of the old generation verified")
	}
}

// A second host on the same socket directory is refused, and writes no host row.
func TestASecondBindingHostIsRefusedAndWritesNothing(t *testing.T) {
	h, root, st := bindingRig(t)
	ctx := context.Background()
	if err := h.Ensure(ctx, "p"); err != nil {
		t.Fatal(err)
	}
	h2 := &BindingHost{DB: st.db, Binder: procbind.NewBinder(time.Now), Root: root}
	t.Cleanup(h2.Shutdown)
	err := h2.Ensure(ctx, "p")
	if err == nil || !errors.Is(err, procbind.ErrAnotherHost) {
		t.Fatalf("a second host was not refused: %v", err)
	}
	if h2.Running("p") {
		t.Error("the refused host reports running")
	}
	if row := hostRow(t, st, "p"); row.Seq != 1 {
		t.Errorf("the refused start wrote a host row: %+v", row)
	}
}

// Two profiles get two services: each with its own socket directory, row and generation.
func TestTwoProfilesGetTwoBindingServices(t *testing.T) {
	h, root, st := bindingRig(t)
	ctx := context.Background()
	for _, p := range []string{"a", "b"} {
		if err := h.Ensure(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	ra, rb := hostRow(t, st, "a"), hostRow(t, st, "b")
	if ra.Generation == rb.Generation {
		t.Error("two profiles share a generation")
	}
	ta, sa := register(t, h, "a")
	if v, err := verify(t, st, root, "a", ra.Generation, ta, sa); err != nil || v != procbind.VerdictAllow {
		t.Fatalf("profile a: %v %v", v, err)
	}
	// profile a's token asked of profile b's service is not allowed
	if v, err := verify(t, st, root, "b", rb.Generation, ta, sa); err == nil && v == procbind.VerdictAllow {
		t.Error("profile b's service allowed profile a's token")
	}
}

// A host without a registry is refused before anything is started (a nil pointer must not reach the service's lease
// interface as a non-nil interface).
func TestEnsureRefusesAHostWithoutARegistry(t *testing.T) {
	h, _, _ := bindingRig(t)
	h.Binder = nil
	if err := h.Ensure(context.Background(), "p"); err == nil || h.Running("p") {
		t.Fatalf("Ensure without a Binder: %v (running=%v)", err, h.Running("p"))
	}
}
