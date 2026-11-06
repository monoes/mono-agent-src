package stop

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// stepClock: After advances time by the requested duration and fires at once, so bounded waits are
// deterministic and never sleep.
type stepClock struct {
	mu  sync.Mutex
	now time.Time
}

func newStepClock() *stepClock { return &stepClock{now: time.Unix(1_800_000_000, 0)} }

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *stepClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	c.now = c.now.Add(d)
	n := c.now
	c.mu.Unlock()
	ch := make(chan time.Time, 1)
	ch <- n
	return ch
}

// faultFS wraps the real FS and fails on demand (a failed fsync, a full disk, an unreadable file).
type faultFS struct {
	OSFS
	mu          sync.Mutex
	failCreate  map[string]bool // by file name suffix
	failRead    map[string]bool
	failFileSyn bool
	failDirSync bool
	failRemove  bool
	failAppend  bool
}

func newFaultFS() *faultFS {
	return &faultFS{failCreate: map[string]bool{}, failRead: map[string]bool{}}
}

func (f *faultFS) match(m map[string]bool, path string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for suf := range m {
		if strings.HasSuffix(path, suf) {
			return true
		}
	}
	return false
}

type faultFile struct {
	File
	fail bool
}

func (f faultFile) Sync() error {
	if f.fail {
		return errInjected
	}
	return f.File.Sync()
}

func (f *faultFS) CreateExclusive(path string, perm os.FileMode) (File, error) {
	if f.match(f.failCreate, path) {
		return nil, errInjected
	}
	fl, err := f.OSFS.CreateExclusive(path, perm)
	if err != nil {
		return nil, err
	}
	return faultFile{File: fl, fail: f.failFileSyn}, nil
}

func (f *faultFS) ReadFile(path string) ([]byte, error) {
	if f.match(f.failRead, path) {
		return nil, errInjected
	}
	return f.OSFS.ReadFile(path)
}

func (f *faultFS) SyncDir(dir string) error {
	if f.failDirSync {
		return errInjected
	}
	return f.OSFS.SyncDir(dir)
}

func (f *faultFS) Remove(path string) error {
	if f.failRemove {
		return errInjected
	}
	return f.OSFS.Remove(path)
}

func (f *faultFS) AppendSync(path string, data []byte, dir string) error {
	if f.failAppend {
		return errInjected
	}
	return f.OSFS.AppendSync(path, data, dir)
}

// fakeRunStore is an in-memory RunStore that logs every call in order.
type fakeRunStore struct {
	mu        sync.Mutex
	log       []string
	state     RunState
	members   []string
	failTo    map[RunState]bool
	failRev   bool
	runs      []RunInfo
	advCalls  int
	onAdvance func(to RunState)
}

func newFakeRunStore() *fakeRunStore {
	return &fakeRunStore{state: StateRunning, failTo: map[RunState]bool{}}
}

func (s *fakeRunStore) record(what string) {
	s.mu.Lock()
	s.log = append(s.log, what)
	s.mu.Unlock()
}

func (s *fakeRunStore) State(context.Context, uint64) (RunState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state, nil
}

func (s *fakeRunStore) Advance(_ context.Context, _ uint64, to RunState, _ string) error {
	s.mu.Lock()
	s.advCalls++
	cb, fail := s.onAdvance, s.failTo[to]
	s.mu.Unlock()
	if cb != nil {
		cb(to)
	}
	if fail {
		return errInjected
	}
	if to == StateQuiescent && len(s.members) > 0 {
		return errors.New("live members")
	}
	s.record("advance:" + string(to))
	s.mu.Lock()
	s.state = to
	s.mu.Unlock()
	return nil
}

func (s *fakeRunStore) RevokeTokens(context.Context, uint64) error {
	if s.failRev {
		return errInjected
	}
	s.record("revoke")
	return nil
}
func (s *fakeRunStore) CancelQueue(context.Context, uint64) error { s.record("cancel"); return nil }
func (s *fakeRunStore) LiveMembers(context.Context, uint64) ([]string, error) {
	return s.members, nil
}
func (s *fakeRunStore) OpenRuns(context.Context) ([]RunInfo, error) { return s.runs, nil }

// fakeEnder records the end call.
type fakeEnder struct {
	store   *fakeRunStore
	out     UnitOutcome
	calls   int
	exclude []uint64
}

func (e *fakeEnder) End(_ context.Context, _ uint64, exclude []uint64) UnitOutcome {
	e.calls++
	e.exclude = exclude
	if e.store != nil {
		e.store.record("end")
	}
	if e.out.Kind == 0 {
		return UnitOutcome{Kind: UnitEnded}
	}
	return e.out
}

func newRunDir(t *testing.T, fsys FS) RunDir {
	t.Helper()
	return RunDir{Dir: shortTempDir(t), FS: fsys}
}

// ----- containment fakes -----

type fakeProc struct {
	pid       int
	stamp     string
	dieOnTerm bool
	dieOnKill bool
	known     bool
}

