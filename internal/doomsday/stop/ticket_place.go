package stop

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// Placer starts processes INSIDE a run's containment unit (note 13.2 section 3). Each Place call proves the unit's
// identity again (ProveUnit, the same checks Stop uses), creates one sub-cgroup t-<n> in it and makes the command
// start there: the child is BORN in that cgroup by clone3 with CLONE_INTO_CGROUP, so it was never anywhere else and
// no hold stub or move-after-fork is needed for it. UnitEmpty therefore sees the child for as long as it lives.
//
// A unit that cannot be proven, or that is gone, refuses the placement: nothing is started outside the unit as a
// fallback.
//
// The sub-cgroup of a command placed for a gate ticket (PlaceTicket) is named t-<ticket id>-<random>, so Stop's
// exclusion of a begun operation's tickets (Gate.ContinuingTickets) names exactly what Place created. A command
// placed without a ticket (Place) gets r-<random>: nothing excludes it, Stop ends it.
type Placer struct {
	K   Kernel
	Rec UnitRecord

	openSub func(unit Cgroup, name string) (subCgroup, error) // tests; nil means the real kernel
}

// subCgroup is what Place needs of a ticket's sub-cgroup (TicketCgroup in production).
type subCgroup interface {
	Name() string
	Apply(*exec.Cmd)
	Close() error
}

func (p *Placer) open(unit Cgroup, name string) (subCgroup, error) {
	if p.openSub != nil {
		return p.openSub(unit, name)
	}
	tc, err := OpenSubCgroup(unit, name)
	if err != nil {
		return nil, err
	}
	return tc, nil
}

// ticketSubName is the sub-cgroup name of a command placed for the gate ticket.
func ticketSubName(ticket uint64, rnd uint64) string { return fmt.Sprintf("t-%d-%x", ticket, rnd) }

// excludedSub reports whether the sub-cgroup sub belongs to a ticket named in exclude (keys "t-<id>"): the
// ticket's own name or one of its t-<id>-<random> sub-cgroups.
func excludedSub(exclude map[string]bool, sub string) bool {
	if exclude[sub] {
		return true
	}
	if !strings.HasPrefix(sub, "t-") {
		return false
	}
	if i := strings.IndexByte(sub[2:], '-'); i >= 0 {
		return exclude[sub[:2+i]]
	}
	return false
}

// ErrNotPlaced means the command could not be put in the run's unit; it was not started.
var ErrNotPlaced = errors.New("stop: the process cannot be placed in the run's containment unit")

// NewPlacer reads the generation's unit record from the run directory (the record Create wrote before the run
// existed). No record, no placer.
func NewPlacer(k Kernel, dir RunDir, gen uint64) (Placement, error) {
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
	if rec.Generation != gen {
		return nil, fmt.Errorf("%w: the unit record names generation %d, not %d", ErrNotPlaced, rec.Generation, gen)
	}
	mk, ok := containments[rec.Kind]
	if !ok {
		return nil, fmt.Errorf("%w: unknown unit kind %q", ErrNotPlaced, rec.Kind)
	}
	if rec.Kind != hostKind() {
		return nil, fmt.Errorf("%w: a %q unit is not placed into on %s", ErrNotPlaced, rec.Kind, hostGOOS)
	}
	return mk(k, nil).Place(rec)
}

// Place makes cmd start in a fresh ticket sub-cgroup of the unit. release closes the descriptor and removes the
// sub-cgroup when it is empty; call it after the command ended (Run or Wait returned). On error nothing was
// created that release would need.
func (p *Placer) Place(cmd *exec.Cmd) (release func(), err error) {
	return p.place(cmd, nil)
}

// PlaceTicket is Place for a command run under the gate ticket: its sub-cgroup carries the ticket's id, so Stop
// spares it while the ticket's operation is CONTINUING (Q11: a begun merge finishes).
func (p *Placer) PlaceTicket(ticket uint64, cmd *exec.Cmd) (release func(), err error) {
	return p.place(cmd, &ticket)
}

func (p *Placer) place(cmd *exec.Cmd, ticket *uint64) (release func(), err error) {
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
	rnd := binary.LittleEndian.Uint64(b[:])
	name := fmt.Sprintf("r-%x", rnd)
	if ticket != nil {
		name = ticketSubName(*ticket, rnd)
	}
	tc, err := p.open(proof.Cg, name)
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

// verifyKinds stub (red commit).
var verifyKinds = map[string]string{}

func verifyKindOf(goos string) string { return platformKinds[goos] }

// NewVerifyPlacer stub (red commit).
func NewVerifyPlacer(k Kernel, dir RunDir, gen uint64) (Placement, error) {
	return NewPlacer(k, dir, gen)
}
