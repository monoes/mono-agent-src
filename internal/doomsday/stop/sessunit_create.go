package stop

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"sync"
	"time"
)

// sessContainment is the session mechanism behind the Containment interface (see sessunit.go).
type sessContainment struct {
	os  SessionOS
	led LeaderLedger
	// hostSteps are the host's own self-tests (lock, peer credentials, pinned database); nil means the macOS ones.
	hostSteps func() []ProbeStep
}

// newSessContainment is the registered constructor; the ledger is whatever the Kernel carries (WithLeaderLedger).
func newSessContainment(k Kernel) Containment {
	led, _ := k.(LeaderLedger)
	return sessContainment{os: realSessionOS(), led: led}
}

func (sessContainment) Kind() string { return KindSession }

// NeedsWitness says End acts on a record of this kind only when the witness bound it: a session number has no
// namespace of its own to bound a forged record, so the key-free run-directory file is not authority to signal.
func (sessContainment) NeedsWitness() bool { return true }

func (c sessContainment) Probe() error {
	if c.os == nil {
		return fmt.Errorf("%w: %v", ErrContainmentUnavailable, errNoSessionOS)
	}
	host := c.hostSteps
	if host == nil {
		host = macosHostSteps
	}
	steps := append([]ProbeStep{{"session create, find and kill", c.os.SelfTest}}, host()...)
	return RunProbeAll(steps)
}

func (c sessContainment) check(rec UnitRecord) error {
	var h sessHandle
	if err := json.Unmarshal([]byte(rec.Handle), &h); err != nil || h.CreatorPID <= 0 || h.CreatorStamp == "" {
		return errors.New("the unit record has no session handle")
	}
	gen, nonce, ok := UnitGeneration(rec.Name)
	if !ok || gen != rec.Generation || nonce != rec.Nonce {
		return errors.New("the unit name does not carry the recorded generation and nonce")
	}
	return nil
}

// proveView is the identity proof's read: the record is well formed, the ledger and the scan could be read, they can be
// tied together, and the signaller is not in the unit.
func (c sessContainment) proveView(rec UnitRecord) (sessView, error) {
	notProven := func(f string, a ...any) (sessView, error) {
		return sessView{}, fmt.Errorf("%w: %s", ErrNotProven, fmt.Sprintf(f, a...))
	}
	if c.os == nil {
		return notProven("%v", errNoSessionOS)
	}
	if err := c.check(rec); err != nil {
		return notProven("%v", err)
	}
	v, err := c.view(rec)
	if err != nil {
		return notProven("%v", err)
	}
	if v.why != "" {
		return notProven("%s", v.why)
	}
	self, _, err := c.os.Self()
	if err != nil {
		return notProven("the signaller's own identity is unknown: %v", err)
	}
	for _, m := range v.members {
		if m.PID == self {
			return notProven("the unit contains the signaller")
		}
	}
	for _, p := range v.all {
		if p.PID == self && slices.Contains(v.live, p.SID) {
			return notProven("the signaller is in a session of the unit")
		}
	}
	c.retireGone(rec, v) // a session seen gone is recorded as gone, whatever asked (best effort)
	return v, nil
}

// Prove is the identity proof of a recorded unit: the name carries its generation and nonce, the recorded leaders are
// read from the ledger, their sessions are found and tied to them, and the signaller is not in the unit. A unit with no
// process in it is gone (best effort: a process that left its sessions is not seen).
func (c sessContainment) Prove(rec UnitRecord) (Proof, error) {
	v, err := c.proveView(rec)
	if err != nil {
		return Proof{}, err
	}
	if len(v.members) == 0 && len(v.live) == 0 {
		if len(v.unproven) > 0 {
			return Proof{}, fmt.Errorf("%w: %s (%d process(es), none of them provably the unit's: nothing was signalled)", ErrNotProven, v.unprovenWhy, len(v.unproven))
		}
		return Proof{Gone: true, BestEffort: true, Note: "no process of the run's sessions exists; " + SessionBestEffortNote}, nil
	}
	return Proof{Cg: &sessGroup{c: c, rec: rec}, BestEffort: true, Note: SessionBestEffortNote}, nil
}

