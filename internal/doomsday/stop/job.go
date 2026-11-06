package stop

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
)

// The Windows unit: a NAMED Job Object with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE. The CLI that creates it exits, so a
// separate ANCHOR process (a blocking system binary, run detached) is put in the job and given a duplicate of the
// job handle: the job lives as long as the anchor does and dies with it. Commands are placed by creating them with
// the anchor as their parent process (PROC_THREAD_ATTRIBUTE_PARENT_PROCESS, which makes a child inherit the parent's
// job): they are BORN in the job, the analogue of clone3 into a cgroup. The mechanism is BEST EFFORT: it cannot show
// that no process left the group (a service started through the service manager, a COM server), so every end of such
// a unit is UnitBestEffort and never "stopped" bare. All Windows calls are behind JobOS; this file is portable, so
// the logic is tested on Linux with a fake and only job_windows.go touches the OS.

// ErrJobAbsent is what JobOS.OpenJob returns for a name with no job.
var ErrJobAbsent = errors.New("stop: no such job object")

// Job is an opened Job Object. Every operation after the identity check goes through this handle.
type Job interface {
	Pids() ([]int, error)
	Contains(pid int) (bool, error)
	Terminate() error // TerminateJobObject: ends every member
	// TerminateMember ends one process through ONE handle: the pid is checked to be a member through that same
	// handle before it is terminated, so a reused pid is never touched.
	TerminateMember(pid int) error
	Close() error
}

// JobOS is the seam over the Windows API (job_windows.go in production, a fake in tests).
type JobOS interface {
	BootID() (string, error)
	// Self is the pid and process start stamp of the caller.
	Self() (pid int, stamp string, err error)
	// StartAnchor starts the detached anchor process.
	StartAnchor() (pid int, stamp string, err error)
	// KillAnchor ends the anchor if pid still has that start stamp (abandoning a unit that was not recorded).
	KillAnchor(pid int, stamp string) error
	// CreateJob makes a new named job with KILL_ON_JOB_CLOSE; a name that exists is an error.
	CreateJob(name string) (Job, error)
	// OpenJob opens an existing job by name; ErrJobAbsent when there is none.
	OpenJob(name string) (Job, error)
	// Adopt puts the anchor in the job and duplicates the job handle into the anchor, so the job outlives the caller.
	Adopt(j Job, anchorPID int, anchorStamp string) error
	ProcInfo(pid int) (stamp, comm string, err error)
	// ParentFor makes cmd's child be created with the anchor as parent (so it is born in the job). release closes the
	// handle it holds; call it after the command ended.
	ParentFor(cmd *exec.Cmd, anchorPID int, anchorStamp string) (release func(), err error)
}

// jobHandle is UnitRecord.Handle of a KindJobObject unit: the job's name and the creator's identity (pid and
// start stamp); the anchor's identity is the record's AnchorPID/AnchorStamp.
type jobHandle struct {
	Job          string `json:"job"`
	CreatorPID   int    `json:"creator_pid"`
	CreatorStamp string `json:"creator_stamp"`
}

func jobName(unitName string) string { return strings.TrimSuffix(unitName, ".service") }

// idFromNonce is the unit's numeric identity (UnitRecord.CgroupID): the witness keys its unit-ended line by it, so
// two units of one generation must never share one.
func idFromNonce(nonce string) uint64 {
	b, err := hex.DecodeString(nonce)
	if err != nil || len(b) < 8 {
		return 0
	}
	return binary.BigEndian.Uint64(b[:8]) | 1
}

type jobContainment struct{ os JobOS }

func newJobContainment(Kernel) Containment { return jobContainment{os: realJobOS()} }

func (jobContainment) Kind() string { return KindJobObject }

func (c jobContainment) Probe() error {
	if c.os == nil {
		return fmt.Errorf("%w: the Job Object mechanism is only available on Windows", ErrContainmentUnavailable)
	}
	return RunProbe(append(jobSelfTestSteps(c.os), windowsHostSteps()...))
}

// jobSelfTestSteps proves the mechanism works here: a job is created, an anchor is put in it, a command is born in
// it and everything ends with the job.
func jobSelfTestSteps(o JobOS) []ProbeStep {
	return []ProbeStep{{"job object create, anchor adopt and kill", func() error { return jobSelfTest(o) }}}
}

func jobSelfTest(o JobOS) error {
	pid, stamp, err := o.StartAnchor()
	if err != nil {
		return err
	}
	defer o.KillAnchor(pid, stamp)
	var nb [8]byte
	if _, err := rand.Read(nb[:]); err != nil {
		return err
	}
	name := fmt.Sprintf("monoagent-probe-%d-%x", pid, nb)
	j, err := o.CreateJob(name)
	if err != nil {
		return err
	}
	defer j.Close()
	if err := o.Adopt(j, pid, stamp); err != nil {
		return err
	}
	if in, err := j.Contains(pid); err != nil || !in {
		return fmt.Errorf("the anchor is not in the job (%v)", err)
	}
	if err := j.Terminate(); err != nil {
		return err
	}
	if pids, err := j.Pids(); err != nil || len(pids) != 0 {
		return fmt.Errorf("the job still holds %d process(es) after the kill (%v)", len(pids), err)
	}
	return nil
}

