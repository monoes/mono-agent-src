package doomsday

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/monoes/mono-agent/internal/doomsday/stop"
	"github.com/monoes/mono-agent/internal/doomsday/store"
	"github.com/monoes/mono-agent/internal/dynorg"
)

// The production end of note 13.3: the merge of a conductor goes through the gate and journal of the profile's
// open run, and a later start revisits merge members a dead host left, with the containment proof as its "gone".

// MergeJournal reads the merge rows of db's profile, each generation's chain verified by the store.
func MergeJournal(db *sql.DB, st *store.SQLStore) dynorg.MergeJournal {
	return dynorg.SQLMergeJournal{DB: db, VerifyAudit: st.VerifyAudit}
}

// MergeFor is dynorg.Config.DoomsdayFor for this host: nil while no run is open (merges are as they were), the
// merge connection of the open run's gate otherwise. A run that cannot be read, or a missing journal, is an
// error: the merge is refused, never run ungated.
func (g *HostGate) MergeFor(ctx context.Context) (*dynorg.DoomsdayMerge, error) {
	gate, run, err := g.currentRun(ctx)
	if err != nil || gate == nil {
		return nil, err
	}
	if g.Journal == nil {
		return nil, fmt.Errorf("the merge journal is not wired")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.merges == nil {
		g.merges = map[int64]*dynorg.DoomsdayMerge{}
	}
	dd := g.merges[run.Generation]
	if dd == nil {
		pl, err := stop.NewPlacer(g.Kernel, g.RunDir, uint64(run.Generation))
		if err != nil {
			return nil, err
		}
		dd = &dynorg.DoomsdayMerge{Gate: gate, Members: g.Store, Journal: g.Journal, Profile: g.Profile, Generation: run.Generation, Place: pl.Place}
		g.merges[run.Generation] = dd
	}
	return dd, nil
}

// MergeRecovery is the profile's recovery of begun merges over this wiring; nil when the wiring has no database
// (a Wiring built by a test that does not exercise merges).
func (w Wiring) MergeRecovery() *dynorg.MergeRecovery {
	if w.DB == nil {
		return nil
	}
	return &dynorg.MergeRecovery{
		Profile: w.Profile, Members: w.Chained(), Journal: MergeJournal(w.DB, w.Store),
		Gone:     dynorg.ContainmentGone(w.Kernel, w.RunDir, nil),
		Recorded: dynorg.TaskStoreRecorded(w.DB, w.Profile),
		PlaceFor: w.placementOfOpenRun,
	}
}

// placementOfOpenRun is MergeRecovery.PlaceFor: the open run's unit placement, nil while no run is open (a
// recovery before the run exists works as it always did). A run whose unit cannot be placed in is an error.
func (w Wiring) placementOfOpenRun(ctx context.Context) (dynorg.GitPlacer, error) {
	runs, err := w.Store.OpenRuns(ctx, w.Profile)
	if err != nil || len(runs) == 0 {
		return nil, err
	}
	pl, err := stop.NewPlacer(w.Kernel, w.RunDir, uint64(runs[0].Generation))
	if err != nil {
		return nil, err
	}
	return pl.Place, nil
}

// recoverMerges is the call from the start and stop paths: it revisits every merge member of the profile's open
// runs (containment proof first, git only after it), then lets the caller go on. An error is returned so a start
// can refuse; a stop ignores it (stopping must stay possible) and the survivors are reported by the order itself.
func (w Wiring) recoverMerges(ctx context.Context) error {
	r := w.MergeRecovery()
	if r == nil {
		return nil
	}
	runs, err := w.Store.OpenRuns(ctx, w.Profile)
	if err != nil {
		return err
	}
	var gens []int64
	for _, run := range runs {
		gens = append(gens, run.Generation)
	}
	_, err = r.Recover(ctx, "", gens...)
	return err
}
