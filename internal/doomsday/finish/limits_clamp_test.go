//go:build !windows

package finish

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

type fakeRlimits struct {
	hard   map[int]uint64
	set    map[int]unix.Rlimit
	getErr error
	setErr map[int]error
}

func (f *fakeRlimits) Get(res int) (unix.Rlimit, error) {
	if f.getErr != nil {
		return unix.Rlimit{}, f.getErr
	}
	h, ok := f.hard[res]
	if !ok {
		h = ^uint64(0) // unlimited
	}
	return unix.Rlimit{Cur: h, Max: h}, nil
}

func (f *fakeRlimits) Set(res int, r unix.Rlimit) error {
	if err := f.setErr[res]; err != nil {
		return err
	}
	if r.Max > f.hard[res] && f.hard[res] != 0 {
		return unix.EPERM // what the OS does: a hard limit cannot be raised
	}
	f.set[res] = r
	return nil
}

func TestClampLimit(t *testing.T) {
	for _, c := range []struct{ req, hard, want uint64 }{{4096, 2000, 2000}, {4096, 4096, 4096}, {100, 2000, 100}, {5, ^uint64(0), 5}, {^uint64(0), 7, 7}} {
		if got := clampLimit(c.req, c.hard); got != c.want {
			t.Errorf("clamp(%d,%d)=%d want %d", c.req, c.hard, got, c.want)
		}
	}
}

// A host whose hard NPROC limit is below the default (a Mac at 2000) gets that limit, not a failure; a hard limit above the
// request is lowered to the request, as on Linux.
func TestApplyLimitsNeverRaisesAHardLimitAndStillLowersTheRest(t *testing.T) {
	f := &fakeRlimits{hard: map[int]uint64{unix.RLIMIT_NPROC: 2000, unix.RLIMIT_NOFILE: 1 << 20, unix.RLIMIT_AS: 1 << 40}, set: map[int]unix.Rlimit{}}
	l := Limits{CPUSeconds: 30, OpenFiles: 1024, FileSize: 1 << 20, Procs: DefaultProcs, AddressSpace: DefaultAddressSpace}
	if err := applyLimits(f, l); err != nil {
		t.Fatal(err)
	}
	want := map[int]uint64{unix.RLIMIT_NPROC: 2000, unix.RLIMIT_NOFILE: 1024, unix.RLIMIT_AS: DefaultAddressSpace, unix.RLIMIT_CPU: 30, unix.RLIMIT_FSIZE: 1 << 20}
	for res, v := range want {
		if got := f.set[res]; got.Cur != v || got.Max != v {
			t.Errorf("resource %d set to %+v, want %d", res, got, v)
		}
	}
}

func TestApplyLimitsFailsClosedWhenALimitCannotBeReadOrSet(t *testing.T) {
	l := Limits{CPUSeconds: 30, OpenFiles: 1024, FileSize: 1 << 20, Procs: 100, AddressSpace: 1 << 30}
	if err := applyLimits(&fakeRlimits{set: map[int]unix.Rlimit{}, getErr: errors.New("boom")}, l); err == nil {
		t.Fatal("an unreadable limit passed")
	}
	f := &fakeRlimits{set: map[int]unix.Rlimit{}, setErr: map[int]error{unix.RLIMIT_FSIZE: unix.EPERM}}
	if err := applyLimits(f, l); err == nil {
		t.Fatal("a limit that cannot be set passed")
	}
}
