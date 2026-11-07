//go:build e2estub

package stop

import (
	"fmt"
	"os/exec"
)

// THE STUB-RUNTIME END-TO-END BUILD (tag e2estub, see cmd/monoagentcli/doomsday_e2e_stub.go). In a private namespace there is no
// cgroup tree to place a process into, and the stub's fake unit is not a Linux cgroup descriptor, so the Linux placement would
// refuse every turn. This replaces ONLY the placement of the cgroup2 mechanism: the unit is still proved (its identity checks run
// against the stub's kernel), and the command is then started as it is, in no cgroup. No shipped build carries this file.

type e2ePlacement struct{}

func (e2ePlacement) Place(*exec.Cmd) (func(), error)               { return func() {}, nil }
func (e2ePlacement) PlaceTicket(uint64, *exec.Cmd) (func(), error) { return func() {}, nil }
func (e2ePlacement) PlaceLeased(*exec.Cmd) error                   { return nil }

type e2eContainment struct{ cgroup2Containment }

func (c e2eContainment) Place(rec UnitRecord) (Placement, error) {
	p, err := proveCgroup2(c.k, rec)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotPlaced, err)
	}
	if p.Gone || p.Cg == nil {
		return nil, fmt.Errorf("%w: the unit is gone", ErrNotPlaced)
	}
	_ = p.Cg.Close()
	return e2ePlacement{}, nil
}

func init() {
	containments[KindCgroup2] = func(k Kernel, steps func() []ProbeStep) Containment {
		return e2eContainment{cgroup2Containment{k: k, steps: steps}}
	}
}
