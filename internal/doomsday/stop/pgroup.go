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
	"syscall"
	"time"
)

// pgPause is how the mechanism waits for a signal to take effect (macOS ends a killed process asynchronously); tests
// replace it so they do not sleep.
var pgPause = time.Sleep

const (
	pgRmdirPolls = 20                    // Members is read this many times after the group was killed ...
	pgRmdirPoll  = 25 * time.Millisecond // ... this far apart, before the group counts as "still holding processes"
)

// The macOS unit: a PROCESS GROUP led by an ANCHOR process. The anchor (a blocking system binary, run detached) is
// its own process-group leader, so the group's number is the anchor's pid and, while the anchor lives, nobody else
// can hold that number. Commands are BORN in the group (SysProcAttr.Pgid), and Stop signals the group and re-sweeps
// its members. The mechanism is BEST EFFORT and says so: a process that calls setsid or setpgid, or double-forks out
// of the group, is not a member any more and CANNOT BE SEEN from here, so every end of such a unit is
// UnitBestEffort and never "stopped" bare. There is no launchd job. All OS calls are behind PGroupOS; this file is
// portable, so the logic is tested on Linux with a fake and only contain_darwin.go touches macOS.
//
// The anchor is started under a NAME that carries the unit's generation and nonce (argv[0] of the exec, read back by
// the proof with kern.procargs2), so a record can only name a process that was started for this unit: the pid and the
// start time alone would let a forged record aim the kill at any process group of the same user. The key-free
// run-directory record is never enough to signal anything either: End acts only on a record the witness bound
// (pgContainment.NeedsWitness, enforced in UnitEnder.End).
//
// The anchor is NOT a new session (Setsid): a process can join a process group only inside its own session
// (setpgid, EPERM across sessions), so a group in a session of its own could not be joined by the commands the host
// starts. The anchor shares the session of the process that created it; Place refuses a caller of another session.

// KindPGroup is the UnitRecord.Kind of the macOS unit. Best effort (see UnitBestEffort).
const KindPGroup = "pgroup"

// PGroupOS is the seam over the macOS API (contain_darwin.go in production, a fake in tests).
type PGroupOS interface {
	BootID() (string, error)
	// Self is the pid and process start stamp of the caller.
	Self() (pid int, stamp string, err error)
	// SelfGroup is the caller's process group and session.
	SelfGroup() (pgid, sid int, err error)
	// StartAnchor starts the detached anchor as the leader of a new process group, under the name argv0. When it
	// returns an error with a non-zero pid the anchor may be running (its start time could not be read): the caller
	// ends it with KillAnchor(pid, stamp), and an EMPTY stamp means "the child this process just started and has not
	// reaped", the one case where a pid is still ours.
	StartAnchor(argv0 string) (pid int, stamp string, err error)
	// KillAnchor ends the anchor if pid still has that start stamp (abandoning a unit that was not recorded).
	KillAnchor(pid int, stamp string) error
	// Argv0 is the name the process was started under (argv[0]); an error for a process that is absent or only a
	// zombie, or whose arguments cannot be read.
	Argv0(pid int) (string, error)
	// ProcInfo reads the start stamp and name; an error for a process that is absent or only a zombie.
	ProcInfo(pid int) (stamp, comm string, err error)
	// PgidOf is the process group of pid; an error for a process that is absent or only a zombie.
	PgidOf(pid int) (int, error)
	// Members lists the live (not zombie) processes of the group.
	Members(pgid int) ([]int, error)
	SignalGroup(pgid int, sig syscall.Signal) error
	SignalPid(pid int, sig syscall.Signal) error
	// Prepare makes cmd's child be created in the group.
	Prepare(cmd *exec.Cmd, pgid int) error
}

// pgHandle is UnitRecord.Handle of a KindPGroup unit: the group number (the anchor's pid), the session it lives in
// and the creator's identity. The anchor's identity is the record's AnchorPID/AnchorStamp.
type pgHandle struct {
	Pgid         int    `json:"pgid"`
	Sid          int    `json:"sid"`
	CreatorPID   int    `json:"creator_pid"`
	CreatorStamp string `json:"creator_stamp"`
}

