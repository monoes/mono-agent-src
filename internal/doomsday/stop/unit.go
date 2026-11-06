package stop

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

// UnitRecord is the durable identity of a run's containment unit (note 13.2 section 4). It is recorded in the
// witness (authenticated) and in the run directory file (key-free copy for the CLI).
type UnitRecord struct {
	Kind       string `json:"kind"` // KindCgroup2 ("cgroup2")
	BootID     string `json:"boot_id"`
	Name       string `json:"name"` // monoagent-run-<gen>-<nonce>.service
	CgroupPath string `json:"cgroup_path"`
	CgroupID   uint64 `json:"cgroup_id"`
	OwnerUID   uint32 `json:"owner_uid"`
	Nonce      string `json:"nonce"`
	Generation uint64 `json:"generation"`
	// AnchorPID and AnchorStamp identify the one process the service itself runs (it runs no user code). They
	// let "the unit holds no member" be answered without counting the anchor; a record without them counts every
	// process as a member.
	AnchorPID   int    `json:"anchor_pid,omitempty"`
	AnchorStamp string `json:"anchor_stamp,omitempty"`
}

var unitName = regexp.MustCompile(`^monoagent-run-([0-9]+)-([0-9a-f]{32})\.service$`)

// UnitGeneration extracts the generation from a run unit name (ok false if the name is not a run unit).
func UnitGeneration(name string) (gen uint64, nonce string, ok bool) {
	m := unitName.FindStringSubmatch(name)
	if m == nil {
		return 0, "", false
	}
	g, err := strconv.ParseUint(m[1], 10, 64)
	return g, m[2], err == nil
}

// WriteUnitFile writes the key-free copy create-exclusive, mode 0600, fsynced file and directory.
func (r RunDir) WriteUnitFile(rec UnitRecord) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return writeDurable(r.fsys(), r.Dir, r.UnitPath(rec.Generation), b)
}

// ReadUnitFile reads the key-free copy; ok is false when there is none.
func (r RunDir) ReadUnitFile(gen uint64) (rec UnitRecord, ok bool, err error) {
	b, err := r.fsys().ReadFile(r.UnitPath(gen))
	if err != nil {
		return rec, false, nil
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		return rec, true, fmt.Errorf("stop: the unit file of generation %d is corrupt: %w", gen, err)
	}
	return rec, true, nil
}

// CgroupIdentity is what fstat/fstatfs on the opened descriptor report.
type CgroupIdentity struct {
	ID        uint64
	UID       uint32
	IsCgroup2 bool
}

// Cgroup is an opened cgroup directory. Every operation after the identity check goes through the descriptor
// opened by the check, never through the path again (P6).
type Cgroup interface {
	Identity() (CgroupIdentity, error)
	Subgroups() ([]string, error)
	Procs(sub string) ([]int, error)
	// ProcInfo reads the start stamp and comm through the process's own handle.
	ProcInfo(sub string, pid int) (stamp, comm string, err error)
	// Signal signals pid through a pidfd after re-checking that it is still a member of sub.
	Signal(sub string, pid int, sig syscall.Signal) error
	Kill(sub string) error // writes 1 to cgroup.kill
	Populated(sub string) (bool, error)
	Rmdir(sub string) error
	Close() error
}

// Kernel is the platform seam of the identity proof (a fake in tests; contain_linux.go in production).
type Kernel interface {
	BootID() (string, error)
	CurrentUID() uint32
	DelegatedRoot() (string, error)
	SelfCgroup() (string, error)
	// OpenCgroup opens with O_DIRECTORY|O_NOFOLLOW; an absent path returns an error satisfying IsAbsent.
	OpenCgroup(path string) (Cgroup, error)
	IsAbsent(err error) bool
	// ManagerControlGroup is the advisory P5 cross-check; answered is false when the manager does not answer.
	ManagerControlGroup(name string) (cg string, answered bool)
	ListUnitNames(root string) ([]string, error)
}