func (c jobContainment) Prove(rec UnitRecord) (Proof, error) {
	if c.os == nil {
		return Proof{}, fmt.Errorf("%w: the Job Object mechanism is only available on Windows", ErrNotProven)
	}
	return proveJob(c.os, rec)
}

func (c jobContainment) Present(rec UnitRecord) (bool, error) {
	if c.os == nil {
		return false, errors.New("the Job Object mechanism is only available on Windows")
	}
	j, err := c.os.OpenJob(jobName(rec.Name))
	switch {
	case err == nil:
		j.Close()
		return true, nil
	case errors.Is(err, ErrJobAbsent):
		return false, nil
	}
	return false, err
}

// proveJob is the identity proof of a job unit: the recorded name carries the generation and nonce, the job of that
// name exists, the recorded anchor (pid and start stamp) is alive and is a member of it, and the signaller is not.
// A job that is absent is gone as far as the mechanism can tell (BestEffort).
func proveJob(o JobOS, rec UnitRecord) (Proof, error) {
	fail := func(f string, a ...any) (Proof, error) {
		return Proof{}, fmt.Errorf("%w: %s", ErrNotProven, fmt.Sprintf(f, a...))
	}
	var h jobHandle
	if err := json.Unmarshal([]byte(rec.Handle), &h); err != nil || h.CreatorPID <= 0 || h.CreatorStamp == "" {
		return fail("the unit record has no job handle")
	}
	gen, nonce, ok := UnitGeneration(rec.Name)
	if !ok || gen != rec.Generation || nonce != rec.Nonce || h.Job != jobName(rec.Name) {
		return fail("the unit name does not carry the recorded generation and nonce")
	}
	if rec.AnchorPID <= 0 || rec.AnchorStamp == "" {
		return fail("the unit record names no anchor process")
	}
	j, err := o.OpenJob(h.Job)
	if errors.Is(err, ErrJobAbsent) {
		return Proof{Gone: true, BestEffort: true, Note: "the job object no longer exists"}, nil
	}
	if err != nil {
		return fail("the job object cannot be opened: %v", err)
	}
	if stamp, _, err := o.ProcInfo(rec.AnchorPID); err != nil || stamp != rec.AnchorStamp {
		j.Close()
		return fail("the recorded anchor process is not alive with its recorded start time")
	}
	if in, err := j.Contains(rec.AnchorPID); err != nil || !in {
		j.Close()
		return fail("the recorded anchor process is not a member of the job")
	}
	self, _, err := o.Self()
	if err != nil {
		j.Close()
		return fail("the signaller's own identity is unknown: %v", err)
	}
	if in, err := j.Contains(self); err != nil || in {
		j.Close()
		return fail("the job contains the signaller, or that cannot be excluded")
	}
	return Proof{Cg: &jobGroup{os: o, job: j, rec: rec}, BestEffort: true}, nil
}

// jobGroup is the Group of a proven job: one flat level (sub "").
type jobGroup struct {
	os  JobOS
	job Job
	rec UnitRecord
}

// Flat says the group has no sub-units: Stop cannot spare the commands of a begun operation inside it.
func (*jobGroup) Flat() bool { return true }

func (g *jobGroup) Identity() (CgroupIdentity, error) {
	return CgroupIdentity{ID: g.rec.CgroupID}, nil
}
func (g *jobGroup) Subgroups() ([]string, error) { return []string{""}, nil }

// Procs lists the members other than the anchor: like the Linux unit's own anchor, it runs no user code, belongs to
// no registry row and is ended by the unit's stop (the job's kill, or StopUnit), not counted as a member.
func (g *jobGroup) Procs(string) ([]int, error) {
	pids, err := g.job.Pids()
	var out []int
	for _, pid := range pids {
		if pid == g.rec.AnchorPID {
			if stamp, _, perr := g.os.ProcInfo(pid); perr == nil && stamp == g.rec.AnchorStamp {
				continue
			}
		}
		out = append(out, pid)
	}
	return out, err
}
func (g *jobGroup) ProcInfo(_ string, pid int) (string, string, error) {
	return g.os.ProcInfo(pid)
}

// Signal ends the process for SIGKILL; Windows has no graceful signal for an arbitrary process, so SIGTERM is not
// delivered and the escalation reaches the kill after its window.
func (g *jobGroup) Signal(_ string, pid int, sig syscall.Signal) error {
	if sig != syscall.SIGKILL {
		return nil
	}
	return g.job.TerminateMember(pid)
}
func (g *jobGroup) Kill(string) error { return g.job.Terminate() }
func (g *jobGroup) Populated(string) (bool, error) {
	pids, err := g.Procs("")
	return len(pids) > 0, err
}

// Rmdir is the "unit is empty" check: a job object is not removed by us, it vanishes when its last handle closes.
func (g *jobGroup) Rmdir(string) error {
	if p, err := g.Populated(""); err != nil || p {
		return fmt.Errorf("the job still holds processes (%v)", err)
	}
	return nil
}
func (g *jobGroup) Close() error { return g.job.Close() }

func (c jobContainment) Create(ctx context.Context, a CreateArgs, gen uint64) (UnitRecord, error) {
	if c.os == nil {
		return UnitRecord{}, fmt.Errorf("%w: the Job Object mechanism is only available on Windows", ErrUnitNotCreated)
	}
	return createJob(ctx, c.os, a, gen)
}
