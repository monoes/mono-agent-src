package dynorg

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/monoes/mono-agent/internal/doomsday/stop"
	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// ContainmentGone is the "is any process of the operation still alive" question of note 13.3 section 5, answered
// by the stop package's identity proof of the run's containment unit. A merge's git commands are processes of the
// host inside the run's unit, so the unit being gone or empty is the proof. Anything it cannot prove (no unit
// record, a failed identity check, an unreadable cgroup) is "not gone": unknown is not gone, and the member stays
// live.
//
// witness may be nil; it adds the authenticated unit records to the key-free file of the run directory.
func ContainmentGone(k stop.Kernel, dir stop.RunDir, witness func(gen uint64) []stop.UnitRecord) func(store.Member) (bool, error) {
	return func(m store.Member) (bool, error) {
		if k == nil {
			return false, errors.New("no containment kernel is wired, so no unit can be proven")
		}
		gen := uint64(m.Generation)
		var recs []stop.UnitRecord
		if witness != nil {
			recs = append(recs, witness(gen)...)
		}
		rec, ok, err := dir.ReadUnitFile(gen)
		if err != nil {
			return false, err
		}
		if ok {
			recs = append(recs, rec)
		}
		if len(recs) == 0 {
			return false, fmt.Errorf("generation %d has no recorded containment unit, so its processes cannot be proven gone", gen)
		}
		for _, r := range recs {
			if gone, err := unitEmpty(k, r); err != nil || !gone {
				return false, err
			}
		}
		return true, nil
	}
}

// unitEmpty is true when the unit is PROVEN to have no process (stop.ProvenEmpty: a reboot, an absent cgroup, or a
// proven cgroup that holds only its anchor); a best-effort platform's answer is never proof.
func unitEmpty(k stop.Kernel, rec stop.UnitRecord) (bool, error) { return stop.ProvenEmpty(k, rec) }

// TaskStoreRecorded answers whether the task store holds the merge record of a branch (the "merge" artifact that
// org_merge writes). It only reads.
func TaskStoreRecorded(db *sql.DB, profile string) func(ctx context.Context, branch string) (bool, error) {
	return func(ctx context.Context, branch string) (bool, error) {
		var n int
		err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_artifacts a JOIN tasks t ON t.id = a.task_id WHERE t.profile_id = ? AND a.kind = 'merge' AND a.ref = ?`, profile, branch).Scan(&n)
		return n > 0, err
	}
}
