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
	h, _ := json.Marshal(jobHandle{Job: jobName(name), CreatorPID: cpid, CreatorStamp: cstamp})
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