type pgContainment struct{ os PGroupOS }

func newPGContainment(Kernel) Containment { return pgContainment{os: realPGroupOS()} }

func (pgContainment) Kind() string { return KindPGroup }

var errNoPGroupOS = errors.New("the process-group mechanism is only available on macOS")

func (c pgContainment) Probe() error {
	if c.os == nil {
		return fmt.Errorf("%w: %v", ErrContainmentUnavailable, errNoPGroupOS)
	}
	return RunProbeAll(append(pgSelfTestSteps(c.os), macosHostSteps()...))
}

// pgSelfTestSteps proves the mechanism works here: an anchor leads a group of its own, is listed as its member and
// ends with a signal to the group.
func pgSelfTestSteps(o PGroupOS) []ProbeStep {
	return []ProbeStep{{"process group create and kill", func() error { return pgSelfTest(o) }}}
}

// macosHostSteps are what the rest of the mode needs on macOS besides the containment: the host and witness lock (flock),
// the peer identity of the emergency channel (LOCAL_PEERCRED) and the pinned database binding (kqueue vnode events).
// Each is a runtime self-test (host_probe.go), so the mode starts on macOS only when all three pass on the machine it
// runs on. Deleting a step, or leaving its check nil, turns the probe red (TestHostStepsExist...).
func macosHostSteps() []ProbeStep { return hostProbeSteps(hostChecksNow()) }

func pgSelfTest(o PGroupOS) error {
	var nb [8]byte
	if _, err := rand.Read(nb[:]); err != nil {
		return err
	}
	pid, stamp, err := o.StartAnchor(fmt.Sprintf("monoagent-probe-%x", nb))
	if pid != 0 {
		defer o.KillAnchor(pid, stamp)
	}
	if err != nil {
		return err
	}
	if g, err := o.PgidOf(pid); err != nil || g != pid {
		return fmt.Errorf("the anchor does not lead a group of its own (%d, %v)", g, err)
	}
	if ms, err := o.Members(pid); err != nil || len(ms) != 1 || ms[0] != pid {
		return fmt.Errorf("the group does not list its anchor (%v, %v)", ms, err)
	}
	if err := o.SignalGroup(pid, syscall.SIGKILL); err != nil {
		return err
	}
	for i := 0; i < 40; i++ {
		if ms, err := o.Members(pid); err == nil && len(ms) == 0 {
			return nil
		}
		pgPause(50 * time.Millisecond)
	}
	return errors.New("the group still holds a process after the kill")
}

// NeedsWitness says End acts on a record of this kind only when the witness bound it: the group number has no
// namespace of its own to bound a forged record (a cgroup path, a unit-name pattern), so the key-free run-directory
// file is not authority to signal.
func (pgContainment) NeedsWitness() bool { return true }

func (c pgContainment) Prove(rec UnitRecord) (Proof, error) {
	if c.os == nil {
		return Proof{}, fmt.Errorf("%w: %v", ErrNotProven, errNoPGroupOS)
	}
	return proveGroup(c.os, rec)
}

// Present: the unit exists while its anchor lives with its recorded start time, or any process is in its group.
func (c pgContainment) Present(rec UnitRecord) (bool, error) {
	if c.os == nil {
		return false, errNoPGroupOS
	}
	if stamp, _, err := c.os.ProcInfo(rec.AnchorPID); err == nil && stamp == rec.AnchorStamp {
		return true, nil
	}
	ms, err := c.os.Members(rec.AnchorPID)
	return len(ms) > 0, err
}

