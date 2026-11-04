//go:build doomsday

package doomsday

import (
	"context"
	"database/sql"
	"sync"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// charterLives remembers the live_per_tree of a verified charter (insert-only, so one value for good).
var charterLives sync.Map // depthKey -> int

// LivePerTree is the live_per_tree of the profile's open run, or 0 (no opinion) when no run is open or the run or its
// charter does not verify. The coder host takes min(this, its own constant): a charter number above the constant changes
// nothing, so this can only LOWER the limit a tree has always had (note B3, seam S3).
func (d DepthSource) LivePerTree(ctx context.Context, profile string) int {
	if d.DB == nil || profile == "" {
		return 0
	}
	var open int
	if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM doomsday_runs WHERE profile = ? AND state <> 'quiescent'`, profile).Scan(&open); err != nil || open == 0 {
		return 0
	}
	st := store.New(d.DB, d.keyer(profile))
	runs, err := st.OpenRuns(ctx, profile)
	if err != nil || len(runs) == 0 {
		return 0
	}
	key := depthKey{d.DB, profile, runs[0].Generation}
	if v, ok := charterLives.Load(key); ok {
		return v.(int)
	}
	c, err := st.LoadCharter(ctx, profile, runs[0].Generation)
	if err != nil {
		return 0
	}
	charterLives.Store(key, c.Limits.LivePerTree)
	return c.Limits.LivePerTree
}

// HostLivePolicy is the coder host's LivePolicy.
func (d DepthSource) HostLivePolicy() coderhost.LivePolicy {
	return func(profileID, _ string) int { return d.LivePerTree(context.Background(), profileID) }
}

// InstallLivePolicy makes every coder-host store made from now on, in this process, read the open run's live_per_tree as
// a lowering of the tree limit. Only a build with the mode calls it.
func InstallLivePolicy() { InstallLivePolicyWith(nil) }

// InstallLivePolicyWith is InstallLivePolicy with the keys the rows verify under (nil: the vault keys).
func InstallLivePolicyWith(keys func(profile string) store.Keyer) {
	coderhost.SetDefaultLivePolicy(func(db *sql.DB) coderhost.LivePolicy { return DepthSource{DB: db, Keys: keys}.HostLivePolicy() })
}
