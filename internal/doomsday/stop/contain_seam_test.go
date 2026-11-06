package stop

import (
	"errors"
	"testing"
)

// recContainment records that the seam, not the Linux code directly, answered.
type recContainment struct {
	probed, proved int
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
	if p, err := ProveUnit(newFakeKernel(), rec); err != nil || p.Note != "seam" || r.proved != 1 {
		t.Fatalf("a registered kind was not dispatched to: %+v %v %d", p, err, r.proved)
	}
}

func TestUnknownPlatformStaysRefused(t *testing.T) {
	for _, goos := range []string{"windows", "darwin", "freebsd", "", "LINUX"} {
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
