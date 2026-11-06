package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"

	chatcli "github.com/monoes/mono-agent/internal/cli/chat"
	doomsdaycli "github.com/monoes/mono-agent/internal/cli/doomsday"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/doomsday/ops"
	"github.com/monoes/mono-agent/internal/doomsday/stop"
	"github.com/monoes/mono-agent/internal/doomsday/store"
	"github.com/monoes/mono-agent/internal/dynorg"
	"github.com/monoes/mono-agent/internal/mcp"
)

// This file wires the open-ended mode into every build; the mode itself is off until a charter starts a run
// (doomsday_gate_test.go holds the gates). Containment is the stop package's probe: where it fails (no cgroup v2
// delegation, macOS, Windows) the engine refuses to start.

func init() {
	// Depth limits as charter inputs: every tasks and coder-host store of this process reads the profile's open run, and
	// is what it always was while there is none.
	doomsdaycli.InstallDepthPolicies()
	// live_per_tree as a lowering of the tree's live-session limit (never a raise).
	doomsdaycli.InstallLivePolicy()
	// The lead-facing boss tools of the MCP server: each call installs the route on its own coder store, and the route
	// is the mode off whenever the profile has no running run (a build without the mode never sets this).
	mcp.SetBossRouteInstaller(bossRouteInstaller)
	// And its finish.begin: the boss starts the finish through the ops package's enforcement function, on the open run.
	mcp.SetBossFinishBeginner(bossFinishBeginner)
	// The strategy layer: its two session-callable operations as boss tools, and its read-only verbs.
	mcp.SetBossStrategyCaller(bossStrategyOp)
	// The boss's read-only reading of the strategy layer's finish-ready marker; the boss then calls finish.begin itself.
	mcp.SetBossFinishReadyReader(bossFinishReady)
	doomsdayStrategyReader = strategyReaderFor
	// `doomsday governor status`: the governor's read model (read-only).
	doomsdayExtra = governorGroupFor
	doomsdayEngine = func(cfg *globalConfig) doomsdaycli.Engine {
		db, err := initDB(cfg)
		if err != nil {
			return engineForOpenFailure(err)
		}
		w := doomsdayWiring(cfg, db.DB)
		// The CLI's own Stop: the line that says the marker is written, before the database step that may wait on a busy database.
		w.Progress = doomsdayProgress
		return w.FinishEngine()
	}
	doomsdayReadEngine = func(cfg *globalConfig) doomsdaycli.Engine {
		db, err := openReadDB(cfg) // the read verbs create and migrate nothing
		if err != nil {
			return engineForOpenFailure(err)
		}
		return doomsdayWiring(cfg, db).ReadEngine()
	}
	doomsdayChat = func(cfg *globalConfig, d *chatcli.Deps) {
		// A conductor's merges go through the open run's gate and journal; the resolver reads the run on every
		// merge, so a run that starts or stops after the turn began is seen.
		d.DoomsdayMerge = func(db *sql.DB) func(ctx context.Context) (*dynorg.DoomsdayMerge, error) {
			return processHostGate(cfg, db).MergeFor
		}
		// The coder host gets the open run's dispatch gate: tickets for spawns, token mints and turn releases, the run
		// stamp, and the bound-mode requirement. With no open run the gate does nothing, so the host is what it was. A
		// gate whose launcher hooks cannot be installed still stands (the ticket is then taken around Start).
		chatcli.HostBuilt = func(st *coderhost.Store, h *coderhost.Host, db *sql.DB) {
			w := doomsdayWiring(cfg, db)
			// A run the host starts beside is known before the first dispatch (one read-only count, no key, nothing written).
			doomsdaycli.OpenRunPrecheck(db, cfg.ProfileID, processRunActivity(cfg.ProfileID), func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) })(context.Background())
			g := processHostGate(cfg, db)
			// The boss's typed operations (plan.approve, ask.answer, spend.grant) are enforced against the open run's
			// charter, witnessed; the route is the mode off whenever no run is running.
			w.InstallOps(st)
			if err := g.Install(st, h); err != nil {
				fmt.Fprintf(os.Stderr, "coder host: the open-ended mode's launcher hooks could not be installed (turns are ticketed around their start instead): %v\n", err)
			}
			// The resource governor admits a turn before the gate takes its ticket; with no running run it is a plain call.
			governLauncher(cfg, st, h, db)
			// The strategy layer's host is ticked from this process; with no run open a tick reads and writes nothing.
			startStrategyLoop(cfg, db)
		}
		// The coder host joins the strategy loop after its Run returned, before the database closes.
		chatcli.HostStopped = stopStrategyLoop
		// Reconcile of worktrees resolves begun merges first and keeps the worktrees of unresolved ones (note
		// 13.3 section 5.1). A database that cannot be opened keeps every turn's worktree.
		dynorg.SetReconcileGuard(func(ctx context.Context, cwd string, active func(string) bool) func(string) bool {
			db, err := initDB(cfg)
			if err != nil {
				return func(string) bool { return true }
			}
			defer db.Close()
			r := doomsdayWiring(cfg, db.DB).MergeRecovery()
			return r.GuardTurns(ctx, cwd, active)
		})
	}
}

