package stop

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
)

// UnitHost is the seam over the service manager: it starts and stops the transient service whose cgroup is the
// run's containment unit. The production implementation is SystemHost (a systemd user manager with Delegate=yes).
type UnitHost interface {
	// StartUnit starts a transient service of that name with delegation, in the manager's root slice (so its
	// cgroup is a direct child of the delegated root), and an anchor process that blocks until it is stopped.
	StartUnit(ctx context.Context, name, description string) error
	// StopUnit stops the service (the convenience stop; it is never the proof that a unit is gone).
	StopUnit(ctx context.Context, name string) error
}

// ErrUnitNotCreated means no unit exists for the run: the mode does not start ("no unit, no run").
var ErrUnitNotCreated = errors.New("stop: the containment unit could not be created and recorded")

// UnitCreator creates the run's containment unit at entry, before the charter becomes effective and before any
// member can start (note 13.2 sections 3 and 4). It records the unit durably before returning, with the facts
// the identity proof checks, so a sweep never has to bind a unit by name pattern and owner UID alone (13.2 P1).
type UnitCreator struct {
	K    Kernel
	Dir  RunDir
	Host UnitHost
	Rand io.Reader // nil: crypto/rand
}

func (c UnitCreator) random() io.Reader {
	if c.Rand != nil {
		return c.Rand
	}
	return rand.Reader
}

// Create starts the unit of the generation, proves it with the same checks recovery will use (so a record that
// would later fail its own proof is refused now), and writes the key-free record create-exclusive and fsynced.
// Any failure after the service started stops the service again and returns ErrUnitNotCreated.
func (c UnitCreator) Create(ctx context.Context, gen uint64) (UnitRecord, error) {
	if c.K == nil || c.Host == nil {
		return UnitRecord{}, fmt.Errorf("%w: no containment kernel or service manager is wired", ErrUnitNotCreated)
	}
	nb := make([]byte, 16)
	if _, err := io.ReadFull(c.random(), nb); err != nil {
		return UnitRecord{}, fmt.Errorf("%w: no randomness: %v", ErrUnitNotCreated, err)
	}
	nonce := hex.EncodeToString(nb)
	name := fmt.Sprintf("monoagent-run-%d-%s.service", gen, nonce)
	boot, err := c.K.BootID()
	if err != nil {
		return UnitRecord{}, fmt.Errorf("%w: the boot id is unreadable: %v", ErrUnitNotCreated, err)
	}
	root, err := c.K.DelegatedRoot()
	if err != nil {
		return UnitRecord{}, fmt.Errorf("%w: the delegated root is unknown: %v", ErrUnitNotCreated, err)
	}
	if err := c.Dir.fsys().MkdirAll(c.Dir.Dir, 0o700); err != nil {
		return UnitRecord{}, fmt.Errorf("%w: the run directory cannot be created: %v", ErrUnitNotCreated, err)
	}
	if err := c.Host.StartUnit(ctx, name, fmt.Sprintf("monoagent open-ended run, generation %d", gen)); err != nil {
		return UnitRecord{}, fmt.Errorf("%w: the service manager refused: %v", ErrUnitNotCreated, err)
	}
	rec := UnitRecord{Kind: "cgroup2", BootID: boot, Name: name, CgroupPath: filepath.Join(root, name),
		OwnerUID: c.K.CurrentUID(), Nonce: nonce, Generation: gen}
	if err := c.bind(&rec); err != nil {
		return UnitRecord{}, c.abandon(ctx, name, err)
	}
	if err := c.Dir.WriteUnitFile(rec); err != nil {
		return UnitRecord{}, c.abandon(ctx, name, fmt.Errorf("the unit record cannot be written: %w", err))
	}
	return rec, nil
}

// bind opens the new cgroup, reads its identity and its single anchor process, and runs the identity proof.
func (c UnitCreator) bind(rec *UnitRecord) error {
	cg, err := c.K.OpenCgroup(rec.CgroupPath)
	if err != nil {
		return fmt.Errorf("the new unit's cgroup cannot be opened: %w", err)
	}
	defer cg.Close()
	id, err := cg.Identity()
	if err != nil {
		return fmt.Errorf("the new unit's cgroup cannot be inspected: %w", err)
	}
	rec.CgroupID = id.ID
	pids, err := cg.Procs("")
	if err != nil || len(pids) != 1 {
		return fmt.Errorf("the new unit does not hold exactly its anchor process (%d found, err %v)", len(pids), err)
	}
	stamp, _, err := cg.ProcInfo("", pids[0])
	if err != nil {
		return fmt.Errorf("the anchor process cannot be identified: %w", err)
	}
	rec.AnchorPID, rec.AnchorStamp = pids[0], stamp
	p, err := ProveUnit(c.K, *rec)
	if err != nil {
		return fmt.Errorf("the new unit fails its own identity proof: %w", err)
	}
	if p.Gone || p.Cg == nil {
		return errors.New("the new unit is already gone")
	}
	return p.Cg.Close()
}

// abandon stops a service this creator started and could not record, and says so when the stop fails too: the
// service then still runs and its name is in the error.
func (c UnitCreator) abandon(ctx context.Context, name string, cause error) error {
	if serr := c.Host.StopUnit(ctx, name); serr != nil {
		return fmt.Errorf("%w: %v; the service %s could not be stopped either (%v) and is still running", ErrUnitNotCreated, cause, name, serr)
	}
	return fmt.Errorf("%w: %v", ErrUnitNotCreated, cause)
}

// Discard stops the unit of a run that was never entered (the store refused the entry after the unit existed) and
// removes its record, so the generation's next attempt can create its own. It only acts on a record this creator
// wrote: the file is read back and must name the same unit.
func (c UnitCreator) Discard(ctx context.Context, rec UnitRecord) error {
	if c.Host == nil {
		return errors.New("stop: no service manager is wired")
	}
	if got, ok, err := c.Dir.ReadUnitFile(rec.Generation); err != nil || !ok || got != rec {
		return errors.New("stop: the unit record on disk is not the one being discarded; nothing was stopped")
	}
	if err := c.Host.StopUnit(ctx, rec.Name); err != nil {
		return fmt.Errorf("stop: the unit %s could not be stopped: %w", rec.Name, err)
	}
	return c.Dir.fsys().Remove(c.Dir.UnitPath(rec.Generation))
}
