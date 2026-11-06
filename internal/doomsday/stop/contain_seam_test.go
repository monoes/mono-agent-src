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

// A kind with no Containment, and a platform with no kind, are refused (no fallback).
func TestUnknownKindAndPlatformStayRefused(t *testing.T) {
	rec := testRecord()
	rec.Kind = "jobobject"
	if _, err := ProveUnit(newFakeKernel(), rec); !errors.Is(err, ErrNotProven) {
		t.Fatalf("%v", err)
	}
	if err := probe("windows", func() []ProbeStep { return nil }); !errors.Is(err, ErrContainmentUnavailable) {
		t.Fatalf("%v", err)
	}
}