// doomsdayProgressOut is where the CLI's Stop prints its early progress line (a test swaps it).
var doomsdayProgressOut io.Writer = os.Stderr

// doomsdayProgress prints the line and can never fail or end the Stop it reports on. The marker is already durable when it runs, so
// a reader that went away (`stop 2>&1 | head -1`) must not kill the process with SIGPIPE before the intent, the revocation and the
// unit end: with SIGPIPE ignored the write fails with EPIPE, and that error is dropped.
func doomsdayProgress(m string) {
	signal.Ignore(syscall.SIGPIPE)
	_, _ = fmt.Fprintln(doomsdayProgressOut, m)
}

// engineForOpenFailure is the engine when the database cannot be opened. Nothing starts either way (fails closed), but
// a read verb must not call that "off": the database may be locked, corrupt or unmigratable while a run is live, so it
// gets doomsdaycli.Unreadable (an error result, non-zero exit). Client mode never opens the local database, so it is
// the mode off there.
func engineForOpenFailure(err error) doomsdaycli.Engine {
	if errors.Is(err, errClientModeDB) || errors.Is(err, errNoReadableSchema) {
		return doomsdaycli.Off{}
	}
	return doomsdaycli.Unreadable{Err: err}
}

var (
	hostGateMu sync.Mutex
	hostGates  = map[string]*doomsdaycli.HostGate{}
)

var (
	runActivityMu sync.Mutex
	runActivities = map[string]*doomsdaycli.RunActivity{}
)

// processRunActivity is the process's one record per profile of whether a run has been seen: the dispatch gate, the governor and
// the strategy loop share it, so a run any of them saw keeps all of them fail-closed when the run table becomes unreadable.
func processRunActivity(profile string) *doomsdaycli.RunActivity {
	runActivityMu.Lock()
	defer runActivityMu.Unlock()
	a := runActivities[profile]
	if a == nil {
		a = &doomsdaycli.RunActivity{}
		runActivities[profile] = a
	}
	return a
}

// processHostGate is the process's one HostGate per profile (merges and the coder host share it): two gates would hold
// two stop.Gate objects for one generation, and a Stop's close, outcome and hung-starter rule would miss the other's tickets.
func processHostGate(cfg *globalConfig, db *sql.DB) *doomsdaycli.HostGate {
	hostGateMu.Lock()
	defer hostGateMu.Unlock()
	if g, ok := hostGates[cfg.ProfileID]; ok {
		return g
	}
	w := doomsdayWiring(cfg, db)
	g := &doomsdaycli.HostGate{Store: w.Chained(), Fresh: w.Fresh(), Profile: w.Profile, RunDir: w.RunDir, Kernel: w.Kernel, Journal: doomsdaycli.MergeJournal(db, w.Store),
		Activity:  processRunActivity(w.Profile),
		BindingOn: func() (bool, error) { return chatcli.PeercredBindingOn(db) },
		InstallHooks: func(st *coderhost.Store, h *coderhost.Host, hk doomsdaycli.Hooks) error {
			return chatcli.InstallRunHooks(st, h, chatcli.RunHooks{RunStamp: hk.RunStamp, BeforeRelease: hk.BeforeRelease, Place: hk.Place})
		}}
	hostGates[cfg.ProfileID] = g
	return g
}

// probeAt is stop.ProbeAt: a seam so a test can see which directories the wiring hands the self-tests.
var probeAt = stop.ProbeAt

