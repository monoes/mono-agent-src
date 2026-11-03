package update

import (
	"context"
	"encoding/json"
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
	cli       []byte     // the verified CLI of the release, when the app's download carried one (appupdate.Result.CLI)
	// Bridge is what the update did about the extension bridge; absent when none ran or it already ran this version.
	Bridge *BridgeUpdate `json:"bridge,omitempty"`
	Error  string        `json:"error,omitempty"`
}

// progressLine is one NDJSON progress line of --json: encoded as JSON, so a control character in a message (a path) is a valid
// escape and not Go's \x01.
func progressLine(msg string) string {
	b, _ := json.Marshal(struct {
		Kind    string `json:"kind"`
		Message string `json:"message"`
	}{"line", msg})
	return string(b)
}

func (d Deps) runApp(cmd *cobra.Command, appPath, current string) error {
	progress := func(msg string) {
		if d.JSONOutput() {
			fmt.Fprintln(cmd.ErrOrStderr(), progressLine(msg))
		} else {
			fmt.Fprintln(cmd.OutOrStdout(), msg)
		}
	}
	d.Say = progress
	force, _ := cmd.Flags().GetBool("force")
	res, err := d.updateApp(cmd, appPath, current, force, progress)
	if err != nil {
		if !d.JSONOutput() {
			return err
		}
		res.Error = err.Error()
	} else {
		// The files are replaced; the other CLIs of the machine (the one a daemon service starts, the one on PATH) are brought
		// onto the same verified release, then the daemon and the bridge are restarted onto them, then the old binaries are
		// dropped or, if the daemon did not come up, put back. The app relays each line.
		stale := d.staleAfterApp(cmd.Context(), appPath, current, res)
		if !res.UpToDate {
			res.Daemon = d.daemonNotReplaced(cmd.Context(), res.NewVersion, stale, appSibling(appPath))
			if res.Daemon == nil {
				res.Daemon = d.settleDaemon(cmd.Context(), stale, d.daemonAfterUpdate(cmd.Context(), res.NewVersion))
			}
			res.Bridge = d.bridgeAfterUpdate(cmd.Context(), res.NewVersion, bridgeSkipReason(res.Daemon))
			d.settleBridge(stale, res.Bridge)
		}
		res.StaleCLIs = stale
		for _, s := range stale {
			progress(s.Message)
		}
		if res.Daemon != nil {
			progress(res.Daemon.Message)
		}
		if res.Bridge != nil {
			progress(res.Bridge.Message)
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
	return AppResult{Success: true, UpToDate: r.UpToDate, NewVersion: r.NewVersion, Restart: r.Restart, cli: r.CLI}, nil
}

// appSibling is the CLI beside the app, which the app update itself brought onto the release (none where the app holds none).
func appSibling(appPath string) []string {
	if abs, err := filepath.EvalSymlinks(appPath); err == nil {
		appPath = abs
	}
	if cli, ok := appupdate.SiblingCLI(filepath.Dir(appPath), runtime.GOOS, runtime.GOARCH); ok {
		return []string{cli}
	}
	return nil
}

// appSkip are the paths staleCLIs does not list: the CLI run now and the one beside the app.
func appSkip(appPath string) []string {
	skip := []string{}
	if exe, err := os.Executable(); err == nil {
		skip = append(skip, exe)
	}
	return append(skip, appSibling(appPath)...)
}

// staleAfterApp brings the CLIs the app update left behind onto the release: the daemon service's exe and the one on PATH are
// replaced, by the rules of staleCLIs, from the CLI inside the verified download (never from anything else, and nothing is
// executed but the new binary's own version check); where the download held no CLI they are only reported.
func (d Deps) staleAfterApp(ctx context.Context, appPath, current string, res AppResult) []StaleCLI {
	// replaced only when the release is new (not when the app was already current) and its download held a CLI
	ver := res.NewVersion
	if ver == "" {
		ver = current
	}
	if ver == "" {
		return nil
	}
	skip := appSkip(appPath)
	var data []byte
	if !res.UpToDate {
		data = res.cli
	}
	return d.staleCLIs(ctx, ver, skip, data)
}
