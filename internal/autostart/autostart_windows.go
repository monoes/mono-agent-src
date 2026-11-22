//go:build windows

package autostart

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/daemonhb"
)

func newPlatformInstaller() Installer { return windowsInstaller{} }

type windowsInstaller struct{}

const taskName = "MonoAgentDaemon"

// serviceLogHint is where the scheduled task's >> redirect writes.
func serviceLogHint() string {
	return `%USERPROFILE%\.monoagent\logs\daemon.log`
}

func (windowsInstaller) Install(ctx context.Context) (Result, error) {
	exe, err := executablePath()
	if err != nil {
		return Result{}, err
	}
	logs, err := logDir()
	if err != nil {
		return Result{}, err
	}
	logPath := filepath.Join(logs, "daemon.log")

	// schtasks has no native stdout/stderr redirection (unlike the plist's
	// StandardOutPath or systemd's journal), so the task runs a cmd
	// wrapper that appends both streams to a file instead. /rl limited
	// avoids requiring admin rights for a per-user task.
	if err := checkTaskPath(exe); err != nil {
		return Result{}, err
	}
	if err := checkTaskPath(logPath); err != nil {
		return Result{}, err
	}
	trArg := taskCommand(exe, logPath)

	cmd := exec.CommandContext(ctx, "schtasks", "/create", "/tn", taskName,
		"/sc", "onlogon", "/rl", "limited", "/f", "/tr", trArg)
	if out, err := cmd.CombinedOutput(); err != nil {
		return Result{}, fmt.Errorf("schtasks /create: %w: %s", err, string(out))
	}

	// /create only schedules it for the next logon; start it now too so
	// install has the same "running immediately" effect on every platform.
	rotateDaemonLogs("daemon.log")
	_ = exec.CommandContext(ctx, "schtasks", "/run", "/tn", taskName).Run()

	return Result{Description: fmt.Sprintf(
		"Installed scheduled task %q — starts at your next logon. Logs: %s. Inspect with: schtasks /query /tn %s /v",
		taskName, logPath, taskName,
	)}, nil
}

func (windowsInstaller) Uninstall(ctx context.Context) error {
	// Stop the running daemon first, as systemctl disable --now and
	// launchctl bootout do: deleting the task alone leaves it running
	// until the next logoff. Fails harmlessly when it is not running.
	_ = exec.CommandContext(ctx, "schtasks", "/end", "/tn", taskName).Run()
	cmd := exec.CommandContext(ctx, "schtasks", "/delete", "/tn", taskName, "/f")
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.ToLower(string(out))
		if strings.Contains(msg, "cannot find") || strings.Contains(msg, "does not exist") {
			return nil // nothing was installed — uninstall is a no-op, not an error
		}
		return fmt.Errorf("schtasks /delete: %w: %s", err, string(out))
	}
	return nil
}

func (windowsInstaller) Status(ctx context.Context) (bool, string) {
	err := exec.CommandContext(ctx, "schtasks", "/query", "/tn", taskName).Run()
	return err == nil, "scheduled task " + taskName
}

// schtasks runs schtasks and returns its combined output; tests replace it.
var schtasks = func(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "schtasks", args...).CombinedOutput()
}

// daemonStopWait is how long Restart waits for the daemon to let go of its lock after its task
// was ended. A scheduled task has no stop time of its own; this follows the other managers' stopGrace.
const daemonStopWait = stopGrace

// waitDaemonStopped waits for no daemon to hold the home's lock; tests replace it.
var waitDaemonStopped = func(ctx context.Context) error {
	if err := waitFor(ctx, func() bool { return !daemonhb.Locked() }, daemonStopWait, 200*time.Millisecond); err != nil {
		return fmt.Errorf("the daemon was still running %v after its task was ended (schtasks /end): %w", daemonStopWait, err)
	}
	return nil
}

// Restart ends the task, waits for the daemon to stop and runs the task again: the task has
// a logon trigger and no keep-alive, so a /run that raced the old process would leave no daemon.
func (windowsInstaller) Restart(ctx context.Context) error {
	// The log is rotated once the old daemon has let go of it.
	stoppedThenRotate := func(ctx context.Context) error {
		if err := waitDaemonStopped(ctx); err != nil {
			return err
		}
		rotateDaemonLogs("daemon.log")
		return nil
	}
	return endThenRun(ctx, schtasks, stoppedThenRotate, taskName)
}

func (windowsInstaller) Start(ctx context.Context) error {
	rotateDaemonLogs("daemon.log")
	out, err := exec.CommandContext(ctx, "schtasks", "/run", "/tn", taskName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("schtasks /run: %w: %s", err, string(out))
	}
	return nil
}

// MainPID: a scheduled task does not report its process in a form that is checked here.
func (windowsInstaller) MainPID(context.Context) (int, error) { return 0, ErrPIDUnverifiable }

// taskCommand is the Scheduled Task's command line: the daemon, output appended to logPath.
func taskCommand(exe, logPath string) string {
	return fmt.Sprintf(`cmd /c ""%s" daemon >> "%s" 2>&1"`, exe, logPath)
}
