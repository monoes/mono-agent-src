package update

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"

	"github.com/monoes/mono-agent/internal/appupdate"
	"github.com/spf13/cobra"
)

// `update --app <exe>`: update the desktop app and the CLI bundled with it
// from the latest release (internal/appupdate). Every asset must match the
// release's SHA256SUMS.txt, and the files are swapped all-or-nothing, or
// nothing is installed. The desktop app calls this and only shows progress;
// it then quits, and on macOS/Windows a detached script relaunches it once
// it has exited.

// AppResult is `update --app --json` on stdout. Progress goes to stderr.
type AppResult struct {
	Success    bool   `json:"success"`
	UpToDate   bool   `json:"up_to_date,omitempty"`
	NewVersion string `json:"new_version,omitempty"`
	// Restart tells the app what happens next: "quit" (the files are
	// replaced; quit and start again) or "relaunch" (a script starts the
	// app again after it quits).
	Restart string `json:"restart,omitempty"`
	// Daemon is what the update did about a running daemon (DaemonUpdate); absent when none ran
	// or it already ran this version.
	Daemon *DaemonUpdate `json:"daemon,omitempty"`
	// StaleCLIs are other mono-agent CLIs older than the release (the one on PATH, the one a daemon service starts): not
	// changed here, each with the command that updates it.
	StaleCLIs []StaleCLI `json:"stale_clis,omitempty"`
	// Bridge is what the update did about the extension bridge; absent when none ran or it already ran this version.
	Bridge *BridgeUpdate `json:"bridge,omitempty"`
	Error  string        `json:"error,omitempty"`
}

func (d Deps) runApp(cmd *cobra.Command, appPath, current string) error {
	progress := func(msg string) {
		if d.JSONOutput() {
			fmt.Fprintf(cmd.ErrOrStderr(), "{\"kind\":\"line\",\"message\":%q}\n", msg)
		} else {
			fmt.Fprintln(cmd.OutOrStdout(), msg)
		}
	}
	force, _ := cmd.Flags().GetBool("force")
	res, err := d.updateApp(cmd, appPath, current, force, progress)
	if err != nil {
		if !d.JSONOutput() {
			return err
		}
		res.Error = err.Error()
	} else if !res.UpToDate {
		// The files are replaced; a daemon still runs the old ones. The app relays this line.
		if res.Daemon = d.daemonAfterUpdate(cmd.Context(), res.NewVersion); res.Daemon != nil {
			progress(res.Daemon.Message)
		}
	}
	if err == nil && !res.UpToDate {
		if res.Bridge = d.bridgeAfterUpdate(cmd.Context(), res.NewVersion); res.Bridge != nil {
			progress(res.Bridge.Message)
		}
	}
	if err == nil {
		res.StaleCLIs = d.staleAfterApp(cmd.Context(), appPath, current, res)
		for _, s := range res.StaleCLIs {
			progress(s.Message)
		}
	}
	if d.JSONOutput() {
		return d.WriteJSON(cmd.OutOrStdout(), res)
	}
	if res.UpToDate {
		fmt.Fprintln(cmd.OutOrStdout(), "The app is already on the latest release.")
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "App updated to %s — restart it (%s).\n", res.NewVersion, res.Restart)
	}
	return nil
}

func (d Deps) updateApp(cmd *cobra.Command, appPath, current string, force bool, progress func(string)) (AppResult, error) {
	if appPath == "" {
		return AppResult{}, d.InvalidInput("--app needs the path of the desktop app's executable")
	}
	if abs, err := filepath.EvalSymlinks(appPath); err == nil {
		appPath = abs
	}
	u := appupdate.Updater{
		Context:       cmd.Context(),
		Client:        http.DefaultClient,
		APIURL:        d.LatestReleaseURL,
		Release:       d.ReleaseClient(),
		Current:       current,
		Force:         force,
		GOOS:          runtime.GOOS,
		GOARCH:        runtime.GOARCH,
		Exe:           appPath,
		Progress:      progress,
		StartDetached: d.StartDetached,
	}
	if current != "" {
		u.UpToDate = func(tag string) bool { return !newerRelease(tag, current) }
	}
	r, err := u.Run()
	if err != nil {
		return AppResult{}, err
	}
	return AppResult{Success: true, UpToDate: r.UpToDate, NewVersion: r.NewVersion, Restart: r.Restart}, nil
}

// staleAfterApp lists the CLIs the app update left behind: reported only, since the verified assets are the app's.
func (d Deps) staleAfterApp(ctx context.Context, appPath, current string, res AppResult) []StaleCLI {
	ver := res.NewVersion
	if ver == "" {
		ver = current
	}
	if ver == "" {
		return nil
	}
	skip := []string{}
	if exe, err := os.Executable(); err == nil {
		skip = append(skip, exe)
	}
	if abs, err := filepath.EvalSymlinks(appPath); err == nil {
		appPath = abs
	}
	if cli, ok := appupdate.SiblingCLI(filepath.Dir(appPath), runtime.GOOS, runtime.GOARCH); ok {
		skip = append(skip, cli)
	}
	return d.staleCLIs(ctx, ver, skip, nil)
}