// Present says whether any process of the unit exists; an unreadable unit is an error.
func (c sessContainment) Present(rec UnitRecord) (bool, error) {
	v, err := c.view(rec)
	if err != nil {
		return false, err
	}
	if v.why != "" {
		return false, fmt.Errorf("%w: %s", ErrNotProven, v.why)
	}
	return len(v.members)+len(v.unproven) > 0, nil
}

func (c sessContainment) Create(ctx context.Context, a CreateArgs, gen uint64) (UnitRecord, error) {
	if c.os == nil {
		return UnitRecord{}, fmt.Errorf("%w: %v", ErrUnitNotCreated, errNoSessionOS)
	}
	if c.led == nil {
		return UnitRecord{}, fmt.Errorf("%w: %v", ErrUnitNotCreated, errNoLedger)
	}
	return createSession(ctx, c, a, gen)
}

// createSession makes the run's session unit: intent in the journal, then the record bound in the journal and written
// to the key-free file. NO process is started: the record is the unit, and a turn's leader joins it at Adopt. "No unit,
// no run" still holds, because without the record no placement is possible.
func createSession(_ context.Context, c sessContainment, a CreateArgs, gen uint64) (UnitRecord, error) {
	o := c.os
	var rd io.Reader = rand.Reader
	if a.Rand != nil {
		rd = a.Rand
	}
	nb := make([]byte, 16)
	if _, err := io.ReadFull(rd, nb); err != nil {
		return UnitRecord{}, fmt.Errorf("%w: no randomness: %v", ErrUnitNotCreated, err)
	}
	nonce := hex.EncodeToString(nb)
	name := fmt.Sprintf("monoagent-run-%d-%s.service", gen, nonce)
	boot, err := o.BootID()
	if err != nil {
		return UnitRecord{}, fmt.Errorf("%w: the boot id is unreadable: %v", ErrUnitNotCreated, err)
	}
	cpid, cstamp, err := o.Self()
	if err != nil {
		return UnitRecord{}, fmt.Errorf("%w: the creator cannot be identified: %v", ErrUnitNotCreated, err)
	}
	if _, err := o.Scan(); err != nil {
		return UnitRecord{}, fmt.Errorf("%w: the processes cannot be listed, so the unit could never be found: %v", ErrUnitNotCreated, err)
	}
	if err := a.Dir.fsys().MkdirAll(a.Dir.Dir, 0o700); err != nil {
		return UnitRecord{}, fmt.Errorf("%w: the run directory cannot be created: %v", ErrUnitNotCreated, err)
	}
	if a.Journal != nil {
		if err := a.Journal.Intent(gen, name, nonce, boot); err != nil {
			return UnitRecord{}, fmt.Errorf("%w: the witness refused the unit intent: %v", ErrUnitNotCreated, err)
		}
	}
	h, _ := json.Marshal(sessHandle{CreatorPID: cpid, CreatorStamp: cstamp})
	rec := UnitRecord{Kind: KindSession, BootID: boot, Name: name, CgroupID: idFromNonce(nonce), Nonce: nonce,
		Generation: gen, Handle: string(h)}
	if err := c.check(rec); err != nil { // the record must pass the proof that every later step applies to it
		return UnitRecord{}, fmt.Errorf("%w: the new unit fails its own identity proof: %v", ErrUnitNotCreated, err)
	}
	if a.Journal != nil {
		if err := a.Journal.Bound(rec); err != nil {
			return UnitRecord{}, fmt.Errorf("%w: the witness refused the unit binding: %v", ErrUnitNotCreated, err)
		}
	}
	if err := a.Dir.WriteUnitFile(rec); err != nil {
		return UnitRecord{}, fmt.Errorf("%w: the unit record cannot be written: %v", ErrUnitNotCreated, err)
	}
	return rec, nil
}

// Adopter is the optional half of a Placement that records a started turn's leader in the unit: only the session unit
// has it (a cgroup, a Job Object and a process group take the command at birth). Adopt is called by the host AFTER the
// process was started and pinned (pid and start stamp, while it is still its own unreaped child) and BEFORE anything is
// delivered to it; an error means the turn must not be released and its process must be ended.
type Adopter interface {
	Adopt(pid int, stamp string) error
}

// sessPlacer places commands in a proven session unit. Placement changes nothing about the command: it only refuses what
// could not become a recorded session leader. The unit exists as long as its record does, so an empty unit is placed
// into (unlike a Prove, which calls it gone); the host's gate, not this placer, decides that a stopped run places nothing.
type sessPlacer struct {
	c   sessContainment
	rec UnitRecord
}

