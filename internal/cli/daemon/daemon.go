package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/cli/apiruntime"
	"github.com/monoes/mono-agent/internal/cli/bridge"
	"github.com/monoes/mono-agent/internal/cli/capturehooks"
	"github.com/monoes/mono-agent/internal/cli/orgenv"
	"github.com/monoes/mono-agent/internal/cli/orgsvc"
	"github.com/monoes/mono-agent/internal/daemonhb"
	"github.com/monoes/mono-agent/internal/httpapi"
	"github.com/monoes/mono-agent/internal/scheduler"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// NewCommand is `daemon`: it runs the workflow engine as a long-running foreground process,
// keeping every active workflow's triggers (schedule, webhook) alive across
// all profiles until interrupted. Without this, `workflow activate` only
// registers a trigger for the lifetime of that one CLI invocation — the
// engine deactivates all triggers on Stop(), which happens as soon as the
// activating command exits.
func NewCommand(d Deps) *cobra.Command {
	var apiOn, allowMutations, bridgeOn bool
	var apiAddr string
	var api apiruntime.Flags
	c := &cobra.Command{
		Use:   "daemon",
		Short: "Run the workflow engine in the foreground, keeping scheduled/webhook triggers alive",
		Long: "Starts the workflow engine and restores every active workflow's triggers (across all " +
			"profiles), then blocks until interrupted (Ctrl+C). This is what actually makes " +
			"`workflow activate` schedules fire over time — without a daemon running, an activated " +
			"workflow's cron/webhook triggers only live for the instant the activating command runs.\n\n" +
			"The daemon also serves the HTTP API (--api, on by default, same token and routes as " +
			"`monoagentcli httpapi`), runs org automations that roles call and trigger.org workflows, " +
			"resumes org.run/org.ask pauses when their org event arrives, reconciles org files with their " +
			"grants, and makes autonomy decisions. Without it every org behaves as autonomy level manual. " +
			"It writes ~/.monoagent/daemon-heartbeat.json every 10 seconds. When turned on with " +
			"`agent roster auto-revalidate on` (off by default; it spends money), it also re-checks stale " +
			"roster models while nothing else runs.\n\n" +
			"It also holds the Chrome extension bridge open (--bridge, on by default, same bridge " +
			"`monoagentcli extension serve` runs standalone), so the MonoAgent Bridge extension stays " +
			"connected for as long as the daemon runs instead of needing a separate `extension serve` " +
			"left open in another terminal. `monoagentcli daemon install` registers the daemon itself " +
			"to start at login (see `daemon install --help`), which is what makes this persist across " +
			"reboots and on a fresh machine, not just this one terminal.\n\n" +
			"The OpenAI-compatible API (/v1) is served on the HTTP API listener when that is loopback, and on " +
			"its own listener with --v1-addr (TLS required beyond loopback). See `monoagentcli api --help`.",
		Example: "  monoagentcli daemon\n  monoagentcli daemon --api=false\n  monoagentcli daemon --api-addr 127.0.0.1:9400 --allow-mutations\n" +
			"  monoagentcli daemon --bridge=false\n  monoagentcli daemon install",
		RunE: func(cmd *cobra.Command, args []string) error {
			// One daemon per home, taken before anything starts: two would
			// both fire the same schedules and run every workflow twice.
			// The OS drops the lock if this process dies.
			releaseLock, err := daemonhb.Lock()
			if errors.Is(err, daemonhb.ErrLocked) {
				return fmt.Errorf("%w (lock %s)", err, daemonhb.LockPath())
			}
			if err != nil {
				return fmt.Errorf("daemon lock: %w", err)
			}
			defer releaseLock()

			engine, closeBrowsers, err := d.BuildEngine()
			if err != nil {
				return fmt.Errorf("build engine: %w", err)
			}
			defer closeBrowsers()

			// Stop the scheduler after the engine has stopped (defers run LIFO,
			// and this is registered before engine.Stop below): the engine's
			// trigger entries live in this scheduler until deactivation.
			if sched, ok := engine.Scheduler().(*scheduler.Scheduler); ok {
				defer sched.Stop() //nolint:errcheck
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			// While the deferred engine.Stop() drains in-flight work, the
			// NotifyContext registration above is still active and swallows any
			// further SIGINT/SIGTERM. Count signals with a raw handler so a
			// second interrupt forces an immediate exit instead of hanging on a
			// stuck execution. shutdownDone is closed once the drain has
			// completed cleanly (registered before engine.Stop, so it runs after).
			shutdownDone := make(chan struct{})
			sigCh := make(chan os.Signal, 2)
			signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
			defer signal.Stop(sigCh)
			var forcedExitAPI atomic.Pointer[apiruntime.Runtime] // set once the API runtime exists
			go func() {
				<-sigCh // first signal — also observed by the NotifyContext above
				select {
				case <-sigCh:
					fmt.Fprintln(os.Stderr, "Second interrupt received — forcing immediate exit.")
					if rt := forcedExitAPI.Load(); rt != nil {
						rt.KillTurns() // the agent CLIs must not outlive the daemon
					}
					os.Exit(130)
				case <-shutdownDone:
				}
			}()
			defer close(shutdownDone)

			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()
			orgs := orgsvc.New(db, engine, d.OpenWorkflowStore)
			registerQueueTriggers(engine, orgs.Logf)
			engine.RegisterTriggerSource(workflow.TriggerNodeTypeSSE, workflow.NewSSETriggerSource(db.DB, orgs.Logf))
			apiRT, err := apiruntime.NewForDaemon(db.DB, api, func(format string, args ...any) { orgs.Logf("api: "+format, args...) }, d.API)
			if err != nil {
				return err
			}
			if apiRT.Disabled() != nil { // the settings saved with `api config` cannot be used: everything else runs
				fmt.Fprintf(os.Stderr, "warning: OpenAI-compatible API not served: %v\n", apiRT.Disabled())
			}
			forcedExitAPI.Store(apiRT)
			defer apiRT.Drain() // runs before the database closes: no agent CLI outlives the daemon

			if err := engine.Start(ctx); err != nil {
				return fmt.Errorf("start engine: %w", err)
			}
			defer engine.Stop() //nolint:errcheck

			if err := engine.RestoreActiveWorkflows(ctx); err != nil {
				return fmt.Errorf("restore active workflows: %w", err)
			}

			servingAddr := ""
			if apiOn {
				addr, err := startDaemonAPI(ctx, d, db, engine, orgs, apiRT, apiAddr, allowMutations)
				if err != nil {
					fmt.Fprintf(os.Stderr, "warning: HTTP API not served: %v\n", err)
				} else {
					servingAddr = addr
				}
			}

			v1ServingAddr, err := apiRT.StartV1(ctx)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: OpenAI-compatible API listener not served: %v\n", err)
			}

			bridgeServingAddr := ""
			if bridgeOn {
				addr, closeBridge, err := startDaemonBridge(ctx, d)
				if err != nil {
					fmt.Fprintf(os.Stderr, "warning: extension bridge not served: %v\n", err)
				} else {
					defer closeBridge()
					bridgeServingAddr = addr
				}
			}

			go daemonhb.RunWith(ctx, apiRT.Heartbeat(servingAddr, bridgeServingAddr, v1ServingAddr),
				daemonHeartbeatRefresh(engine))
			// The org services run only while the account is valid (daemon_account.go);
			// the daemon itself never exits because of a lock.
			go newAccountSupervisor(orgs, engine).run(ctx)
			// Automatic roster re-validation (#230): off unless the user
			// turned it on; waited for so a run stops before the db closes.
			defer d.StartAutoRevalidation(ctx, db.DB, orgs.Logf)()

			msg := daemonRunningLine(account.CurrentStatus())
			if servingAddr != "" {
				msg += " HTTP API on " + servingAddr + "."
			}
			if v1ServingAddr != "" {
				msg += " OpenAI-compatible API on " + v1ServingAddr + "."
			}
			if bridgeServingAddr != "" {
				msg += " Extension bridge on " + bridgeServingAddr + "."
			}
			fmt.Fprintln(os.Stdout, msg+" Press Ctrl+C to stop.")
			<-ctx.Done()
			fmt.Fprintln(os.Stdout, "Shutting down...")
			// The API's turns end now, not after the engine has drained: a long
			// workflow must not keep an agent CLI running for a client that is gone.
			apiRT.Drain()
			return nil
		},
	}
	c.Flags().BoolVar(&apiOn, "api", true, "Serve the HTTP API and the automation-role endpoint receiver in this process")
	c.Flags().StringVar(&apiAddr, "api-addr", "", "HTTP API address (default 127.0.0.1:9322, or MONOAGENT_HTTPAPI_ADDR)")
	c.Flags().BoolVar(&allowMutations, "allow-mutations", false, "Serve mutating HTTP API endpoints (the endpoint receiver is served either way)")
	api.Bind(c)
	c.Flags().BoolVar(&bridgeOn, "bridge", true, "Hold the Chrome extension bridge open in this process (same bridge 'extension serve' runs standalone)")
	c.AddCommand(newDaemonInstallCmd(), newDaemonUninstallCmd(), NewRestartCommand(d))
	return c
}

