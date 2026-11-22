//go:build darwin

package autostart

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"time"
)

func newPlatformInstaller() Installer { return darwinInstaller{} }

type darwinInstaller struct{}

const darwinPlistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{{.Label}}</string>
	<key>ProgramArguments</key>
	<array>
		<string>{{.Exe}}</string>
		<string>daemon</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>ExitTimeOut</key>
	<integer>{{.ExitTimeOut}}</integer>
	<key>StandardOutPath</key>
	<string>{{.LogDir}}/daemon.log</string>
	<key>StandardErrorPath</key>
	<string>{{.LogDir}}/daemon.err.log</string>
</dict>
</plist>
`

// renderPlist returns the launchd job for the binary at exe, logging under logs. ExitTimeOut is
// stopGrace: launchd's default of 20 s is shorter than a daemon needs to finish a refresh.
// darwinLogNames are the plist's StandardOutPath and StandardErrorPath.
var darwinLogNames = []string{"daemon.log", "daemon.err.log"}

// serviceLogHint is where the launchd job's output goes.
func serviceLogHint() string {
	return "~/.monoagent/logs/daemon.log (and daemon.err.log)"
}

func renderPlist(exe, logs string) (string, error) {
	var b strings.Builder
	tmpl := template.Must(template.New("plist").Parse(darwinPlistTemplate))
	err := tmpl.Execute(&b, struct {
		Label, Exe, LogDir string
		ExitTimeOut        int
	}{Label, xmlText(exe), xmlText(logs), int(stopGrace / time.Second)})
	return b.String(), err
}

func plistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", Label+".plist"), nil
}

// launchdDomain is the per-user GUI domain launchctl's modern bootstrap/
// bootout subcommands operate on — the successor to `load -w`/`unload -w`,
// which Apple deprecated and which behaves inconsistently for agents
// started outside a full login (e.g. over SSH).
func launchdDomain() string {
	return "gui/" + strconv.Itoa(os.Getuid())
}

func (darwinInstaller) Install(ctx context.Context) (Result, error) {
	exe, err := executablePath()
	if err != nil {
		return Result{}, err
	}
	logs, err := logDir()
	if err != nil {
		return Result{}, err
	}
	path, err := plistPath()
	if err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Result{}, fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}

	plist, err := renderPlist(exe, logs)
	if err != nil {
		return Result{}, fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
		return Result{}, fmt.Errorf("write %s: %w", path, err)
	}

	domain := launchdDomain()
	// bootout first and ignore its error: it fails (harmlessly) whenever
	// nothing is loaded yet, which is the common case on a first install,
	// and re-running bootstrap over an already-loaded label otherwise
	// fails with "service already loaded".
	_ = exec.CommandContext(ctx, "launchctl", "bootout", domain+"/"+Label).Run()
	rotateDaemonLogs(darwinLogNames...)
	bootstrap := exec.CommandContext(ctx, "launchctl", "bootstrap", domain, path)
	if out, err := bootstrap.CombinedOutput(); err != nil {
		return Result{}, fmt.Errorf("launchctl bootstrap: %w: %s", err, string(out))
	}

	return Result{Description: fmt.Sprintf(
		"Installed %s — starts at login, restarts if it exits. Logs: %s/daemon.log (and daemon.err.log). "+
			"Inspect with: launchctl print %s/%s", path, logs, domain, Label,
	)}, nil
}

func (darwinInstaller) Uninstall(ctx context.Context) error {
	path, err := plistPath()
	if err != nil {
		return err
	}
	domain := launchdDomain()
	_ = exec.CommandContext(ctx, "launchctl", "bootout", domain+"/"+Label).Run()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

// launchctl runs launchctl and returns its combined output; tests replace
// it.
var launchctl = func(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "launchctl", args...).CombinedOutput()
}

// Status reports the agent installed only when its plist exists and
// launchd has it loaded (`launchctl print` knows the job) — a plist alone
// starts nothing until the next login, or never after a failed bootstrap
// or a `bootout`. When it doesn't count, where says why.
func (darwinInstaller) Status(ctx context.Context) (bool, string) {
	path, err := plistPath()
	if err != nil {
		return false, ""
	}
	if _, err := os.Stat(path); err != nil {
		return false, ""
	}
	target := launchdDomain() + "/" + Label
	if _, err := launchctl(ctx, "print", target); err != nil {
		return false, fmt.Sprintf("%s exists but launchd has not loaded it (launchctl print %s: %v) — run `monoagentcli daemon install` again",
			path, target, err)
	}
	return true, path
}

// Restart is launchd's own: kickstart -k kills the running instance of the job and starts it
// again (and starts a job that was not running).
func (darwinInstaller) Restart(ctx context.Context) error {
	target := launchdDomain() + "/" + Label
	rotateDaemonLogs(darwinLogNames...)
	if out, err := launchctl(ctx, "kickstart", "-k", target); err != nil {
		return fmt.Errorf("launchctl kickstart -k %s: %w: %s", target, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (darwinInstaller) Start(ctx context.Context) error {
	// kickstart starts a loaded job and is a no-op for a running one
	// (no -k: never restart a healthy daemon).
	target := launchdDomain() + "/" + Label
	rotateDaemonLogs(darwinLogNames...)
	out, err := exec.CommandContext(ctx, "launchctl", "kickstart", target).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl kickstart %s: %w: %s", target, err, string(out))
	}
	return nil
}

var launchdPIDLine = regexp.MustCompile(`(?m)^\s*pid = (\d+)\s*$`)

// MainPID is the pid launchd reports for the daemon's job (`launchctl print`); 0 when it has none.
func (darwinInstaller) MainPID(ctx context.Context) (int, error) {
	target := launchdDomain() + "/" + Label
	out, err := launchctl(ctx, "print", target)
	if err != nil {
		return 0, fmt.Errorf("launchctl print %s: %w", target, err)
	}
	if m := launchdPIDLine.FindSubmatch(out); m != nil {
		return strconv.Atoi(string(m[1]))
	}
	return 0, nil
}
