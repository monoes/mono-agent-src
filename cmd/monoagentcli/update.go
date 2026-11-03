package main

import (
	"context"
	"os"
	"os/exec"

	"github.com/monoes/mono-agent/internal/autostart"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/cli/doctor"
	"github.com/monoes/mono-agent/internal/cli/update"
	"github.com/monoes/mono-agent/internal/daemonhb"
	"github.com/monoes/mono-agent/internal/release"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/spf13/cobra"
)

// `monoagentcli update` lives in internal/cli/update; this is its adapter:
// the CLI's globals and helpers handed over as update.Deps.

// releasesRepo is the public repository the release assets are published to
// (owner/name); every release URL of the CLI is built from it. It is not the
// repository the code is built in (which may be private), and it must equal
// internal/release.ReleasesRepo.
const releasesRepo = update.ReleasesRepo

// latestReleaseURL is GitHub's latest-release endpoint (a variable so tests
// can point it at a fake server).
var latestReleaseURL = update.LatestReleaseURL

// releaseClient builds the signed-manifest client (a variable so tests can
// point it at an httptest server with generated keys).
var releaseClient = release.DefaultClient

func newUpdateCmd(cfg *globalConfig) *cobra.Command {
	return update.NewCommand(updateDeps(cfg))
}

func updateDeps(cfg *globalConfig) update.Deps {
	return update.Deps{
		JSONOutput:       func() bool { return cfg.JSONOutput },
		Version:          getVersion,
		ReleaseClient:    func() *release.Client { return releaseClient() },
		LatestReleaseURL: latestReleaseURL,
		InvalidInput:     errInvalidInput,
		WriteJSON:        writeJSONTo,
		StartDetached:    startDetached,
		RestartBlockers: func(ctx context.Context, pid int) (int, error, error) {
			return restartBlockers(ctx, cfg, pid)
		},
		Installer: func() autostart.Installer { return newInstaller() },
		Bridge: func(ctx context.Context) (update.BridgeState, bool) {
			daemonPID := 0
			if hb, live := daemonhb.Read(); live {
				daemonPID = hb.PID
			}
			b, ok := doctor.FindBridge(ctx, daemonPID)
			return update.BridgeState{PID: b.PID, Version: b.Version, InFlight: b.InFlight, Unit: b.Unit, DaemonOwned: b.DaemonOwned}, ok
		},
		RestartBridge: doctor.RestartBridgeUnit,
	}
}

// fetchLatest is update.FetchLatest with this CLI's release client and endpoint.
func fetchLatest(ctx context.Context, force bool) (*update.LatestInfo, error) {
	return update.FetchLatest(ctx, releaseClient(), latestReleaseURL, force)
}

// startDetached starts name in its own session (clicore.DetachStart) so the
// restart outlives this CLI and the app that ran it.
func startDetached(name string, args ...string) error {
	return clicore.DetachStart(exec.Command(name, args...)) //nolint:gosec
}

// restartBlockers reads the database the command is pointed at, opened without migrating it: how
// many executions the daemon with this pid owns, which a restart ends (RUNNING, and QUEUED rows it
// has claimed; a WAITING run is persisted and resumes), and whether the settings saved with
// `api config` can be used, which `daemon restart` checks first. A database that is absent or
// unreadable is err: the caller does not restart what it cannot see into.
func restartBlockers(ctx context.Context, cfg *globalConfig, pid int) (inFlight int, settingsErr, err error) {
	path := expandPath(cfg.DBPath)
	if _, err = os.Stat(path); err != nil {
		return 0, nil, err
	}
	db, err := storage.NewDatabase(path)
	if err != nil {
		return 0, nil, err
	}
	defer db.Close()
	err = db.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM workflow_executions WHERE pid = ? AND status IN ('RUNNING', 'QUEUED')`, pid).Scan(&inFlight)
	if err != nil {
		return 0, nil, err
	}
	_, settingsErr = savedSettings(ctx, db.DB)
	return inFlight, settingsErr, nil
}
