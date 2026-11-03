package stop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ErrCannotStartWork: the host could not make itself durably dirty, so it must not start work.
var ErrCannotStartWork = errors.New("stop: the host could not be marked dirty durably; no work is started")

func (r RunDir) cleanPath() string      { return filepath.Join(r.Dir, "clean-shutdown") }
func (r RunDir) failClosedPath() string { return filepath.Join(r.Dir, "fail-closed") }

// StartDirty makes the host dirty at start, durably: it reads the clean record, then removes it and fsyncs the
// removal and its parent directory before any work is admitted. If that fails the host must not start work
// (a clean record left in place by a crash would let the next start read an unclean loss as clean).
// wasClean is false when there was no clean record or a fail-closed record exists.
func (r RunDir) StartDirty() (wasClean bool, err error) {
	if err := r.fsys().MkdirAll(r.Dir, 0o700); err != nil {
		return false, fmt.Errorf("%w: %v", ErrCannotStartWork, err)
	}
	_, cerr := r.fsys().ReadFile(r.cleanPath())
	_, ferr := r.fsys().ReadFile(r.failClosedPath())
	wasClean = cerr == nil && ferr != nil
	if cerr == nil {
		if err := r.fsys().Remove(r.cleanPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("%w: %v", ErrCannotStartWork, err)
		}
		r.Hook.at("clean.removed")
		if err := r.fsys().SyncDir(r.Dir); err != nil {
			r.Hook.at("fsync.dir")
			return false, fmt.Errorf("%w: %v", ErrCannotStartWork, err)
		}
		r.Hook.at("fsync.dir")
	}
	return wasClean, nil
}

// AckRecovery removes the fail-closed record once recovery has reconciled every generation it covered.
func (r RunDir) AckRecovery() error {
	if err := r.fsys().Remove(r.failClosedPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return r.fsys().SyncDir(r.Dir)
}

// Shutdown is the orderly exit. It never writes "clean" while a stop was requested and not recorded: it writes
// the durable fail-closed record instead. If that cannot be written the host stays dirty (the clean record was
// removed at start), so the next start reads the loss as unclean and refuses every previously running run.
func (r RunDir) Shutdown(flag *UnrecordedFlag) error {
	if flag != nil && flag.IsSet() {
		return writeDurable(r.fsys(), r.Dir, r.failClosedPath(), []byte("fail-closed\n"))
	}
	return writeDurable(r.fsys(), r.Dir, r.cleanPath(), []byte("clean\n"))
}

// RecoveryReport is what Recover did.
type RecoveryReport struct {
	WasClean  bool
	Resumed   []Report // generations whose Stop was resumed or forced
	Untouched []uint64 // running generations left alone (clean start, no marker)
	Unbound   []string // containment units found by name only: reported, never signalled
}

// Recoverer runs recovery on every host start, before the host accepts any work.
type Recoverer struct {
	Dir     RunDir
	Store   RunStore
	Stopper *Stopper
	// Unbound lists the run units present on the machine that have no durable binding record (note 13.2
	// section 8); they are reported and left alone: a name pattern and a UID are not authority to signal.
	Unbound func(ctx context.Context) []string
}

// Recover makes the host dirty durably (failing closed), then: any generation with a marker, or a non-running
// state, resumes its Stop from the recorded step; after an unclean loss every previously running generation is
// refused and reconciled, never resumed.
func (rc Recoverer) Recover(ctx context.Context) (RecoveryReport, error) {
	var rep RecoveryReport
	wasClean, err := rc.Dir.StartDirty()
	if err != nil {
		return rep, err
	}
	rep.WasClean = wasClean
	runs, err := rc.Store.OpenRuns(ctx)
	if err != nil {
		return rep, fmt.Errorf("stop: recovery cannot read the runs, so none is resumed: %w", err)
	}
	for _, run := range runs {
		marker := rc.Dir.ReadMarker(run.Generation, run.CharterID)
		switch {
		case marker.Stops() || run.State != StateRunning:
			rep.Resumed = append(rep.Resumed, rc.Stopper.Stop(ctx, run.Generation, run.CharterID, "recovery: resume stop"))
		case !wasClean:
			rep.Resumed = append(rep.Resumed, rc.Stopper.Stop(ctx, run.Generation, run.CharterID, "recovery: unclean host loss"))
		default:
			rep.Untouched = append(rep.Untouched, run.Generation)
		}
	}
	if rc.Unbound != nil {
		rep.Unbound = rc.Unbound(ctx)
	}
	return rep, nil
}

// EnforceHung is the crash-only rule of note 13.1 section 5: a start call that does not return cannot be
// cancelled, so while a STARTING ticket (a continuation starter included) is older than T_hung the host
// terminates itself through exit, which writes the fail-closed record and exits. It returns when the live set
// has no starter or after exit was called.
func EnforceHung(ctx context.Context, g *Gate, clk Clock, tick time.Duration, exit func(starters []uint64)) {
	if tick == 0 {
		tick = time.Second
	}
	for {
		if len(g.Outcome().Starters) == 0 {
			return
		}
		if h := g.HungStarters(); len(h) > 0 {
			exit(h)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-clk.After(tick):
		}
	}
}
