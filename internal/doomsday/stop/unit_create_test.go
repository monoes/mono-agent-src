package stop

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// fakeHost is the service manager: StartUnit builds the new cgroup in the fake kernel (one anchor process in the
// unit's own cgroup, as a real service has), StopUnit removes it.
type fakeHost struct {
	mu       sync.Mutex
	k        *fakeKernel
	started  []string
	stopped  []string
	startErr error
	stopErr  error
	anchors  []*fakeProc // what the new unit holds in its own cgroup
}

func newFakeHost(k *fakeKernel) *fakeHost {
	return &fakeHost{k: k, anchors: []*fakeProc{{pid: 500, stamp: "st-500"}}}
}

func (h *fakeHost) StartUnit(_ context.Context, name, _ string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.started = append(h.started, name)
	if h.startErr != nil {
		return h.startErr
	}
	h.k.cg.subs[""] = h.anchors
	return nil
}

func (h *fakeHost) StopUnit(_ context.Context, name string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stopped = append(h.stopped, name)
	if h.stopErr != nil {
		return h.stopErr
	}
	h.k.absent = true
	return nil
}

func newCreator(t *testing.T) (UnitCreator, *fakeHost, *fakeKernel) {
	t.Helper()
	k := newFakeKernel()
	h := newFakeHost(k)
	return UnitCreator{K: k, Dir: newRunDir(t, nil), Host: h, Rand: bytes.NewReader(bytes.Repeat([]byte{0xab}, 64))}, h, k
}

func TestCreateRecordsAProvableUnitBeforeReturning(t *testing.T) {
	c, h, k := newCreator(t)
	rec, err := c.Create(bg, 7)
	if err != nil {
		t.Fatal(err)
	}
	gen, nonce, ok := UnitGeneration(rec.Name)
	if !ok || gen != 7 || nonce != rec.Nonce || len(h.started) != 1 || h.started[0] != rec.Name {
		t.Fatalf("name %q nonce %q started %v", rec.Name, rec.Nonce, h.started)
	}
	if rec.CgroupPath != k.root+"/"+rec.Name || rec.AnchorPID != 500 || rec.AnchorStamp != "st-500" || rec.CgroupID != 42 {
		t.Fatalf("%+v", rec)
	}
	got, found, err := c.Dir.ReadUnitFile(7)
	if err != nil || !found || got != rec {
		t.Fatalf("the unit record is not durable before Create returns: %+v found=%v err=%v", got, found, err)
	}
	// the record is the proof's input: it proves against the kernel it was made on
	p, err := ProveUnit(k, rec)
	if err != nil || p.Gone {
		t.Fatalf("the created record does not prove: %v", err)
	}
	p.Cg.Close()
	if len(h.stopped) != 0 {
		t.Fatalf("a recorded unit was stopped: %v", h.stopped)
	}
}

// No unit, no run: every failure leaves no record and, when the service started, stops it again.
func TestCreateFailureLeavesNoRecordAndStopsTheService(t *testing.T) {
	cases := map[string]func(c *UnitCreator, h *fakeHost, k *fakeKernel){
		"the manager refuses": func(c *UnitCreator, h *fakeHost, k *fakeKernel) { h.startErr = errors.New("no delegation") },
		"two processes in the new unit": func(c *UnitCreator, h *fakeHost, k *fakeKernel) {
			h.anchors = append(h.anchors, &fakeProc{pid: 501, stamp: "x"})
		},
		"no process in the new unit": func(c *UnitCreator, h *fakeHost, k *fakeKernel) { h.anchors = nil },
		"owned by another user":      func(c *UnitCreator, h *fakeHost, k *fakeKernel) { k.cg.id.UID = 0 },
		"not cgroup2":                func(c *UnitCreator, h *fakeHost, k *fakeKernel) { k.cg.id.IsCgroup2 = false },
		"the record cannot be made durable": func(c *UnitCreator, h *fakeHost, k *fakeKernel) {
			c.Dir.FS = failSyncFS{}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c, h, k := newCreator(t)
			mutate(&c, h, k)
			_, err := c.Create(bg, 7)
			if !errors.Is(err, ErrUnitNotCreated) {
				t.Fatalf("err = %v", err)
			}
			if _, found, _ := c.Dir.ReadUnitFile(7); found {
				t.Fatal("a record was left for a unit that was refused")
			}
			if started := len(h.started) == 1 && h.startErr == nil; started != (len(h.stopped) == 1) {
				t.Fatalf("started %v stopped %v: a service that started must be stopped again", h.started, h.stopped)
			}
		})
	}
}

// A service that cannot be stopped again is named in the error, not hidden.
func TestCreateFailureNamesAServiceItCouldNotStop(t *testing.T) {
	c, h, _ := newCreator(t)
	h.anchors = nil
	h.stopErr = errors.New("bus gone")
	_, err := c.Create(bg, 7)
	if err == nil || !strings.Contains(err.Error(), h.started[0]) || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("err = %v", err)
	}
}

func TestCreateWithoutAKernelOrHostRefuses(t *testing.T) {
	if _, err := (UnitCreator{Dir: newRunDir(t, nil)}).Create(bg, 7); !errors.Is(err, ErrUnitNotCreated) {
		t.Fatalf("err = %v", err)
	}
}

