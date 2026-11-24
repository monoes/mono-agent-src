// Package crashreport records a panic of monoagentcli: in a local file, or, opted in, filed to
// GitHub through the `monomind` CLI. main calls Report from its deferred recover().
package crashreport

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/statedir"
)

// crashReportOptIn enables GitHub crash filing via the sibling `monomind`
// CLI. Default (unset or any value other than "1"): the crash record is
// written to a local file only — nothing ever leaves the machine, and no
// subprocess is spawned.
const crashReportOptIn = "MONOAGENT_CRASH_REPORT"

// Build is the binary a crash happened in.
type Build struct {
	Version   string // the CLI's version
	BuildDate string // when it was built
	Repo      string // the GitHub repo crashes are filed to (the public releases repo)
}

// Report is called from a deferred recover() in main(). It never panics
// itself and never blocks longer than a few seconds — it's an observability
// side effect, not something that should change crash behavior for the user.
//
// Default: append the crash record (timestamp, version, panic title, stack)
// to $HOME/.monoagent/crashes/crash-<unixts>.log (best-effort; failures are
// reported on stderr and swallowed).
//
// Opt-in: when MONOAGENT_CRASH_REPORT=1 AND `monomind` is already installed
// (resolved and pinned via monomind.Find), the crash is filed to GitHub
// through `monomind report-crash` so redaction, dedup against existing issues, and
// auth (gh CLI / GITHUB_TOKEN) live in one place. There is no auto-download
// fallback: npx is never invoked.
func Report(panicValue interface{}, stack []byte, b Build) {
	title := Scrub(fmt.Sprintf("panic: %v", panicValue))
	body := Scrub(fmt.Sprintf("Uncaught panic in `monoagentcli`.\n\nVersion: %s (built %s)\n\n```\n%s\n```\n", b.Version, b.BuildDate, stack))

	if os.Getenv(crashReportOptIn) == "1" {
		if monomindPath, err := monomind.Find(); err == nil {
			if reportViaMonomind(monomindPath, b.Repo, title, body) {
				return
			}
		}
	}
	saveCrashLocally(title, body, b)
}

// reportViaMonomind returns false if the invocation failed (e.g. an older
// monomind on PATH predates the `report-crash` command) so the caller can
// fall back rather than silently swallowing the crash report.
func reportViaMonomind(monomindPath, repo, title, body string) bool {
	cmd := monomind.Command(monomindPath, "report-crash", "--repo", repo, "--title", title, "--body", body)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return runWithTimeout(cmd, 15*time.Second) == nil
}

func runWithTimeout(cmd *exec.Cmd, timeout time.Duration) error {
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		return fmt.Errorf("crash report timed out after %s", timeout)
	}
}

// saveCrashLocally appends the crash record to a per-crash log file under
// $HOME/.monoagent/crashes/. Best-effort: any failure is reported on stderr
// and returned from silently — this must never re-panic or mask the
// original crash.
func saveCrashLocally(title, body string, b Build) {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[monoagentcli] crash occurred, and couldn't save a report (no home dir): %s\n", title)
		return
	}
	crashDir := filepath.Join(home, ".monoagent", "crashes")
	if err := statedir.MkdirAll(crashDir); err != nil {
		return
	}
	path := filepath.Join(crashDir, fmt.Sprintf("crash-%d.log", time.Now().Unix()))
	record := fmt.Sprintf("timestamp: %s\nversion: %s (built %s)\ntitle: %s\n\n%s\n",
		time.Now().UTC().Format(time.RFC3339), b.Version, b.BuildDate, title, body)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, statedir.FilePerm)
	if err != nil {
		return
	}
	defer f.Close()
	if _, err := f.WriteString(record); err != nil {
		return
	}
	fmt.Fprintf(os.Stderr, "[monoagentcli] crash report saved to %s (set MONOAGENT_CRASH_REPORT=1 to file via monomind)\n", path)
}