func (c sessContainment) Place(rec UnitRecord) (Placement, error) {
	if c.os == nil {
		return nil, fmt.Errorf("%w: %v", ErrNotPlaced, errNoSessionOS)
	}
	return &sessPlacer{c: c, rec: rec}, nil
}

func (p *sessPlacer) Place(cmd *exec.Cmd) (func(), error) { return p.place(cmd) }

// errNoAdoptPath is why a placement other than Place is refused: only a turn leader that the host starts as a session of
// its own and then Adopts is inside this unit. A command placed any other way would run outside it while the unit
// reports itself empty, so it is not placed at all.
var errNoAdoptPath = errors.New("a session unit takes only a started turn leader (Place, then Adopt); this placement cannot be adopted")

func (p *sessPlacer) PlaceTicket(uint64, *exec.Cmd) (func(), error) {
	return nil, fmt.Errorf("%w: %v", ErrNotPlaced, errNoAdoptPath)
}

// StandalonePlacement is the placement of a command that is not a turn (a merge's git command): the placement makes the command a
// session leader itself and the caller must call AdoptPlaced right after the command started, before it waits for it. A ticket is
// not used: like the process-group and Job Object units, the session unit cannot spare one command, so Stop ends it.
type StandalonePlacement interface {
	PlaceStandalone(cmd *exec.Cmd) (release func(), err error)
}

var _ StandalonePlacement = (*sessPlacer)(nil)

type pendingLeader struct {
	p     *sessPlacer
	stamp string // set once the started command was adopted
}

var pendingAdopt = struct {
	mu sync.Mutex
	by map[*exec.Cmd]*pendingLeader
}{by: map[*exec.Cmd]*pendingLeader{}}

func (p *sessPlacer) PlaceStandalone(cmd *exec.Cmd) (func(), error) {
	if cmd == nil || cmd.Process != nil {
		return nil, fmt.Errorf("%w: the command is nil or already started", ErrNotPlaced)
	}
	if err := prepareSessCmd(cmd); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotPlaced, err)
	}
	if _, err := p.place(cmd); err != nil {
		return nil, err
	}
	pl := &pendingLeader{p: p}
	pendingAdopt.mu.Lock()
	pendingAdopt.by[cmd] = pl
	pendingAdopt.mu.Unlock()
	return func() {
		pendingAdopt.mu.Lock()
		delete(pendingAdopt.by, cmd)
		stamp := pl.stamp
		pendingAdopt.mu.Unlock()
		if stamp != "" && cmd.Process != nil { // the command's own wait reaped it: let the hold's entry go (a bounded table)
			dropHold(p.rec.CgroupID, cmd.Process.Pid, stamp)
		}
	}, nil
}

// AdoptPlaced records the leader of a command placed by PlaceStandalone, right after it started: its pid with the start stamp read
// now, while it is still the caller's unreaped child. A command that was not placed that way (no placement, another kind) is
// nothing to adopt. An error means the caller must end the command: it is running in a session the unit does not know.
func AdoptPlaced(cmd *exec.Cmd) error {
	pendingAdopt.mu.Lock()
	pl := pendingAdopt.by[cmd]
	pendingAdopt.mu.Unlock()
	if pl == nil {
		return nil
	}
	if cmd.Process == nil {
		return fmt.Errorf("%w: the command did not start", ErrNotPlaced)
	}
	info, err := pl.p.c.os.Info(cmd.Process.Pid)
	if err != nil {
		return fmt.Errorf("%w: the started command cannot be read: %v", ErrNotPlaced, err)
	}
	pendingAdopt.mu.Lock()
	pl.stamp = info.Stamp
	pendingAdopt.mu.Unlock()
	return pl.p.Adopt(cmd.Process.Pid, info.Stamp)
}

func (p *sessPlacer) PlaceLeased(*exec.Cmd) error {
	return fmt.Errorf("%w: %v", ErrNotPlaced, errNoAdoptPath)
}

func (p *sessPlacer) place(cmd *exec.Cmd) (func(), error) {
	if cmd == nil || cmd.Process != nil {
		return nil, fmt.Errorf("%w: the command is nil or already started", ErrNotPlaced)
	}
	v, err := p.c.proveView(p.rec)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotPlaced, err)
	}
	if v.rebooted {
		return nil, fmt.Errorf("%w: the unit was recorded before the machine's boot time changed", ErrNotPlaced)
	}
	if err := p.c.os.CheckCmd(cmd); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotPlaced, err)
	}
	return func() {}, nil
}

