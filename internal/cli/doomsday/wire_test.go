package doomsday

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/doomsday/stop"
	"github.com/monoes/mono-agent/internal/doomsday/store"
	"github.com/monoes/mono-agent/internal/storage"
)

// fakeWorld is the service manager and the cgroup tree these tests cannot have: StartUnit makes a unit that holds
// its anchor process, StopUnit removes it. extra puts a second process into the unit's own cgroup (a live member).
type fakeWorld struct {
	mu      sync.Mutex
	present bool
	extra   bool
	started []string
	onStart func(name string) // called before the unit exists, to observe what was recorded first
	stopped []string
	boot    string
	ids     map[string]uint64
}

func (w *fakeWorld) StartUnit(_ context.Context, name, _ string) error {
	if w.onStart != nil {
		w.onStart(name)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.present = true
	w.started = append(w.started, name)
	return nil
}

func (w *fakeWorld) StopUnit(_ context.Context, name string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.present = false
	w.stopped = append(w.stopped, name)
	return nil
}

// fakeKernel is the cgroup side of fakeWorld. The zero value (no world) is a kernel with no unit at all.
type fakeKernel struct{ w *fakeWorld }

func (k fakeKernel) BootID() (string, error) {
	if k.w != nil && k.w.boot != "" {
		return k.w.boot, nil
	}
	return "boot", nil
}
func (fakeKernel) CurrentUID() uint32             { return uint32(os.Getuid()) }
func (fakeKernel) DelegatedRoot() (string, error) { return "/fake/user@1", nil }
func (fakeKernel) SelfCgroup() (string, error)    { return "/self", nil }

var errFakeAbsent = errors.New("fake: absent")

func (k fakeKernel) OpenCgroup(path string) (stop.Cgroup, error) {
	if k.w == nil {
		return nil, errors.New("fake: no cgroup")
	}
	k.w.mu.Lock()
	defer k.w.mu.Unlock()
	if !k.w.present {
		return nil, errFakeAbsent
	}
	return fakeCgroup{w: k.w, path: path}, nil
}
func (fakeKernel) IsAbsent(err error) bool                   { return errors.Is(err, errFakeAbsent) }
func (fakeKernel) ManagerControlGroup(string) (string, bool) { return "", false }
func (fakeKernel) ListUnitNames(string) ([]string, error)    { return nil, nil }

type fakeCgroup struct {
	w    *fakeWorld
	path string
}

// Identity gives each unit path its own id (7 for the first), as real cgroups have: two units never share one.
func (c fakeCgroup) Identity() (stop.CgroupIdentity, error) {
	c.w.mu.Lock()
	defer c.w.mu.Unlock()
	if c.w.ids == nil {
		c.w.ids = map[string]uint64{}
	}
	id, ok := c.w.ids[c.path]
	if !ok {
		id = 7 + uint64(len(c.w.ids))
		c.w.ids[c.path] = id
	}
	return stop.CgroupIdentity{ID: id, UID: uint32(os.Getuid()), IsCgroup2: true}, nil
}
func (fakeCgroup) Subgroups() ([]string, error) { return nil, nil }
func (c fakeCgroup) Procs(string) ([]int, error) {
	c.w.mu.Lock()
	defer c.w.mu.Unlock()
	if c.w.extra {
		return []int{100, 200}, nil
	}
	return []int{100}, nil
}
func (fakeCgroup) ProcInfo(_ string, pid int) (string, string, error) {
	return fmt.Sprintf("stamp-%d", pid), "proc", nil
}
func (fakeCgroup) Signal(string, int, syscall.Signal) error { return nil }
func (fakeCgroup) Kill(string) error                        { return nil }
func (fakeCgroup) Populated(string) (bool, error)           { return false, nil }
func (fakeCgroup) Rmdir(string) error                       { return nil }
func (fakeCgroup) Close() error                             { return nil }

type wireRig struct {
	*rig
	world   *fakeWorld
	w       Wiring
	host    *coderhost.Store
	charter string
}

type recordingHost struct {
	CoderHost
	revokeErr error
}

func (h recordingHost) RevokeSessionTokensErr(p, c string) error {
	if h.revokeErr != nil {
		return h.revokeErr
	}
	return h.CoderHost.RevokeSessionTokensErr(p, c)
}

func newWire(t *testing.T, probe func() error, kernel stop.Kernel, revokeErr error) (*wireRig, *storage.Database) {
	t.Helper()
	keyring.MockInit()
	dbPath := t.TempDir() + "/dd.db"
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.DB.Close() })
	t.Cleanup(ClosePins)
	host := coderhost.NewStore(db.DB)
	world := &fakeWorld{}
	if _, isFake := kernel.(fakeKernel); isFake {
		kernel = fakeKernel{w: world}
	}
	w := Wiring{
		Store: store.New(db.DB, store.NewHMACKeyer([]byte("k"))), Profile: "p1",
		Witness: &WitnessSetup{DBPath: dbPath, Keys: store.NewHMACKeyer([]byte("k")), Secrets: &stop.FileSecretStore{Dir: t.TempDir()}},
		RunDir:  stop.RunDir{Dir: t.TempDir() + "/run"},
		Probe:   probe, Kernel: kernel, Host: world,
		Binding: func(context.Context) (bool, error) { return true, nil },
		Tokens:  CoderTokens{DB: db.DB, Profile: "p1", Host: recordingHost{CoderHost: host, revokeErr: revokeErr}},
	}
	r := newRig()
	e := w.Engine()
	r.deps.Engine = func() Engine { return e }
	return &wireRig{rig: r, world: world, w: w, host: host, charter: writeCharter(t, goodCharter)}, db
}

