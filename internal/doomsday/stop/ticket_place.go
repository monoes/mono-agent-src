package stop

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
)

// Placer starts processes INSIDE a run's containment unit (note 13.2 section 3). Each Place call proves the unit's
// identity again (ProveUnit, the same checks Stop uses), creates one sub-cgroup t-<n> in it and makes the command
// start there: the child is BORN in that cgroup by clone3 with CLONE_INTO_CGROUP, so it was never anywhere else and
// no hold stub or move-after-fork is needed for it. UnitEmpty therefore sees the child for as long as it lives.
//
// A unit that cannot be proven, or that is gone, refuses the placement: nothing is started outside the unit as a
// fallback.
type Placer struct {
	K   Kernel
	Rec UnitRecord
}

// ErrNotPlaced means the command could not be put in the run's unit; it was not started.
var ErrNotPlaced = errors.New("stop: the process cannot be placed in the run's containment unit")

// NewPlacer reads the generation's unit record from the run directory (the record Create wrote before the run
// existed). No record, no placer.
func NewPlacer(k Kernel, dir RunDir, gen uint64) (*Placer, error) {
	if k == nil {
		return nil, fmt.Errorf("%w: no containment kernel is wired", ErrNotPlaced)
	}
	rec, ok, err := dir.ReadUnitFile(gen)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotPlaced, err)
	}
	if !ok {
		return nil, fmt.Errorf("%w: generation %d has no recorded unit", ErrNotPlaced, gen)
	}
	return &Placer{K: k, Rec: rec}, nil
}

// Place makes cmd start in a fresh ticket sub-cgroup of the unit. release closes the descriptor and removes the
// sub-cgroup when it is empty; call it after the command ended (Run or Wait returned). On error nothing was
// created that release would need.
func (p *Placer) Place(cmd *exec.Cmd) (release func(), err error) {
	if cmd == nil || cmd.Process != nil {
		return nil, fmt.Errorf("%w: the command is nil or already started", ErrNotPlaced)
	}
	proof, err := ProveUnit(p.K, p.Rec)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotPlaced, err)
	}
	if proof.Gone {
		return nil, fmt.Errorf("%w: the unit is gone", ErrNotPlaced)
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		proof.Cg.Close()
		return nil, fmt.Errorf("%w: no randomness: %v", ErrNotPlaced, err)
	}
	tc, err := OpenTicketCgroup(proof.Cg, binary.LittleEndian.Uint64(b[:]))
	if err != nil {
		proof.Cg.Close()
		return nil, fmt.Errorf("%w: %v", ErrNotPlaced, err)
	}
	tc.Apply(cmd)
	return func() {
		_ = tc.Close()
		_ = proof.Cg.Rmdir(tc.Name()) // refused while a member lives: Stop's EndUnit removes what is left
		_ = proof.Cg.Close()
	}, nil
}

// PlaceLeased is Place for a process that outlives the call that starts it (a coder turn): the command is placed
// the same way, and the ticket cgroup is released when cmd is garbage collected, which is long after Start
// returned. A cgroup that still holds the running process is not removed by the release (the kernel refuses);
// Stop's EndUnit ends and removes every ticket cgroup that is left.
func (p *Placer) PlaceLeased(cmd *exec.Cmd) error {
	release, err := p.Place(cmd)
	if err != nil {
		return err
	}
	runtime.AddCleanup(cmd, func(f func()) { f() }, release)
	return nil
}