// ErrNotProven means the identity of a unit could not be proven: nothing is signalled.
var ErrNotProven = errors.New("stop: identity not proven")

// Proof is the result of the identity proof.
type Proof struct {
	Gone              bool // proven gone (a reboot, or the cgroup is absent): nothing to signal
	ManagerUnverified bool // P5: the manager did not answer; advisory only, does not block
	Cg                Cgroup
	Note              string
}

// ProveUnit proves the identity of a recorded unit through the Containment of its Kind. Any failed check returns
// ErrNotProven; an unknown kind is not proven.
func ProveUnit(k Kernel, rec UnitRecord) (Proof, error) {
	mk, ok := containments[rec.Kind]
	if !ok {
		return Proof{}, fmt.Errorf("%w: unknown unit kind %q", ErrNotProven, rec.Kind)
	}
	return mk(k, nil).Prove(rec)
}

// proveCgroup2 runs checks P1-P6 of note 13.2 section 5.
func proveCgroup2(k Kernel, rec UnitRecord) (Proof, error) {
	fail := func(f string, a ...any) (Proof, error) {
		return Proof{}, fmt.Errorf("%w: %s", ErrNotProven, fmt.Sprintf(f, a...))
	}
	// P1: a different boot means every process of the unit is gone.
	boot, err := k.BootID()
	if err != nil {
		return fail("the boot id is unreadable: %v", err)
	}
	if boot != rec.BootID {
		return Proof{Gone: true, Note: "the machine rebooted since the unit was recorded"}, nil
	}
	// P2: canonical path implied by the delegated root, the fixed name pattern and the recorded name.
	gen, nonce, ok := UnitGeneration(rec.Name)
	if !ok || gen != rec.Generation || nonce != rec.Nonce {
		return fail("the unit name does not carry the recorded generation and nonce")
	}
	root, err := k.DelegatedRoot()
	if err != nil {
		return fail("the delegated root is unknown: %v", err)
	}
	want := filepath.Join(root, rec.Name)
	if rec.CgroupPath != want || filepath.Clean(rec.CgroupPath) != rec.CgroupPath || strings.Contains(rec.CgroupPath, "..") ||
		filepath.Dir(rec.CgroupPath) != root {
		return fail("the recorded path is not the run unit path under the delegated root")
	}
	// P3: open the path without following links and check the descriptor.
	cg, err := k.OpenCgroup(rec.CgroupPath)
	if err != nil {
		if k.IsAbsent(err) {
			return Proof{Gone: true, Note: "the cgroup no longer exists"}, nil
		}
		return fail("the cgroup cannot be opened: %v", err)
	}
	id, err := cg.Identity()
	if err != nil {
		cg.Close()
		return fail("the cgroup cannot be inspected: %v", err)
	}
	if !id.IsCgroup2 || id.ID != rec.CgroupID || id.UID != rec.OwnerUID || id.UID != k.CurrentUID() {
		cg.Close()
		return fail("the descriptor does not match the recorded cgroup id, owner or filesystem")
	}
	// P4: never the signaller's own cgroup or an ancestor of it.
	self, err := k.SelfCgroup()
	if err != nil {
		cg.Close()
		return fail("the signaller's own cgroup is unknown: %v", err)
	}
	if self == rec.CgroupPath || strings.HasPrefix(self, rec.CgroupPath+"/") {
		cg.Close()
		return fail("the unit contains, or is an ancestor of, the signaller")
	}
	// P5: advisory cross-check with the manager.
	p := Proof{Cg: cg}
	if got, answered := k.ManagerControlGroup(rec.Name); !answered {
		p.ManagerUnverified = true
	} else if got != "" && !strings.HasSuffix(rec.CgroupPath, got) {
		cg.Close()
		return fail("the manager reports a different control group for %s", rec.Name)
	}
	return p, nil
}
