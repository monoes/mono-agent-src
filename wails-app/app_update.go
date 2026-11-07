package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// AppSelfUpdate updates the desktop app — and the CLI bundled next to it —
// from the latest release, then quits (restarting it on macOS/Windows).
//
// The installer lives in the CLI (`monoagentcli update --app`,
// internal/appupdate): it verifies every asset against SHA256SUMS.txt and
// swaps the files all-or-nothing. The app only names its own executable,
// relays the progress as "update:progress" and quits once it is installed.
// The CLI that runs is usually the bundled one, which replaces itself; that
// is safe because it keeps running from the old file until it exits.
func (a *App) AppSelfUpdate() UpdateResult {
	exe, err := os.Executable()
	if err != nil {
		return UpdateResult{Error: "cannot determine executable path"}
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	cliBin, err := findMonoAgentCLI()
	if err != nil {
		return UpdateResult{Error: err.Error()}
	}
	return runAppUpdate(a.ctx, cliBin, exe, version,
		func(msg string) { eventsEmit(a.ctx, "update:progress", msg) },
		a.quitSoon)
}

// quitSoon quits the app a moment after the update result has been returned to the window.
func (a *App) quitSoon() {
	go func() {
		time.Sleep(300 * time.Millisecond)
		runtime.Quit(a.ctx)
	}()
}

// QuitApp quits the app: the "Restart now" of an update that came with a notice, which did not quit by itself so that the
// person could read it first. The restart script of the update waits for the app to exit.
func (a *App) QuitApp() {
	a.quitSoon()
}

// appUpdateTimeout bounds `update --app` (a release download).
const appUpdateTimeout = 15 * time.Minute

// cliAppUpdate is `monoagentcli --json update --app` on stdout.
type cliAppUpdate struct {
	Success    bool   `json:"success"`
	UpToDate   bool   `json:"up_to_date"`
	NewVersion string `json:"new_version"`
	Error      string `json:"error"`
	// What the update did about the daemon, the extension bridge and the other CLIs of the machine (each with a message).
	Daemon    *cliNote  `json:"daemon"`
	Bridge    *cliNote  `json:"bridge"`
	StaleCLIs []cliNote `json:"stale_clis"`
}

// cliNote is the part of a daemon, bridge or stale-CLI entry the notice needs.
type cliNote struct {
	Action  string `json:"action"`
	Message string `json:"message"`
}

// updateNotice is what the person is told after an update, built from what the CLI says it did about the daemon, the bridge
// and the other CLIs: everything but the outcomes that need no words (restarted, updated). Empty when all went as it should.
func updateNotice(res cliAppUpdate) string {
	var lines []string
	add := func(n *cliNote, quiet ...string) {
		if n == nil || strings.TrimSpace(n.Message) == "" {
			return
		}
		for _, q := range quiet {
			if n.Action == q {
				return
			}
		}
		lines = append(lines, strings.TrimSpace(n.Message))
	}
	add(res.Daemon, "restarted")
	add(res.Bridge, "restarted")
	for i := range res.StaleCLIs {
		add(&res.StaleCLIs[i], "updated")
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\nThe update is installed; restart the app to use it."
}

// runAppUpdate runs `monoagentcli --json update --app <exe> --current <v>`,
// forwards each NDJSON progress line on its stderr through progress and
// calls quit after a successful install (not when already up to date). An install that comes with a notice (the daemon or the
// bridge could not be restarted, another CLI is still old) does not quit by itself: the notice is returned and the person
// quits with QuitApp once they have read it.
func runAppUpdate(ctx context.Context, cliBin, exe, current string, progress func(string), quit func()) UpdateResult {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, appUpdateTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, cliBin, "--json", "update", "--app", exe, "--current", current)
	suppressConsole(cmd)
	var stdout strings.Builder
	cmd.Stdout = &stdout
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return UpdateResult{Error: err.Error()}
	}
	if err := cmd.Start(); err != nil {
		return UpdateResult{Error: fmt.Sprintf("run %s: %v", cliBin, err)}
	}
	// Anything on stderr that isn't a progress record is kept for the
	// error message of a CLI that fails without a result.
	var other []string
	sc := bufio.NewScanner(stderr)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var ev struct{ Kind, Message string }
		if json.Unmarshal(sc.Bytes(), &ev) == nil && ev.Kind == "line" {
			progress(ev.Message)
			continue
		}
		if line := strings.TrimSpace(sc.Text()); line != "" {
			other = append(other, line)
		}
	}
	runErr := cmd.Wait()

	var res cliAppUpdate
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout.String())), &res); err != nil {
		msg := strings.Join(other, "\n")
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" && runErr != nil {
			msg = runErr.Error()
		}
		if msg == "" {
			msg = "no result"
		}
		return UpdateResult{Error: "app update: " + msg}
	}
	if !res.Success {
		if res.Error == "" {
			res.Error = "app update failed"
			if runErr != nil {
				res.Error += ": " + runErr.Error()
			}
		}
		return UpdateResult{Error: res.Error}
	}
	if runErr != nil {
		return UpdateResult{Error: fmt.Sprintf("app update: %v", runErr)}
	}
	if res.UpToDate {
		return UpdateResult{Success: true, UpToDate: true, NewVersion: current}
	}
	if notice := updateNotice(res); notice != "" {
		return UpdateResult{Success: true, NewVersion: res.NewVersion, Notice: notice}
	}
	quit()
	return UpdateResult{Success: true, NewVersion: res.NewVersion}
}