// startDaemonBridge starts the Chrome extension bridge (see
// internal/cli/bridge) so the MonoAgent Bridge extension stays connected for
// as long as the daemon runs, instead of needing a separate
// `monoagentcli extension serve` left open in another terminal —
// setupExtensionBridge's own comment already named "the daemon" as one of
// the processes a bridge might belong to (node.go). Probes first exactly as
// runExtensionServe does: if something else already owns the port (a stray
// `extension serve`, or another daemon), relay through it rather than racing
// for a bind that would only fail.
//
// Unlike startDaemonAPI, the caller gets a closeFn back rather than this
// function blocking or backgrounding its own cleanup: the daemon's RunE
// already defers every other component's shutdown in the order it started
// them (engine, db, scheduler), and the bridge fits that same pattern rather
// than inventing a second shutdown path.
func startDaemonBridge(ctx context.Context, d Deps) (addr string, closeFn func(), err error) {
	if st, base, ok := bridge.FindRunning(context.Background(), d.ProbeAddrs()); ok {
		return bridge.Addr(st, base), func() {}, nil
	}

	logger := bridge.NewServeLogger()
	srv := d.NewExtensionServer(logger)
	summaries := d.InstallCaptureHooks(srv, bridge.LoggerLogf(logger))
	summaries.SweepIndexAfter(ctx, capturehooks.IndexStartupDelay)
	closeFn = func() {
		summaries.Close()
		srv.Close() //nolint:errcheck
	}

	errCh := srv.StartAsync(ctx)
	addr, err = bridge.WaitForBind(ctx, srv, errCh)
	if err != nil {
		closeFn()
		return "", func() {}, err
	}
	return addr, closeFn, nil
}