func TestDiscardStopsOnlyTheUnitItRecordedAndFreesTheGeneration(t *testing.T) {
	c, h, _ := newCreator(t)
	rec, err := c.Create(bg, 7)
	if err != nil {
		t.Fatal(err)
	}
	other := rec
	other.Name = "monoagent-run-7-" + strings.Repeat("0", 32) + ".service"
	if err := c.Discard(bg, other); err == nil || len(h.stopped) != 0 {
		t.Fatalf("a record that is not on disk stopped a unit: %v %v", err, h.stopped)
	}
	if err := c.Discard(bg, rec); err != nil || len(h.stopped) != 1 || h.stopped[0] != rec.Name {
		t.Fatalf("err %v stopped %v", err, h.stopped)
	}
	if _, found, _ := c.Dir.ReadUnitFile(7); found {
		t.Fatal("the record outlived its unit")
	}
}

type failSyncFS struct{ OSFS }

func (failSyncFS) SyncDir(string) error { return errors.New("disk full") }

func TestUnitEmptyCountsOnlyTheRecordedAnchor(t *testing.T) {
	rec := testRecord()
	rec.AnchorPID, rec.AnchorStamp = 500, "st-500"
	mk := func(mut func(cg *fakeCg)) bool {
		cg := newFakeCg()
		cg.subs[""] = []*fakeProc{{pid: 500, stamp: "st-500"}}
		mut(cg)
		got, err := UnitEmpty(cg, rec)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if !mk(func(*fakeCg) {}) {
		t.Fatal("a unit holding only its anchor is empty")
	}
	if !mk(func(cg *fakeCg) { cg.subs["t-1"] = nil }) {
		t.Fatal("an unpopulated ticket sub-cgroup is empty")
	}
	if mk(func(cg *fakeCg) { cg.subs["t-1"] = []*fakeProc{{pid: 77}} }) {
		t.Fatal("a populated ticket sub-cgroup is a live member")
	}
	if mk(func(cg *fakeCg) { cg.subs[""] = append(cg.subs[""], &fakeProc{pid: 501, stamp: "x"}) }) {
		t.Fatal("a second process in the unit's own cgroup is a live member")
	}
	if mk(func(cg *fakeCg) { cg.subs[""] = []*fakeProc{{pid: 500, stamp: "reused"}} }) {
		t.Fatal("a pid with another start stamp is not the anchor")
	}
	rec.AnchorPID = 0
	if mk(func(*fakeCg) {}) {
		t.Fatal("a record without an anchor identity cannot call any process the anchor")
	}
}

func unitEnderWithService(t *testing.T) (UnitEnder, *fakeHost, *fakeKernel, UnitRecord) {
	t.Helper()
	c, h, k := newCreator(t)
	rec, err := c.Create(bg, 7)
	if err != nil {
		t.Fatal(err)
	}
	h.stopped = nil
	k.absent = false
	delete(k.cg.subs, "") // the End tests list sub-cgroups only
	return UnitEnder{K: k, Dir: c.Dir, Cfg: endCfg(allKnown), StopUnit: h.StopUnit}, h, k, rec
}

func TestStopEndsTheServiceAndProvesTheCgroupAbsent(t *testing.T) {
	u, h, k, rec := unitEnderWithService(t)
	k.cg.subs["t-1"] = []*fakeProc{{pid: 77, dieOnTerm: true, known: true}}
	out := u.End(bg, 7, nil)
	if out.Kind != UnitEnded || len(h.stopped) != 1 || h.stopped[0] != rec.Name {
		t.Fatalf("%+v stopped %v", out, h.stopped)
	}
}

func TestStopReportsAUnitWhoseServiceCannotBeStoppedOrWhoseCgroupStays(t *testing.T) {
	u, h, k, _ := unitEnderWithService(t)
	h.stopErr = errors.New("bus gone")
	if out := u.End(bg, 7, nil); out.Kind != UnitStragglers || len(out.Survivors) == 0 {
		t.Fatalf("a service that would not stop was called ended: %+v", out)
	}
	// the stop "succeeds" but the cgroup is still there
	h.stopErr = nil
	u.StopUnit = func(context.Context, string) error { return nil }
	k.absent = false
	if out := u.End(bg, 7, nil); out.Kind != UnitStragglers {
		t.Fatalf("a cgroup that stayed was called ended: %+v", out)
	}
}

// Q11: while a begun operation is excluded the unit is not stopped (its ticket sub-cgroup must stay).
func TestStopDoesNotStopTheServiceWhileATicketIsExcluded(t *testing.T) {
	u, h, k, _ := unitEnderWithService(t)
	k.cg.subs["t-1"] = []*fakeProc{{pid: 77}}
	out := u.End(bg, 7, []uint64{1})
	if len(h.stopped) != 0 {
		t.Fatalf("the service was stopped under an excluded ticket: %v (%+v)", h.stopped, out)
	}
}

func TestACreatedUnitIsFoundByAnEmptyStopAfterARebootAsGone(t *testing.T) {
	u, h, k, _ := unitEnderWithService(t)
	k.boot = "boot-2"
	if out := u.End(bg, 7, nil); out.Kind != UnitEnded || len(h.stopped) != 0 {
		t.Fatalf("%+v stopped %v", out, h.stopped)
	}
}
