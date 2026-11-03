package doomsday

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/doomsday/ops"
	"github.com/monoes/mono-agent/internal/doomsday/store"
	"github.com/monoes/mono-agent/internal/tasks"
)

// The production construction of the ops package (B1 depth and width as charter inputs, B2 routing). Nothing here is
// reachable without a build that carries the mode, and nothing here changes a host while no run is open:
//
//   - the depth policies answer 0 (which both stores clamp up to today's limit) unless the profile has a run that is
//     not quiescent, and then the charter's tree_depth;
//   - the route consults the profile's running run on every call and is the mode off (coderhost.ErrDoomsdayOff) when
//     there is none, so a host that lives across runs needs no re-installing and no removing: a run that reaches
//     stop-requested, or quiescent, ends the route and the widening with it.

// charterDepths remembers the tree_depth of a verified charter: a charter row is insert-only, so (database, profile,
// generation) names one value for good.
var charterDepths sync.Map // depthKey -> int

type depthKey struct {
	db      *sql.DB
	profile string
	gen     int64
}

// DepthSource reads the tree depth the profile's open run allows.
type DepthSource struct {
	DB *sql.DB
	// Keys makes the MAC keys a profile's rows verify under; nil is the profile's vault key (VaultKeyer).
	Keys func(profile string) store.Keyer
}

func (d DepthSource) keyer(profile string) store.Keyer {
	if d.Keys != nil {
		return d.Keys(profile)
	}
	return VaultKeyer{DB: d.DB, Profile: profile}
}

// ForProfile is the open run's tree_depth for the profile, or 0 when no run is open or the run or its charter does not
// verify (the stores clamp 0 up to the limit they have always had, so a failure never widens anything). The common case, a
// profile with no open run, is one query and needs no key.
func (d DepthSource) ForProfile(ctx context.Context, profile string) int {
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
	if v, ok := charterDepths.Load(key); ok {
		return v.(int)
	}
	c, err := st.LoadCharter(ctx, profile, runs[0].Generation)
	if err != nil {
		return 0
	}
	charterDepths.Store(key, c.Limits.TreeDepth)
	return c.Limits.TreeDepth
}

// TasksPolicy is the tasks package's DepthPolicy: the session names its profile, the profile's open run names the depth.
//
// A session being created is asked about before its row is visible to this handle (the walker runs inside the creating
// transaction, which this policy cannot read). Its profile is then not known, and the answer is the smallest tree_depth
// among the open runs of the database: exact when one profile has a run (the usual case), never wider than any run's
// charter otherwise. A database with no open run answers 0 at the cost of one query.
func (d DepthSource) TasksPolicy() tasks.DepthPolicy {
	return func(ctx context.Context, sessionID int64) int {
		var profile string
		err := d.DB.QueryRowContext(ctx, `SELECT profile_id FROM task_sessions WHERE id = ?`, sessionID).Scan(&profile)
		switch {
		case err == nil:
			return d.ForProfile(ctx, profile)
		case !errors.Is(err, sql.ErrNoRows):
			return 0
		}
		return d.narrowestOpen(ctx)
	}
}

// narrowestOpen is the smallest tree_depth among the profiles that have a run that is not quiescent (0 when none, or when
// any of them does not verify).
func (d DepthSource) narrowestOpen(ctx context.Context) int {
	rows, err := d.DB.QueryContext(ctx, `SELECT DISTINCT profile FROM doomsday_runs WHERE state <> 'quiescent'`)
	if err != nil {
		return 0
	}
	var profiles []string
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil {
			profiles = append(profiles, p)
		}
	}
	rows.Close()
	narrowest := 0
	for i, p := range profiles {
		n := d.ForProfile(ctx, p)
		if n == 0 {
			return 0
		}
		if i == 0 || n < narrowest {
			narrowest = n
		}
	}
	return narrowest
}

// HostPolicy is the coder host's DepthPolicy.
func (d DepthSource) HostPolicy() coderhost.DepthPolicy {
	return func(profileID, _ string) int { return d.ForProfile(context.Background(), profileID) }
}

// InstallDepthPolicies makes every tasks and coder-host store made from now on, in this process, read the open run's
// depth. Only a build with the mode calls it (cmd/monoagentcli/doomsday_engine.go).
func InstallDepthPolicies() { InstallDepthPoliciesWith(nil) }

