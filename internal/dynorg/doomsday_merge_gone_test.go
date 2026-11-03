package dynorg

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/stop"
	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// fkCg is a cgroup that records every signal sent through it: ContainmentGone must never send one.
type fkCg struct {
	id        stop.CgroupIdentity
	populated bool
	signals   int
}

func (c *fkCg) Identity() (stop.CgroupIdentity, error) { return c.id, nil }
func (c *fkCg) Subgroups() ([]string, error)           { return nil, nil }
func (c *fkCg) Procs(string) ([]int, error) {
	if c.populated {
		return []int{9}, nil // a member that is not the (unrecorded) anchor
	}
	return nil, nil
}
func (c *fkCg) ProcInfo(string, int) (string, string, error) { return "", "", errors.New("n/a") }
func (c *fkCg) Signal(string, int, syscall.Signal) error     { c.signals++; return nil }
func (c *fkCg) Kill(string) error                            { c.signals++; return nil }
func (c *fkCg) Populated(string) (bool, error)               { return c.populated, nil }
func (c *fkCg) Rmdir(string) error                           { c.signals++; return nil }
func (c *fkCg) Close() error                                 { return nil }

type fkKernel struct {
	boot, root, self string
	cg               *fkCg
	absent           bool
}

var errFkAbsent = errors.New("absent")

func (k *fkKernel) BootID() (string, error)                   { return k.boot, nil }
func (k *fkKernel) CurrentUID() uint32                        { return 1000 }
func (k *fkKernel) DelegatedRoot() (string, error)            { return k.root, nil }
func (k *fkKernel) SelfCgroup() (string, error)               { return k.self, nil }
func (k *fkKernel) IsAbsent(err error) bool                   { return errors.Is(err, errFkAbsent) }
func (k *fkKernel) ListUnitNames(string) ([]string, error)    { return nil, nil }
func (k *fkKernel) ManagerControlGroup(string) (string, bool) { return "", false }
func (k *fkKernel) OpenCgroup(string) (stop.Cgroup, error) {
	if k.absent {
		return nil, errFkAbsent
	}
	return k.cg, nil
}

const fkNonce = "0123456789abcdef0123456789abcdef"

func goneRig(t *testing.T, gen int64) (*fkKernel, stop.RunDir, store.Member) {
	t.Helper()
	k := &fkKernel{boot: "boot-1", root: "/sys/fs/cgroup/user.slice/app", self: "/sys/fs/cgroup/user.slice/host", cg: &fkCg{id: stop.CgroupIdentity{ID: 42, UID: 1000, IsCgroup2: true}}}
	dir := stop.RunDir{Dir: t.TempDir()}
	name := fmt.Sprintf("monoagent-run-%d-%s.service", gen, fkNonce)
	rec := stop.UnitRecord{Kind: "cgroup2", BootID: "boot-1", Name: name, CgroupPath: k.root + "/" + name, CgroupID: 42, OwnerUID: 1000, Nonce: fkNonce, Generation: uint64(gen)}
	if err := dir.WriteUnitFile(rec); err != nil {
		t.Fatal(err)
	}
	return k, dir, store.Member{Profile: "p", Generation: gen, ID: "merge-x", Kind: "merge"}
}

// The process question is answered by the containment proof, and "not proven" is never "gone". Nothing is ever
// signalled by it.
func TestContainmentGoneProvesOnlyWhatTheUnitProofProves(t *testing.T) {
	cases := []struct {
		name  string
		mod   func(*fkKernel)
		want  bool
		isErr bool
	}{
		{"populated unit", func(k *fkKernel) { k.cg.populated = true }, false, false},
		{"empty unit", func(*fkKernel) {}, true, false},
		{"a reboot since the record", func(k *fkKernel) { k.boot = "boot-2"; k.cg.populated = true }, true, false},
		{"cgroup absent", func(k *fkKernel) { k.absent = true }, true, false},
		{"a different cgroup id", func(k *fkKernel) { k.cg.id.ID = 7 }, false, true},
		{"the signaller inside the unit", func(k *fkKernel) { k.self = k.root + "/" + fmt.Sprintf("monoagent-run-1-%s.service", fkNonce) + "/x" }, false, true},
	}
	for _, c := range cases {
		k, dir, m := goneRig(t, 1)
		c.mod(k)
		got, err := ContainmentGone(k, dir, nil)(m)
		if got != c.want || (err != nil) != c.isErr {
			t.Errorf("%s: gone=%v err=%v, want %v (err %v)", c.name, got, err, c.want, c.isErr)
		}
		if k.cg.signals != 0 {
			t.Errorf("%s: the proof signalled the unit %d times", c.name, k.cg.signals)
		}
	}
}

// A generation with no recorded unit cannot be proven gone: a unit bound only by a name pattern and the user id
// is not authority (note 13.2 P1), so the answer is "not gone" with a reason.
func TestContainmentGoneWithoutARecordedUnitIsNotGone(t *testing.T) {
	k, _, m := goneRig(t, 1)
	empty := stop.RunDir{Dir: t.TempDir()}
	got, err := ContainmentGone(k, empty, nil)(m)
	if got || err == nil || !strings.Contains(err.Error(), "no recorded containment unit") {
		t.Errorf("gone=%v err=%v", got, err)
	}
	if got, err := ContainmentGone(nil, empty, nil)(m); got || err == nil {
		t.Errorf("no kernel: gone=%v err=%v", got, err)
	}
}

// The same function drives recovery: a populated unit keeps the member live with zero git, an empty one lets the
// repository be judged and the member released.
func TestRecoveryUsesTheContainmentProofAsItsGoneFunction(t *testing.T) {
	k, dir, _ := goneRig(t, 1)
	s := repoScene(t)
	s.seed(2, true)
	rec := s.recovery()
	rec.Gone = ContainmentGone(k, dir, nil)
	k.cg.populated = true
	out, err := rec.Recover(t.Context(), "", s.r.run.Generation)
	if err != nil || len(out.StillLive) != 1 || len(s.calls) != 0 {
		t.Fatalf("populated: %+v %v git=%v", out, err, s.calls)
	}
	k.cg.populated = false
	if out, err = rec.Recover(t.Context(), "", s.r.run.Generation); err != nil || len(out.StillLive) != 0 || out.Judged != 1 {
		t.Fatalf("empty: %+v %v", out, err)
	}
	s.want(StateIncomplete, RepairClean)
}
