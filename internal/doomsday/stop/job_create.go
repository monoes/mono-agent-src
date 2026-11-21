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
	"runtime"
	"sync"
)

// createJob makes the run's job unit: intent in the journal, the anchor, the named job, the anchor adopted into it,
// the record proven by the same checks recovery will use, then unit-bound in the journal and the key-free file. Any
// failure after the anchor started ends the anchor again (the job dies with it) and returns ErrUnitNotCreated.
func createJob(ctx context.Context, o JobOS, a CreateArgs, gen uint64) (UnitRecord, error) {
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
	sess, err := o.Session()
	if err != nil {
		return UnitRecord{}, fmt.Errorf("%w: the logon session is unknown: %v", ErrUnitNotCreated, err)
	}
	if err := a.Dir.fsys().MkdirAll(a.Dir.Dir, 0o700); err != nil {
		return UnitRecord{}, fmt.Errorf("%w: the run directory cannot be created: %v", ErrUnitNotCreated, err)
	}
	if a.Journal != nil {
		if err := a.Journal.Intent(gen, name, nonce, boot); err != nil {
			return UnitRecord{}, fmt.Errorf("%w: the witness refused the unit intent: %v", ErrUnitNotCreated, err)
		}
	}
	apid, astamp, err := o.StartAnchor()
	if err != nil {
		return UnitRecord{}, fmt.Errorf("%w: the anchor process could not be started: %v", ErrUnitNotCreated, err)
	}
	abandon := func(cause error) error {
		if kerr := o.KillAnchor(apid, astamp); kerr != nil {
			return fmt.Errorf("%w: %v; the anchor process %d could not be ended either (%v) and is still running", ErrUnitNotCreated, cause, apid, kerr)
		}
		return fmt.Errorf("%w: %v", ErrUnitNotCreated, cause)
	}
	j, err := o.CreateJob(jobName(name))
	if err != nil {
		return UnitRecord{}, abandon(fmt.Errorf("the job object could not be created: %w", err))
	}
	err = o.Adopt(j, apid, astamp)
	j.Close()
	if err != nil {
		return UnitRecord{}, abandon(fmt.Errorf("the anchor could not be put in the job: %w", err))
	}
	h, _ := json.Marshal(jobHandle{Job: jobName(name), CreatorPID: cpid, CreatorStamp: cstamp, Session: sess})
	rec := UnitRecord{Kind: KindJobObject, BootID: boot, Name: name, CgroupID: idFromNonce(nonce), Nonce: nonce,
		Generation: gen, AnchorPID: apid, AnchorStamp: astamp, Handle: string(h)}
	p, err := proveJob(o, rec)
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

// jobPlacer places commands inside a proven job unit. A Job Object has no sub-units, so a ticket's command is placed
// in the one job: Stop cannot spare a begun operation's command (EndUnit leaves a flat group alone while one is
// finishing).
type jobPlacer struct {
	os  JobOS
	rec UnitRecord
}

func (c jobContainment) Place(rec UnitRecord) (Placement, error) {
	if c.os == nil {
		return nil, fmt.Errorf("%w: the Job Object mechanism is only available on Windows", ErrNotPlaced)
	}
	return &jobPlacer{os: c.os, rec: rec}, nil
}

func (p *jobPlacer) Place(cmd *exec.Cmd) (func(), error) { return p.place(cmd) }

func (p *jobPlacer) PlaceTicket(_ uint64, cmd *exec.Cmd) (func(), error) { return p.place(cmd) }

func (p *jobPlacer) PlaceLeased(cmd *exec.Cmd) error {
	release, err := p.place(cmd)
	if err != nil {
		return err
	}
	runtime.AddCleanup(cmd, func(f func()) { f() }, release)
	return nil
}

// PlaceCommit places a coder turn without the anchor as its parent (#1723): the bound launcher passes its credential
// pipe as an inherited handle, which with the anchor as parent would have to sit in the anchor, a process any process
// of the same user can take handles from. The command is created suspended as the caller's own child, so its access
// lists, handles and environment stay exactly as the caller built them; commit puts it in the job through the job
// handle the identity proof opened (it carries the assign right), checks it is a member, and only then resumes it.
// That is "born in the job" as much as creating it under the anchor is: a suspended process has run no instruction,
// so nothing it would start, or do, happens outside the job. A process that cannot be put in the job is ended before
// it ran, and commit fails (the start must be reported as failed). The proof's job handle is held until commit.
//
// The turn would inherit the caller's own job, and Windows only lets a process join a second job nested under the
// one it is in: a run started from another job chain than the coder host (another terminal, the desktop app, an ssh
// session) would refuse every turn. So the turn is created out of the caller's jobs (CREATE_BREAKAWAY_FROM_JOB) when
// they allow it, decided HERE, before the start, by asking Windows with a throwaway child (BreakawayAllowed), and
// never by starting the turn twice: a command starts once, and the one this commit holds is the one the caller starts,
// so the caller's checks run on exactly what is started. The trade-off: such a turn leaves the coder host's outer job
// (whatever that job would do to it on close no longer applies); the run's job is what contains and ends it. A
// caller whose job forbids breakaway gets the turn in its job, and a run's job outside that chain refuses it.
func (p *jobPlacer) PlaceCommit(cmd *exec.Cmd) (func() error, error) {
	if cmd == nil || cmd.Process != nil {
		return nil, fmt.Errorf("%w: the command is nil or already started", ErrNotPlaced)
	}
	proof, err := proveJob(p.os, p.rec)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotPlaced, err)
	}
	if proof.Gone {
		return nil, fmt.Errorf("%w: the unit is gone", ErrNotPlaced)
	}
	g := proof.Cg.(*jobGroup)
	breakaway, err := p.os.BreakawayAllowed()
	if err == nil {
		err = p.os.Suspend(cmd, breakaway)
	}
	if err != nil {
		g.Close()
		return nil, fmt.Errorf("%w: %v", ErrNotPlaced, err)
	}
	var once sync.Once
	var cerr error
	return func() error {
		once.Do(func() {
			defer g.Close()
			if cmd.Process == nil || cmd.ProcessState != nil {
				// not started, or the start failed after the process existed and the caller already ended and reaped it
				// (the bound launcher does when it cannot hold the leader): there is no process to place or resume
				return
			}
			defer func() {
				if r := recover(); r != nil { // never left suspended, or running, outside the job
					_ = cmd.Process.Kill()
					cerr = fmt.Errorf("%w: putting the process in the run's job panicked, so it was ended: %v", ErrNotPlaced, r)
				}
			}()
			if err := p.os.AssignResume(g.job, cmd); err != nil && !breakaway {
				cerr = fmt.Errorf("%w: the process could not be put in the run's job before it ran, so it was ended (%v): the coder host runs in a job that does not let its children leave it, so the turn stayed in that job, and the run's job is not nested under it; start the open-ended run from the same environment as the coder host (the same terminal, session or app), or run the coder host outside that job", ErrNotPlaced, err)
			} else if err != nil {
				cerr = fmt.Errorf("%w: the process could not be put in the run's job before it ran, so it was ended: %v", ErrNotPlaced, err)
			}
		})
		return cerr
	}, nil
}

func (p *jobPlacer) place(cmd *exec.Cmd) (func(), error) {
	if cmd == nil || cmd.Process != nil {
		return nil, fmt.Errorf("%w: the command is nil or already started", ErrNotPlaced)
	}
	proof, err := proveJob(p.os, p.rec)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotPlaced, err)
	}
	if proof.Gone {
		return nil, fmt.Errorf("%w: the unit is gone", ErrNotPlaced)
	}
	defer proof.Cg.Close() // the proof's handle is only for the check; the child is born through the anchor
	release, err := p.os.ParentFor(cmd, p.rec.AnchorPID, p.rec.AnchorStamp)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotPlaced, err)
	}
	return release, nil
}