// doomsdayWiring is the one construction of the engine's seams: the store over the profile's vault key, the run
// directory next to the database, the stop package's containment probe and kernel, real token revocation.
func doomsdayWiring(cfg *globalConfig, db *sql.DB) doomsdaycli.Wiring {
	keys := doomsdaycli.VaultKeyer{DB: db, Profile: cfg.ProfileID}
	runDir := doomsdayRunDirPath(cfg)
	return doomsdaycli.Wiring{
		// The entry refuses a charter whose 13.4 constraints would only ever deny (ops.EntryCheck, under the host's own spend bounds).
		Store:   store.New(db, keys).WithEntryCheck(ops.EntryCheck(coderhost.SpendBounds())),
		Profile: cfg.ProfileID,
		Binding: func(context.Context) (bool, error) { return chatcli.PeercredBindingOn(db) },
		RunDir:  stop.RunDir{Dir: runDir},
		// The self-tests run next to the real database and in the real run directory, so what they show holds on that volume.
		Probe:  func() error { return probeAt(filepath.Dir(expandPath(cfg.DBPath)), runDir) },
		Kernel: doomsdayKernel(),
		Host:   stop.SystemHost(),
		// Stop's token revocation and queue cancel; the governor's queue is cancelled with the host's pending sends.
		Tokens: doomsdaycli.GovernedTokens{Inner: doomsdaycli.CoderTokens{DB: db, Profile: cfg.ProfileID, Host: coderhost.NewStore(db)}, DB: db, Profile: cfg.ProfileID, Keys: keys,
			Logf: func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) }},
		DB: db,
		// The witness of note 13.5: the pinned connection on the database file, the vault-derived MAC keys, and the OS
		// secret store of the anchor (a run on a machine with none is `anchor-bound: no` and says so).
		Witness: &doomsdaycli.WitnessSetup{DBPath: expandPath(cfg.DBPath), Keys: keys, Secrets: stop.OSSecretStore{},
			Warn: func(m string) { fmt.Fprintln(os.Stderr, "doomsday: "+m) }},
	}
}

// bossFinishBeginner is mcp.BossFinishBeginner: finish.begin for the verified lead session. Like the route installer it asks
// first whether the profile has a running run and constructs nothing (no pinned connection, no key read) when it has none.
func bossFinishBeginner(ctx context.Context, db *sql.DB, dbPath, profile string, session int64) (mcp.BossFinishResult, error) {
	var running int
	if err := db.QueryRow(`SELECT COUNT(*) FROM doomsday_runs WHERE profile = ? AND state = 'running'`, profile).Scan(&running); err != nil || running == 0 {
		return mcp.BossFinishResult{}, fmt.Errorf("%w: %v", coderhost.ErrDoomsdayOff, ops.ErrNoCharter)
	}
	res, err := doomsdayWiring(&globalConfig{DBPath: dbPath, ProfileID: profile}, db).BossFinishBegin(ctx, session)
	switch {
	case errors.Is(err, ops.ErrNoCharter):
		return mcp.BossFinishResult{}, fmt.Errorf("%w: %v", coderhost.ErrDoomsdayOff, err)
	case errors.Is(err, ops.ErrCharterStale), errors.Is(err, ops.ErrCallerMismatch), errors.Is(err, ops.ErrNotEnabled), errors.Is(err, ops.ErrNotAllowlisted):
		return mcp.BossFinishResult{}, fmt.Errorf("%w: %v", coderhost.ErrNotYours, err)
	case err != nil:
		return mcp.BossFinishResult{}, err
	}
	return mcp.BossFinishResult{Generation: res.Generation, Accepted: res.Accepted, Refused: res.Refused, State: res.State, Lines: res.Lines}, nil
}

// bossRouteInstaller is mcp.BossRouteInstaller: the route on one boss-tool call's coder store. One cheap query first:
// with no running run nothing is constructed (no pinned connection, no key read) and the call is refused as the mode off.
func bossRouteInstaller(_ context.Context, st *coderhost.Store, db *sql.DB, dbPath, profile string) error {
	var running int
	if err := db.QueryRow(`SELECT COUNT(*) FROM doomsday_runs WHERE profile = ? AND state = 'running'`, profile).Scan(&running); err != nil || running == 0 {
		return errors.New("no run is running for this profile")
	}
	doomsdayWiring(&globalConfig{DBPath: dbPath, ProfileID: profile}, db).InstallOps(st)
	return nil
}