// InstallDepthPoliciesWith is InstallDepthPolicies with the keys the rows verify under (nil: the vault keys).
func InstallDepthPoliciesWith(keys func(profile string) store.Keyer) {
	tasks.SetDefaultDepthPolicy(func(db *sql.DB) tasks.DepthPolicy { return DepthSource{DB: db, Keys: keys}.TasksPolicy() })
	coderhost.SetDefaultDepthPolicy(func(db *sql.DB) coderhost.DepthPolicy { return DepthSource{DB: db, Keys: keys}.HostPolicy() })
}

// Ops is the enforcement side of the mode over one wiring: the adapter that reads the live charter and spends the
// counters, the enforcer of plan.approve / ask.answer / spend.grant, and the typed enforcer of note 13.4. Every write goes
// through the sequenced store, so each is a witnessed transition.
type Ops struct {
	Adapter  ops.StoreAdapter
	Enforcer *ops.Enforcer
	Typed    *ops.TypedEnforcer
}

// Ops builds the enforcers over the wiring's chained store. facts reads a plan, an ask and a budget request (the coder
// host's own store).
func (w Wiring) Ops(facts ops.Facts) *Ops {
	ch := w.Chained()
	bounds := coderhost.SpendBounds()
	ad := ops.StoreAdapter{Store: ch, Bounds: bounds}
	return &Ops{
		Adapter: ad,
		Enforcer: &ops.Enforcer{Charters: ad, Facts: facts, Ledger: ad, Auditor: ad, Bounds: bounds,
			Parker: PersonParker{Store: ch, Profile: w.Profile}, Bosses: BossSource{Wiring: w}},
		Typed: &ops.TypedEnforcer{Charters: ad, Approvals: ops.StoreApprovals{Store: ch}, Ledger: ad, Auditor: ad, Bounds: bounds},
	}
}

// RunRunning is coderhost.DoomsdayRoute.Live: the profile's latest run is running (not stop-requested or later). A run
// that cannot be read is an error, which the route treats as the mode off.
func (w Wiring) RunRunning(ctx context.Context, profile string) (bool, error) {
	runs, err := w.Store.OpenRuns(ctx, profile)
	if err != nil {
		return false, err
	}
	return len(runs) > 0 && runs[0].State == store.StateRunning, nil
}

// InstallOps puts the route on the coder host's store: the host of an engine built with the mode. It is installed once and
// is live only while a run is running.
func (w Wiring) InstallOps(st *coderhost.Store) *Ops {
	o := w.Ops(st.NewDoomsdayFacts())
	st.SetDoomsdayRoute(&coderhost.DoomsdayRoute{Enforcer: o.Enforcer, Live: w.RunRunning})
	return o
}

// PersonParker is ops.Parker. An over-limit plan stays a draft and an over-limit critical ask stays open, so the
// subject is already the person item and whatever depends on it (the plan's tasks, the asker's wait) stays blocked; the
// route never answers either (BossAnswerAsk and BossApprovePlan return ErrParked before any effect). What the parker adds
// is the chained record that a person item was raised by the mode, with the limits that were exceeded, so the pane and
// `doomsday status` can say what waits for a person.
type PersonParker struct {
	Store   store.Store
	Profile string
}

// personItem is the payload of the person-item row.
type personItem struct {
	Op       string   `json:"op"`
	Subject  string   `json:"subject"`
	Exceeded []string `json:"exceeded"`
}

// KindPersonItem is the audit kind of a raised person item.
const KindPersonItem = "ops.person-item"

func (p PersonParker) Park(ctx context.Context, x ops.Park) error {
	if x.Profile != p.Profile {
		return errors.New("doomsday: a park for another profile")
	}
	next, err := p.Store.NextGeneration(ctx, x.Profile)
	if err != nil {
		return err
	}
	b, err := json.Marshal(personItem{Op: x.Op, Subject: x.Subject, Exceeded: x.Exceeded})
	if err != nil {
		return err
	}
	_, err = p.Store.AppendAudit(ctx, x.Profile, next-1, KindPersonItem, string(b))
	return err
}