// proveGroup is the identity proof of a group unit: the record's name carries its generation and nonce, the
// recorded anchor (pid and start stamp) is alive and leads the group whose number the handle records, and the
// signaller is not in it. An anchor that is not provably the recorded one proves nothing about the group number
// (it may have been reused): the group is then gone only if it holds nobody, and otherwise NOT PROVEN. Gone is
// always best effort.
func proveGroup(o PGroupOS, rec UnitRecord) (Proof, error) {
	fail := func(f string, a ...any) (Proof, error) {
		return Proof{}, fmt.Errorf("%w: %s", ErrNotProven, fmt.Sprintf(f, a...))
	}
	var h pgHandle
	if err := json.Unmarshal([]byte(rec.Handle), &h); err != nil || h.Pgid <= 0 || h.CreatorPID <= 0 || h.CreatorStamp == "" {
		return fail("the unit record has no process-group handle")
	}
	gen, nonce, ok := UnitGeneration(rec.Name)
	if !ok || gen != rec.Generation || nonce != rec.Nonce {
		return fail("the unit name does not carry the recorded generation and nonce")
	}
	if rec.AnchorPID <= 0 || rec.AnchorStamp == "" || h.Pgid != rec.AnchorPID {
		return fail("the unit record names no anchor that leads its group")
	}
	// The boot time is NOT evidence: kern.boottime moves when the clock is stepped, so a mismatch must not read as "the
	// machine rebooted, everything is gone". It only explains a failure; the anchor and the group decide.
	rebooted := false
	if boot, err := o.BootID(); err == nil && boot != rec.BootID {
		rebooted = true
	}
	if stamp, _, err := o.ProcInfo(rec.AnchorPID); err != nil || stamp != rec.AnchorStamp {
		ms, merr := o.Members(h.Pgid)
		switch {
		case merr != nil:
			return fail("the group cannot be listed: %v", merr)
		case len(ms) > 0 && rebooted:
			return fail("the recorded anchor is gone and the boot time differs, but the group number is held by %d process(es) that are not provably this unit's (after a reboot it may belong to another process: end them by hand)", len(ms))
		case len(ms) > 0:
			return fail("the recorded anchor is not alive with its recorded start time, so the group number is not tied to this unit")
		}
		return Proof{Gone: true, BestEffort: true, Note: "the process group no longer exists"}, nil
	}
	// The anchor was started under this unit's name: a live process with the right pid and start time that carries a
	// different name is not this unit's anchor, whatever the record says.
	if a0, err := o.Argv0(rec.AnchorPID); err != nil || a0 != rec.Name {
		return fail("the recorded anchor process was not started for this unit (its name is %q, %v)", a0, err)
	}
	if g, err := o.PgidOf(rec.AnchorPID); err != nil || g != h.Pgid {
		return fail("the recorded anchor does not lead the recorded group")
	}
	self, _, err := o.Self()
	if err != nil {
		return fail("the signaller's own identity is unknown: %v", err)
	}
	spg, _, err := o.SelfGroup()
	if err != nil || spg == h.Pgid {
		return fail("the group contains the signaller, or that cannot be excluded")
	}
	if ms, err := o.Members(h.Pgid); err != nil {
		return fail("the group cannot be listed: %v", err)
	} else {
		for _, m := range ms {
			if m == self {
				return fail("the group contains the signaller")
			}
		}
	}
	return Proof{Cg: &pgGroup{os: o, rec: rec, pgid: h.Pgid}, BestEffort: true}, nil
}

// pgGroup is the Group of a proven process group: one flat level (sub "").
type pgGroup struct {
	os   PGroupOS
	rec  UnitRecord
	pgid int
}

// Flat says the group has no sub-units: Stop cannot spare the commands of a begun operation inside it.
func (*pgGroup) Flat() bool { return true }

// SurvivorWhy is the reason EndUnit gives for a member that is still there after the kill.
func (*pgGroup) SurvivorWhy() string { return "stayed in the group after KILL" }

func (g *pgGroup) Identity() (CgroupIdentity, error) { return CgroupIdentity{ID: g.rec.CgroupID}, nil }
func (g *pgGroup) Subgroups() ([]string, error)      { return []string{""}, nil }

func (g *pgGroup) anchorAlive() bool {
	stamp, _, err := g.os.ProcInfo(g.rec.AnchorPID)
	return err == nil && stamp == g.rec.AnchorStamp
}

// Procs lists the members other than the anchor, which runs no user code and is ended by Rmdir.
func (g *pgGroup) Procs(string) ([]int, error) {
	ms, err := g.os.Members(g.pgid)
	var out []int
	for _, pid := range ms {
		if pid != g.rec.AnchorPID || !g.anchorAlive() {
			out = append(out, pid)
		}
	}
	return out, err
}