// startDaemonAPI binds the HTTP API over the daemon's own database and
// engine, records the address for endpoint URLs, and serves until ctx ends.
func startDaemonAPI(ctx context.Context, d Deps, db *storage.Database, engine *workflow.WorkflowEngine, orgs *orgsvc.Services, api *apiruntime.Runtime, addr string, allowMutations bool) (string, error) {
	srv, err := httpapi.NewServer(httpapi.Options{
		DB: db, Store: d.OpenWorkflowStore(db), Engine: engine, Profile: d.ProfileID(),
		Addr: addr, AllowMutations: allowMutations, Version: d.API.Version(),
		// The server has one ExtraRoutes slot: the org receiver and the
		// OpenAI-compatible API both ride it.
		ExtraRoutes: apiruntime.DaemonRoutes(orgs.RegisterRoutes, api, addr),
	})
	if err != nil {
		api.ReleaseUnused() // the routes above built the gateway, which nothing serves now
		return "", err
	}
	ln, err := net.Listen("tcp", srv.Addr())
	if err != nil {
		api.ReleaseUnused()
		return "", fmt.Errorf("listen on %s: %w (is another daemon or `monoagentcli httpapi` using it?)", srv.Addr(), err)
	}
	if _, err := db.DB.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, orgenv.DaemonAPIAddrSetting, srv.Addr()); err != nil {
		fmt.Fprintf(os.Stderr, "warning: recording API address: %v\n", err)
	}
	go func() {
		if err := srv.Serve(ctx, ln); err != nil && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "HTTP API stopped: %v\n", err)
		}
	}()
	return srv.Addr(), nil
}

// heartbeatSchedules is the heartbeat form of the engine's schedule triggers.
func heartbeatSchedules(runs []workflow.ScheduledRun) []daemonhb.Schedule {
	out := make([]daemonhb.Schedule, 0, len(runs))
	for _, r := range runs {
		out = append(out, daemonhb.Schedule{WorkflowID: r.WorkflowID, NodeID: r.NodeID, NextRun: r.Next.UTC().Format(time.RFC3339)})
	}
	return out
}