type fakeCg struct {
	mu        sync.Mutex
	id        CgroupIdentity
	subs      map[string][]*fakeProc
	signals   []string
	kills     []string
	rmdirBusy map[string]int // times Rmdir fails with EBUSY before succeeding
	removed   map[string]bool
}

func newFakeCg() *fakeCg {
	return &fakeCg{id: CgroupIdentity{ID: 42, UID: 1000, IsCgroup2: true}, subs: map[string][]*fakeProc{},
		rmdirBusy: map[string]int{}, removed: map[string]bool{}}
}

func (c *fakeCg) Identity() (CgroupIdentity, error) { return c.id, nil }
func (c *fakeCg) Close() error                      { return nil }
func (c *fakeCg) Subgroups() ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for s := range c.subs {
		if !c.removed[s] {
			out = append(out, s)
		}
	}
	return out, nil
}
func (c *fakeCg) Procs(sub string) ([]int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []int
	for _, p := range c.subs[sub] {
		out = append(out, p.pid)
	}
	return out, nil
}
func (c *fakeCg) ProcInfo(sub string, pid int) (string, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range c.subs[sub] {
		if p.pid == pid {
			return p.stamp, "tool", nil
		}
	}
	return "", "", errors.New("gone")
}
func (c *fakeCg) Signal(sub string, pid int, sig syscall.Signal) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.signals = append(c.signals, sub)
	if sig == syscall.SIGTERM {
		var keep []*fakeProc
		for _, p := range c.subs[sub] {
			if !(p.pid == pid && p.dieOnTerm) {
				keep = append(keep, p)
			}
		}
		c.subs[sub] = keep
	}
	return nil
}
func (c *fakeCg) Kill(sub string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.kills = append(c.kills, sub)
	var keep []*fakeProc
	for _, p := range c.subs[sub] {
		if !p.dieOnKill {
			keep = append(keep, p)
		}
	}
	c.subs[sub] = keep
	return nil
}
func (c *fakeCg) Populated(sub string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.subs[sub]) > 0, nil
}
func (c *fakeCg) Rmdir(sub string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.subs[sub]) > 0 {
		return syscall.EBUSY
	}
	if c.rmdirBusy[sub] > 0 {
		c.rmdirBusy[sub]--
		return syscall.EBUSY
	}
	c.removed[sub] = true
	return nil
}

type fakeKernel struct {
	boot    string
	uid     uint32
	root    string
	self    string
	cg      *fakeCg
	absent  bool
	mgr     string
	mgrAnsw bool
	names   []string
	opened  int
}

var errAbsent = errors.New("absent")

func (k *fakeKernel) BootID() (string, error)                   { return k.boot, nil }
func (k *fakeKernel) CurrentUID() uint32                        { return k.uid }
func (k *fakeKernel) DelegatedRoot() (string, error)            { return k.root, nil }
func (k *fakeKernel) SelfCgroup() (string, error)               { return k.self, nil }
func (k *fakeKernel) IsAbsent(err error) bool                   { return errors.Is(err, errAbsent) }
func (k *fakeKernel) ListUnitNames(string) ([]string, error)    { return k.names, nil }
func (k *fakeKernel) ManagerControlGroup(string) (string, bool) { return k.mgr, k.mgrAnsw }
func (k *fakeKernel) OpenCgroup(string) (Cgroup, error) {
	k.opened++
	if k.absent {
		return nil, errAbsent
	}
	return k.cg, nil
}

const testNonce = "0123456789abcdef0123456789abcdef"

func testRecord() UnitRecord {
	name := "monoagent-run-7-" + testNonce + ".service"
	return UnitRecord{Kind: "cgroup2", BootID: "boot-1", Name: name, Nonce: testNonce, Generation: 7,
		CgroupPath: "/sys/fs/cgroup/user.slice/user@1000.service/" + name, CgroupID: 42, OwnerUID: 1000}
}

func newFakeKernel() *fakeKernel {
	return &fakeKernel{boot: "boot-1", uid: 1000, root: "/sys/fs/cgroup/user.slice/user@1000.service",
		self: "/sys/fs/cgroup/user.slice/user@1000.service/app.slice/host.scope", cg: newFakeCg(), mgrAnsw: false}
}

func mustNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// shortTempDir is a private directory with a SHORT path, for tests that bind a unix socket (macOS limits a socket path to
// 104 bytes and t.TempDir() under $TMPDIR is longer).
func shortTempDir(t *testing.T) string {
	t.Helper()
	base := os.TempDir() // never a fixed directory: on Windows C:\tmp may exist outside the test's scratch area
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat("/tmp"); err == nil && fi.IsDir() {
			base = "/tmp"
		}
	}
	d, err := os.MkdirTemp(base, "ma")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		var rerr error
		for i := 0; i < 30; i++ { // Windows refuses to remove a file something still has open: retry briefly
			if rerr = os.RemoveAll(d); rerr == nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Logf("scratch directory left behind: %v", rerr)
	})
	return d
}