func (g *pgGroup) ProcInfo(_ string, pid int) (string, string, error) { return g.os.ProcInfo(pid) }

// Signal signals one member after re-checking that it is still in the group. macOS has no pid-stable handle, so a
// pid that exits and is reused between the check and the signal is a window this mechanism cannot close.
func (g *pgGroup) Signal(_ string, pid int, sig syscall.Signal) error {
	if pg, err := g.os.PgidOf(pid); err != nil || pg != g.pgid {
		return fmt.Errorf("process %d is not a member of the group", pid)
	}
	return g.os.SignalPid(pid, sig)
}

// Kill signals the whole group with SIGKILL while the anchor still pins its number, and sweeps the members that
// are left. With the anchor gone it signals only members re-checked one by one: the number could be someone else's.
func (g *pgGroup) Kill(sub string) error {
	var first error
	if g.anchorAlive() {
		first = g.os.SignalGroup(g.pgid, syscall.SIGKILL)
	}
	ms, err := g.Procs(sub)
	if err != nil && first == nil {
		first = err
	}
	for _, pid := range ms {
		if err := g.Signal(sub, pid, syscall.SIGKILL); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (g *pgGroup) Populated(string) (bool, error) {
	pids, err := g.Procs("")
	return len(pids) > 0, err
}

// Rmdir is the "unit is empty" step: with no member left it ends the anchor too and checks that the group is gone. A
// signal is delivered asynchronously on macOS, so the group is read again for a short, bounded time before it counts
// as still holding processes.
func (g *pgGroup) Rmdir(string) error {
	if p, err := g.Populated(""); err != nil || p {
		return fmt.Errorf("the group still holds processes (%v)", err)
	}
	if g.anchorAlive() {
		if err := g.os.SignalGroup(g.pgid, syscall.SIGKILL); err != nil {
			return err
		}
	}
	for i := 0; ; i++ {
		ms, err := g.os.Members(g.pgid)
		if err == nil && len(ms) == 0 {
			return nil
		}
		if i >= pgRmdirPolls {
			return fmt.Errorf("the group still holds %d process(es) after its anchor was ended (%v)", len(ms), err)
		}
		pgPause(pgRmdirPoll)
	}
}
func (g *pgGroup) Close() error { return nil }

func (c pgContainment) Create(ctx context.Context, a CreateArgs, gen uint64) (UnitRecord, error) {
	if c.os == nil {
		return UnitRecord{}, fmt.Errorf("%w: %v", ErrUnitNotCreated, errNoPGroupOS)
	}
	return createPGroup(ctx, c.os, a, gen)
}

// createPGroup makes the run's group unit: intent in the journal, the anchor, the record proven by the same checks
// recovery will use, then unit-bound in the journal and the key-free file. Any failure after the anchor started
// ends the anchor again and returns ErrUnitNotCreated.
func createPGroup(_ context.Context, o PGroupOS, a CreateArgs, gen uint64) (UnitRecord, error) {
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
	if err := a.Dir.fsys().MkdirAll(a.Dir.Dir, 0o700); err != nil {
		return UnitRecord{}, fmt.Errorf("%w: the run directory cannot be created: %v", ErrUnitNotCreated, err)
	}
	if a.Journal != nil {
		if err := a.Journal.Intent(gen, name, nonce, boot); err != nil {
			return UnitRecord{}, fmt.Errorf("%w: the witness refused the unit intent: %v", ErrUnitNotCreated, err)
		}
	}
	apid, astamp, err := o.StartAnchor(name)
	abandon := func(cause error) error {
		if kerr := o.KillAnchor(apid, astamp); kerr != nil {
			return fmt.Errorf("%w: %v; the anchor process %d could not be ended either (%v) and is still running", ErrUnitNotCreated, cause, apid, kerr)
		}
		return fmt.Errorf("%w: %v", ErrUnitNotCreated, cause)
	}
	if err != nil {
		if apid == 0 {
			return UnitRecord{}, fmt.Errorf("%w: the anchor process could not be started: %v", ErrUnitNotCreated, err)
		}
		return UnitRecord{}, abandon(fmt.Errorf("the anchor process was started but not identified: %v", err))
	}
	if g, err := o.PgidOf(apid); err != nil || g != apid {
		return UnitRecord{}, abandon(fmt.Errorf("the anchor is not the leader of a group of its own (%d, %v)", g, err))
	}
	_, sid, err := o.SelfGroup()
	if err != nil {
		return UnitRecord{}, abandon(fmt.Errorf("the session is unknown: %w", err))
	}
	h, _ := json.Marshal(pgHandle{Pgid: apid, Sid: sid, CreatorPID: cpid, CreatorStamp: cstamp})
	rec := UnitRecord{Kind: KindPGroup, BootID: boot, Name: name, CgroupID: idFromNonce(nonce), Nonce: nonce,
		Generation: gen, AnchorPID: apid, AnchorStamp: astamp, Handle: string(h)}
	p, err := proveGroup(o, rec)
	if err != nil {
		return UnitRecord{}, abandon(fmt.Errorf("the new unit fails its own identity proof: %w", err))
	}
	if p.Gone || p.Cg == nil {
		return UnitRecord{}, abandon(errors.New("the new unit is already gone"))
	}
	p.Cg.Close()
	if a.Journal != nil {
		if err := a.Journal.Bound(rec); err != nil {
			return UnitRecord{}, abandon(fmt.Errorf("the witness refused the unit binding: %w", err))
		}
	}
	if err := a.Dir.WriteUnitFile(rec); err != nil {
		return UnitRecord{}, abandon(fmt.Errorf("the unit record cannot be written: %w", err))
	}
	return rec, nil
}

// pgPlacer places commands inside a proven group unit; the group has no sub-units, so a ticket's command is placed
// in the one group (see jobPlacer).
type pgPlacer struct {
	os  PGroupOS
	rec UnitRecord
}

func (c pgContainment) Place(rec UnitRecord) (Placement, error) {
	if c.os == nil {
		return nil, fmt.Errorf("%w: %v", ErrNotPlaced, errNoPGroupOS)
	}
	return &pgPlacer{os: c.os, rec: rec}, nil
}

func (p *pgPlacer) Place(cmd *exec.Cmd) (func(), error) { return p.place(cmd) }

func (p *pgPlacer) PlaceTicket(_ uint64, cmd *exec.Cmd) (func(), error) { return p.place(cmd) }

func (p *pgPlacer) PlaceLeased(cmd *exec.Cmd) error {
	_, err := p.place(cmd)
	return err
}

func (p *pgPlacer) place(cmd *exec.Cmd) (func(), error) {
	if cmd == nil || cmd.Process != nil {
		return nil, fmt.Errorf("%w: the command is nil or already started", ErrNotPlaced)
	}
	proof, err := proveGroup(p.os, p.rec)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotPlaced, err)
	}
	if proof.Gone {
		return nil, fmt.Errorf("%w: the unit is gone", ErrNotPlaced)
	}
	proof.Cg.Close()
	var h pgHandle
	if err := json.Unmarshal([]byte(p.rec.Handle), &h); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotPlaced, err)
	}
	if _, sid, err := p.os.SelfGroup(); err != nil || sid != h.Sid {
		return nil, fmt.Errorf("%w: the caller is not in the session of the group (a process cannot join a group across sessions)", ErrNotPlaced)
	}
	if err := p.os.Prepare(cmd, h.Pgid); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotPlaced, err)
	}
	return func() {}, nil
}

// parseProcArgs2 reads argv[0] out of a kern.procargs2 answer: an int32 argc, the executable path, NUL padding, then
// the argument strings.
func parseProcArgs2(b []byte) (string, error) {
	bad := errors.New("the process arguments are not in the expected form")
	if len(b) < 5 || (b[0]|b[1]|b[2]|b[3]) == 0 {
		return "", bad
	}
	i := 4
	for i < len(b) && b[i] != 0 { // the executable path
		i++
	}
	for i < len(b) && b[i] == 0 { // its padding
		i++
	}
	j := i
	for j < len(b) && b[j] != 0 {
		j++
	}
	if j == i || j >= len(b) {
		return "", bad
	}
	return string(b[i:j]), nil
}