func (w *wireRig) start(t *testing.T) string {
	t.Helper()
	h := fullHash(t, w.rig, w.charter)
	out, err := run(t, w.deps, "", "start", "--charter", w.charter, "--yes-i-lift", "--hash", h)
	if err != nil {
		t.Fatal(err, out)
	}
	return out
}

// seedSession gives generation gen a session with a live token and a pending send, as the coder host will once the
// ticket package stamps its sessions.
func seedSession(t *testing.T, w *wireRig, db *storage.Database, gen int64) string {
	t.Helper()
	s, err := w.host.EnsureSession("p1", "conv-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`UPDATE coder_sessions SET doomsday_generation = ? WHERE conversation_id = ?`, gen, s.ConversationID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.host.IssueSessionToken("p1", s.ConversationID, "t1"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.host.Enqueue(coderhost.Command{ProfileID: "p1", ConversationID: s.ConversationID, Kind: coderhost.KindSend, TurnID: "send-1", Message: "go"}); err != nil {
		t.Fatal(err)
	}
	return s.ConversationID
}

func count(t *testing.T, db *storage.Database, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.DB.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A -tags doomsday build can start AND stop a run: against a fake containment in a scratch HOME, the whole path
// (probe, store entry, secret, marker, intent, real token revocation, real queue cancel, quiescent) runs.
func TestWiredEngineStartsAndStopsARunAgainstAFakeContainment(t *testing.T) {
	w, db := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	w.start(t)
	if _, err := os.Stat(w.w.RunDir.SecretPath(1)); err != nil {
		t.Fatalf("the per-run stop secret was not created at start: %v", err)
	}
	conv := seedSession(t, w, db, 1)
	if count(t, db, `SELECT COUNT(*) FROM coder_session_tokens WHERE conversation_id = ?`, conv) == 0 {
		t.Fatal("seed: no token")
	}
	out, err := run(t, w.deps, "", "status")
	if err != nil || !strings.Contains(out, "running") || !strings.Contains(out, "audit: ") {
		t.Fatalf("status shows no state or audit head: %v %q", err, out)
	}

	out, err = run(t, w.deps, "", "stop")
	if err != nil || !strings.HasSuffix(out, "stopped\n") {
		t.Fatalf("stop: %v %q", err, out)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM coder_session_tokens WHERE conversation_id = ?`, conv); n != 0 {
		t.Fatalf("%d token(s) of the run's session survive stop", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM coder_session_commands WHERE conversation_id = ? AND status = 'pending'`, conv); n != 0 {
		t.Fatalf("%d queued command(s) survive stop", n)
	}
	if _, err := os.Stat(w.w.RunDir.StopPath(1)); err != nil {
		t.Fatalf("no stop marker: %v", err)
	}
	runs, err := w.w.Store.OpenRuns(context.Background(), "p1")
	if err != nil || len(runs) != 0 {
		t.Fatalf("the run is still open: %v %v", runs, err)
	}
	if out, _ = run(t, w.deps, "", "status"); !strings.Contains(out, "quiescent") {
		t.Fatalf("status after stop shows no state: %q", out)
	}
	if out, err = run(t, w.deps, "", "stop"); err == nil {
		t.Fatalf("a second stop with no open run claimed success: %q", out)
	}
}

func TestWiredEngineRefusesToStartWhereContainmentIsUnavailable(t *testing.T) {
	w, db := newWire(t, func() error { return stop.ErrContainmentUnavailable }, fakeKernel{}, nil)
	h := strings.Repeat("0", 64)
	if _, err := run(t, w.deps, "", "start", "--charter", w.charter, "--yes-i-lift", "--hash", h); err == nil || !strings.Contains(err.Error(), "refuses") {
		t.Fatalf("started without containment: %v", err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM doomsday_runs`); n != 0 {
		t.Fatalf("%d run row(s) written by a refused start", n)
	}
}

// The word "stopped" needs a proven unit end: with no containment kernel the unit is not proven, so the run stays
// cleanup-pending and the command says NOT stopped.
func TestStopWithoutAProvableUnitNeverSaysStopped(t *testing.T) {
	w, _ := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	w.start(t)
	w.w.Kernel = nil // the unit can no longer be proven
	e := w.w.Engine()
	w.deps.Engine = func() Engine { return e }
	out, err := run(t, w.deps, "", "stop")
	if err == nil || strings.Contains(strings.ReplaceAll(out, "NOT stopped", ""), "stopped\n") || !strings.Contains(out, "NOT stopped") {
		t.Fatalf("claimed stopped with no provable unit: %v %q", err, out)
	}
	runs, _ := w.w.Store.OpenRuns(context.Background(), "p1")
	if len(runs) != 1 || runs[0].State != store.StateCleanupPending {
		t.Fatalf("the run must stay open in cleanup-pending: %+v", runs)
	}
}

// A token revocation that fails leaves authority un-revoked: the run is not quiescent and nothing says stopped.
func TestStopWhenTokenRevocationFailsIsNotStopped(t *testing.T) {
	w, db := newWire(t, func() error { return nil }, fakeKernel{}, errors.New("db locked"))
	w.start(t)
	seedSession(t, w, db, 1)
	out, err := run(t, w.deps, "", "stop")
	if err == nil || strings.HasSuffix(out, "stopped\n") {
		t.Fatalf("claimed stopped with a failed revocation: %v %q", err, out)
	}
	runs, _ := w.w.Store.OpenRuns(context.Background(), "p1")
	if len(runs) != 1 || runs[0].State == store.StateQuiescent || runs[0].State == store.StateAuthorityRevoked {
		t.Fatalf("authority was marked revoked though revocation failed: %+v", runs)
	}
}

// Sessions of another generation or profile keep their tokens: Stop revokes the run's sessions only.
func TestStopLeavesSessionsOutsideTheRunAlone(t *testing.T) {
	w, db := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	w.start(t)
	other, err := w.host.EnsureSession("p1", "outside")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.host.IssueSessionToken("p1", other.ConversationID, "t9"); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, w.deps, "", "stop"); err != nil {
		t.Fatal(err, out)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM coder_session_tokens WHERE conversation_id = 'outside'`); n != 1 {
		t.Fatalf("a session outside the run lost its token (%d)", n)
	}
}