// Adopt records the leader of a started turn. The process must be the caller's own child, alive with that start stamp,
// the leader of a session of its own and the caller's user: anything else is refused, because every recorded leader is
// later signalled. It is written to the ledger (the witness) before Adopt returns; a ledger that cannot take the line
// refuses, and nothing is recorded.
func (p *sessPlacer) Adopt(pid int, stamp string) error {
	if p.c.led == nil {
		return fmt.Errorf("%w: %v", ErrNotPlaced, errNoLedger)
	}
	v, err := p.c.proveView(p.rec)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotPlaced, err)
	}
	if v.rebooted {
		return fmt.Errorf("%w: the unit was recorded before the machine's boot time changed", ErrNotPlaced)
	}
	if _, ok := parseSessStamp(stamp); !ok || pid <= 1 {
		return fmt.Errorf("%w: the leader (pid %d, start %q) cannot be checked", ErrNotPlaced, pid, stamp)
	}
	self, _, err := p.c.os.Self()
	if err != nil {
		return fmt.Errorf("%w: the adopter's own identity is unknown: %v", ErrNotPlaced, err)
	}
	cur, err := p.c.os.Info(pid)
	switch {
	case err != nil:
		return fmt.Errorf("%w: the leader %d is not running: %v", ErrNotPlaced, pid, err)
	case cur.Stamp != stamp:
		return fmt.Errorf("%w: process %d is not the one that was started (start stamp differs)", ErrNotPlaced, pid)
	case cur.SID != pid:
		return fmt.Errorf("%w: process %d is not the leader of a session of its own", ErrNotPlaced, pid)
	case cur.PPID != self:
		return fmt.Errorf("%w: process %d is not a child of the process that adopts it", ErrNotPlaced, pid)
	case cur.UID != p.c.os.CurrentUID():
		return fmt.Errorf("%w: process %d belongs to another user", ErrNotPlaced, pid)
	}
	have, err := p.c.led.Leaders(p.rec.Generation)
	if err != nil {
		return fmt.Errorf("%w: the leader ledger cannot be read: %v", ErrNotPlaced, err)
	}
	if slices.ContainsFunc(have, func(l Leader) bool { return l.PID == pid && l.Stamp == stamp && l.Unit == p.rec.CgroupID }) {
		if err := holdFor(p.c.os, p.rec.CgroupID, pid, stamp); err != nil { // already recorded: it is still held (idempotent)
			return fmt.Errorf("%w: the leader's number cannot be kept: %v", ErrNotPlaced, err)
		}
		return nil
	}
	if err := holdFor(p.c.os, p.rec.CgroupID, pid, stamp); err != nil { // the number stays reserved from here on
		return fmt.Errorf("%w: the leader's number cannot be kept: %v", ErrNotPlaced, err)
	}
	if err := p.c.led.AddLeader(p.rec.Generation, Leader{PID: pid, Stamp: stamp, Unit: p.rec.CgroupID, At: time.Now().Unix()}); err != nil {
		// The hold is KEPT: the launcher kills this child now, by number, and a released hold would reap it at once and free the number
		// under that kill. The unrecorded leader's zombie is let go with the unit (ReleaseUnitHolds) or at shutdown, and is counted
		// against sessMaxHolds meanwhile.
		return fmt.Errorf("%w: %w: the witness refused the leader: %v", ErrNotPlaced, ErrReapOwned, err)
	}
	return nil
}

var _ Adopter = (*sessPlacer)(nil)

// ErrReapOwned is wrapped in the error of an Adopt that refused AFTER it took the leader's hold: the hold is kept (the number stays
// reserved while the launcher ends the child), so the launcher must not wait for the process itself. Every other refusal happens
// before the hold and leaves the leader the launcher's own child to reap.
var ErrReapOwned = errors.New("stop: the unit has taken over reaping the leader")

// UnitID is the identity of the unit this placement belongs to (UnitRecord.CgroupID): what ReleaseUnitHolds is called with.
func (p *sessPlacer) UnitID() uint64 { return p.rec.CgroupID }
