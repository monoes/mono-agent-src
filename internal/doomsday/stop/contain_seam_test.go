package stop

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// recContainment records that the seam, not the Linux code directly, answered.
type recContainment struct {
	probed, proved, created, placed, present int
}

func (*recContainment) Kind() string { return KindCgroup2 }
func (r *recContainment) Probe() error {
	r.probed++
	return errors.New("seam probe")
}
func (r *recContainment) Prove(UnitRecord) (Proof, error) {
	r.proved++
	return Proof{Note: "seam"}, nil
}
func (r *recContainment) Create(context.Context, CreateArgs, uint64) (UnitRecord, error) {
	r.created++
	return UnitRecord{Kind: r.Kind()}, nil
}
func (r *recContainment) Place(UnitRecord) (Placement, error) { r.placed++; return nil, nil }
func (r *recContainment) Present(UnitRecord) (bool, error)    { r.present++; return false, nil }

func swapContainment(t *testing.T, kind string, c Containment) {
	t.Helper()
	old, had := containments[kind]
	containments[kind] = func(Kernel, func() []ProbeStep) Containment { return c }
	t.Cleanup(func() {
		if had {
			containments[kind] = old
		} else {
			delete(containments, kind)
		}
	})
}

// The Linux unit kind is reached only through the Containment seam: ProveUnit and probe both go through it.
func TestLinuxUnitsGoThroughTheContainmentSeam(t *testing.T) {
	r := &recContainment{}
	swapContainment(t, KindCgroup2, r)
	if p, err := ProveUnit(newFakeKernel(), testRecord()); err != nil || p.Note != "seam" || r.proved != 1 {
		t.Fatalf("ProveUnit bypassed the seam: %+v %v %d", p, err, r.proved)
	}
	if err := probe("linux", func() []ProbeStep { t.Fatal("linux steps consulted directly"); return nil }); err == nil || r.probed != 1 {
		t.Fatalf("probe bypassed the seam: %v %d", err, r.probed)
	}
}

// A kind with no Containment is refused, and a kind that has one is dispatched to it: the second half is what
// goes red if the dispatch is replaced by a fixed answer.
func TestUnknownKindRefusedAndRegisteredKindDispatched(t *testing.T) {
	rec := testRecord()
	rec.Kind = "jobobject"
	if _, err := ProveUnit(newFakeKernel(), rec); !errors.Is(err, ErrNotProven) {
		t.Fatalf("%v", err)
	}
	r := &recContainment{}
	swapContainment(t, "jobobject", r)
	// a registered kind that is not THIS platform's kind is refused: a record from another OS is never proven here
	if _, err := ProveUnit(newFakeKernel(), rec); !errors.Is(err, ErrNotProven) || r.proved != 0 {
		t.Fatalf("a kind of another platform was dispatched to: %v %d", err, r.proved)
	}
	hostAs(t, "windows")
	if p, err := ProveUnit(newFakeKernel(), rec); err != nil || p.Note != "seam" || r.proved != 1 {
		t.Fatalf("a registered kind was not dispatched to: %+v %v %d", p, err, r.proved)
	}
	if _, err := ProveUnit(newFakeKernel(), testRecord()); !errors.Is(err, ErrNotProven) {
		t.Fatalf("a cgroup2 record is proven on a Windows host: %v", err)
	}
}

// darwin left this list when macOS got its own containment (KindPGroup); on a macOS CI runner it would have started a
// real anchor here. "DARWIN" stays: GOOS names are exact.
func TestUnknownPlatformStaysRefused(t *testing.T) {
	for _, goos := range []string{"freebsd", "openbsd", "", "LINUX", "WINDOWS", "DARWIN"} {
		if err := probe(goos, func() []ProbeStep { return nil }); !errors.Is(err, ErrContainmentUnavailable) {
			t.Fatalf("%q: %v", goos, err)
		}
	}
}

func TestLinuxMapsToCgroup2(t *testing.T) {
	if platformKinds["linux"] != KindCgroup2 {
		t.Fatalf("linux maps to %q", platformKinds["linux"])
	}
	if _, ok := containments[KindCgroup2]; !ok {
		t.Fatal("no containment for the cgroup2 kind")
	}
}

func TestRegistriesAreConsistent(t *testing.T) {
	for goos, kind := range platformKinds {
		if _, ok := containments[kind]; !ok {
			t.Fatalf("%s maps to kind %q which has no containment", goos, kind)
		}
	}
	for key, mk := range containments {
		if got := mk(nil, nil).Kind(); got != key {
			t.Fatalf("registered under %q but reports kind %q", key, got)
		}
	}
}

func TestMalformedKindsAreRefused(t *testing.T) {
	for _, kind := range []string{"", "CGROUP2", "Cgroup2", " cgroup2", "cgroup2 ", "cgroup2\n"} {
		rec := testRecord()
		rec.Kind = kind
		if _, err := ProveUnit(newFakeKernel(), rec); !errors.Is(err, ErrNotProven) {
			t.Fatalf("%q: %v", kind, err)
		}
	}
}

// The Linux unit's absence check (used after its service was stopped) is the old OpenCgroup/IsAbsent decision.
func TestCgroup2PresentFollowsTheKernel(t *testing.T) {
	k := newFakeKernel()
	c := cgroup2Containment{k: k}
	if present, err := c.Present(testRecord()); err != nil || !present {
		t.Fatalf("an openable cgroup is present: %v %v", present, err)
	}
	k.absent = true
	if present, err := c.Present(testRecord()); err != nil || present {
		t.Fatalf("an absent cgroup is not present: %v %v", present, err)
	}
}

// A Linux unit's stop after the service stopped reports a cgroup that is still there as a straggler, never ended.
func TestStopServiceReportsAUnitThatIsStillPresent(t *testing.T) {
	k := newFakeKernel()
	rec := testRecord()
	u := UnitEnder{K: k, StopUnit: func(context.Context, string) error { return nil }}
	out := u.stopService(bg, rec, UnitOutcome{Kind: UnitEnded})
	if out.Kind != UnitStragglers || len(out.Survivors) != 1 || !strings.Contains(out.Survivors[0].Why, "cgroup is still present") {
		t.Fatalf("%+v", out)
	}
	k.absent = true
	if out := u.stopService(bg, rec, UnitOutcome{Kind: UnitEnded}); out.Kind != UnitEnded {
		t.Fatalf("%+v", out)
	}
}
