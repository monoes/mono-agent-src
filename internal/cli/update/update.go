// Package update is `monoagentcli update`: the CLI's self-update, the
// desktop app's update (--app) and the restart of a daemon left on the old
// version. Everything the command needs from the CLI around it comes in
// through Deps, so it can be tested in isolation.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/autostart"
	"github.com/monoes/mono-agent/internal/daemonhb"
	"github.com/monoes/mono-agent/internal/release"
	"github.com/monoes/mono-agent/internal/updatecheck"
	"github.com/spf13/cobra"
)

// SumsAssetName is the checksum manifest published with every
// release (see .github/workflows/release.yml "Flatten and checksum").
const SumsAssetName = updatecheck.SumsAssetName

// Deps is what the update command takes from the CLI around it.
type Deps struct {
	// JSONOutput reports the global --json flag; it is read when the command runs.
	JSONOutput func() bool
	// Version is this binary's version ("dev" for a build with none).
	Version func() string
	// ReleaseClient builds the signed-manifest client.
	ReleaseClient func() *release.Client
	// LatestReleaseURL is GitHub's latest-release endpoint (the legacy path).
	LatestReleaseURL string
	// InvalidInput builds the CLI's invalid-input error (exit code 3).
	InvalidInput func(format string, a ...interface{}) error
	// WriteJSON writes the --json result.
	WriteJSON func(w io.Writer, v any) error
	// StartDetached starts a process that outlives this one (update --app's relaunch).
	StartDetached func(name string, args ...string) error

	// The daemon restart after an update (daemonAfterUpdate).

	// Heartbeat reads this home's daemon heartbeat; nil: daemonhb.Read.
	Heartbeat func() (daemonhb.Heartbeat, bool)
	// RestartBlockers reads the database the command is pointed at, opened without migrating it:
	// how many executions the daemon with this pid owns, which a restart ends, and whether the
	// settings saved with `api config` can be used. A database that cannot be read is err.
	RestartBlockers func(ctx context.Context, pid int) (inFlight int, settingsErr, err error)
	// Installer is the auto-start service manager.
	Installer func() autostart.Installer
	// LookPath finds a program on PATH; nil: exec.LookPath. ProbeVersion asks a CLI for its version in a scratch
	// environment; nil: it runs `<path> --json version` there (stale.go).
	// SelfPath is the path of the binary an update replaces; nil: where this process was started from.
	SelfPath     func() (string, error)
	LookPath     func(name string) (string, error)
	ProbeVersion func(ctx context.Context, path string) (string, error)
	// ServicePID is the pid of the process the registered service runs; nil: autostart.ServiceMainPID.
	ServicePID func(ctx context.Context, in autostart.Installer) (int, error)
	// VerifyWait and VerifyTick bound the wait for the restarted daemon's heartbeat; zero: 15s and 250ms.
	VerifyWait, VerifyTick time.Duration
}

// NewCommand is `monoagentcli update`.
func NewCommand(d Deps) *cobra.Command {
	var check, force bool
	var current, app string
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update monoagentcli to the latest release",
		Long: "Downloads the latest release, verifies it against the release's SHA256SUMS.txt and replaces this binary.\n\n" +
			"--check only reports whether a newer release exists (nothing is downloaded). --current compares against another " +
			"version instead of this binary's — the desktop app asks about its own version this way.\n\n" +
			"--app updates the desktop app at that executable path, and the CLI bundled with it, instead of this binary: " +
			"every asset is verified against SHA256SUMS.txt and the files are swapped all-or-nothing. With --json, progress is " +
			"NDJSON on stderr ({\"kind\":\"line\",\"message\":…}) and the result is one JSON object on stdout.\n\n" +
			"A running daemon keeps the code it started with, so update restarts one that runs another version, through the " +
			"service manager it is registered with, unless an execution is in flight or the saved `api config` settings cannot be used: " +
			"it then says so and leaves the restart to `monoagentcli daemon restart`.",
		Example: `  monoagentcli update
  monoagentcli --json update --check
  monoagentcli --json update --check --current v0.72.0
  monoagentcli --json update --app /opt/MonoAgent/MonoAgent-linux-amd64 --current v0.72.0`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if check && app != "" {
				return d.InvalidInput("--check and --app are separate: check with --check, install with --app")
			}
			if check {
				return d.runCheck(cmd, current)
			}
			if app != "" {
				return d.runApp(cmd, app, current)
			}
			if current != "" {
				return d.InvalidInput("--current goes with --check or --app")
			}
			return d.run(cmd)
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "Only report whether a newer release exists; download nothing")
	cmd.Flags().StringVar(&current, "current", "", "Version to compare against (default: this binary's)")
	cmd.Flags().BoolVar(&force, "force", false, "Install a release older than the installed one (downgrade)")
	cmd.Flags().StringVar(&app, "app", "", "Update the desktop app at this executable path (and the CLI bundled with it) instead of this binary")
	return cmd
}

// Check is `update --check --json`.
type Check struct {
	CurrentVersion  string `json:"current_version"`
	LatestVersion   string `json:"latest_version"`
	UpdateAvailable bool   `json:"update_available"`
	ReleaseURL      string `json:"release_url"`
	Error           string `json:"error,omitempty"`
}

// runUpdateCheck reports the latest release. A release that cannot be
// fetched is a result (error field, exit 0) in --json mode — the caller is
// polling, not failing.
func (d Deps) runCheck(cmd *cobra.Command, current string) error {
	if current == "" {
		current = d.Version()
	}
	res := Check{CurrentVersion: current}
	force, _ := cmd.Flags().GetBool("force")
	release, err := FetchLatest(cmd.Context(), d.ReleaseClient(), d.LatestReleaseURL, force)
	if err != nil {
		if !d.JSONOutput() {
			return err
		}
		res.Error = err.Error()
		return d.WriteJSON(cmd.OutOrStdout(), res)
	}
	res.LatestVersion = release.Tag
	res.ReleaseURL = release.URL
	res.UpdateAvailable = newerRelease(release.Tag, current)
	if d.JSONOutput() {
		return d.WriteJSON(cmd.OutOrStdout(), res)
	}
	out := cmd.OutOrStdout()
	switch {
	case res.UpdateAvailable:
		fmt.Fprintf(out, "Update available: %s → %s\n%s\n", current, release.Tag, release.URL)
	case IsDevVersion(current):
		fmt.Fprintf(out, "Latest release is %s (this is a dev build)\n", release.Tag)
	default:
		fmt.Fprintf(out, "Already on the latest version (%s)\n", current)
	}
	return nil
}

// IsDevVersion reports a build with no release version: "dev", or a git
// describe of a commit past a tag ("v0.72.0-3-gabc123").
func IsDevVersion(v string) bool {
	v = strings.TrimSpace(v)
	return v == "" || v == "dev" || strings.Contains(v, "-g")
}

// newerRelease reports whether latest is a newer release than current,
// comparing dotted numbers (v0.10.0 > v0.9.9). Dev builds never are.
func newerRelease(latest, current string) bool {
	if IsDevVersion(current) || latest == "" {
		return false
	}
	a := strings.Split(strings.TrimPrefix(strings.TrimSpace(latest), "v"), ".")
	b := strings.Split(strings.TrimPrefix(strings.TrimSpace(current), "v"), ".")
	for i := 0; i < len(a) || i < len(b); i++ {
		x, y := versionPart(a, i), versionPart(b, i)
		if x != y {
			return x > y
		}
	}
	return false
}

func versionPart(parts []string, i int) int {
	if i >= len(parts) {
		return 0
	}
	n := 0
	for _, r := range parts[i] {
		if r < '0' || r > '9' {
			break // "0-rc1" compares as 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func (d Deps) run(cmd *cobra.Command) error {
	fmt.Println("Checking for updates...")
	force, _ := cmd.Flags().GetBool("force")

	info, err := FetchLatest(context.Background(), d.ReleaseClient(), d.LatestReleaseURL, force)
	if err != nil {
		return err
	}

	v := d.Version()
	latest := strings.TrimPrefix(info.Tag, "v")
	current := strings.TrimPrefix(v, "v")

	if v == "dev" {
		fmt.Println("Running a dev build (no embedded version) — skipping update check.")
		return nil
	}
	if latest == current {
		fmt.Printf("Already on latest version (%s)\n", v)
		d.printStale(d.staleCLIs(cmd.Context(), info.Tag, d.selfSkip(selfPathOrEmpty()), nil))
		// A daemon started before this binary was installed still runs the old code.
		if du := d.daemonAfterUpdate(cmd.Context(), v); du != nil {
			fmt.Println(du.Message)
		}
		return nil
	}
	if err := release.CheckDowngrade(info.Tag, v, force); err != nil {
		return fmt.Errorf("%w; the installed binary was kept", err)
	}
	fmt.Printf("Update available: %s → %s\n", v, info.Tag)

	assetName := updateAssetName()
	selfBinary := selfBinaryPath
	if d.SelfPath != nil {
		selfBinary = d.SelfPath
	}
	selfPath, err := selfBinary()
	if err != nil {
		return fmt.Errorf("locate binary: %w", err)
	}

	var data []byte
	if info.Manifest != nil {
		// Signed path: the manifest signature already passed; the download
		// must match its size and sha256 before anything is replaced.
		a, err := info.Manifest.Asset(assetName, runtime.GOOS, runtime.GOARCH, "cli")
		if err != nil {
			return err
		}
		fmt.Printf("Downloading %s...\n", assetName)
		if data, err = d.ReleaseClient().Download(context.Background(), a); err != nil {
			return fmt.Errorf("%w; the installed binary was kept", err)
		}
		fmt.Printf("Signature and checksum verified: %s SHA256 %s\n", assetName, a.SHA256)
	} else {
		if data, err = legacyDownload(info.Legacy, assetName); err != nil {
			return err
		}
	}

	if err := installBinary(data, selfPath); err != nil {
		return err
	}

	fmt.Printf("Updated to %s\n", info.Tag)
	// Other mono-agent CLIs (the one a daemon service starts, the one on PATH) are still old: the daemon's own is replaced
	// from the verified asset, so that the restart below starts the new code; the rest is reported with its command.
	stale := d.staleCLIs(cmd.Context(), info.Tag, d.selfSkip(selfPath), data)
	d.printStale(stale)
	du := d.settleReplaced(cmd.Context(), stale, d.daemonAfterUpdate(cmd.Context(), info.Tag))
	if du != nil {
		fmt.Println(du.Message)
	}
	return nil
}

// legacyDownload is the pre-manifest path: the GitHub release's asset checked
// against the release's SHA256SUMS.txt.
func legacyDownload(rel *LatestRelease, assetName string) ([]byte, error) {
	var downloadURL string
	for _, a := range rel.Assets {
		if a.Name == assetName {
			downloadURL = a.BrowserDownloadURL
			break
		}
	}
	if downloadURL == "" {
		names := make([]string, 0, len(rel.Assets))
		for _, a := range rel.Assets {
			names = append(names, a.Name)
		}
		available := "(none)"
		if len(names) > 0 {
			available = strings.Join(names, ", ")
		}
		// Hard error, no architecture fallback: silently downloading an
		// amd64 binary onto an arm64 host bricks the update.
		return nil, fmt.Errorf("no binary for %s/%s in release %s (wanted %s); available assets: %s",
			runtime.GOOS, runtime.GOARCH, rel.TagName, assetName, available)
	}

	// Pre-flight: the release must publish a checksum manifest, otherwise
	// the downloaded binary cannot be verified. Hard-fail like install.sh —
	// never silently skip integrity verification.
	sumsURL := ""
	for _, a := range rel.Assets {
		if a.Name == SumsAssetName {
			sumsURL = a.BrowserDownloadURL
			break
		}
	}
	if sumsURL == "" {
		return nil, fmt.Errorf("release %s has no %s asset — cannot verify download integrity, refusing to update", rel.TagName, SumsAssetName)
	}

	fmt.Printf("Downloading %s...\n", assetName)
	data, err := HTTPGetAll(downloadURL)
	if err != nil {
		return nil, err
	}
	sumsData, err := HTTPGetAll(sumsURL)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", SumsAssetName, err)
	}
	if err := verifyReleaseDigest(data, sumsData, assetName); err != nil {
		return nil, err
	}
	fmt.Printf("Checksum verified: %s SHA256 %s\n", assetName, sha256Hex(data))
	return data, nil
}

// LatestRelease is the part of GitHub's latest-release payload we use.
type LatestRelease struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

// ReleasesRepo is the public repository the release assets are published to
// (owner/name); every release URL of the CLI is built from it. It is not the
// repository the code is built in (which may be private), and it must equal
// internal/release.ReleasesRepo.
const ReleasesRepo = "monoes/mono-agent"

// LatestReleaseURL is GitHub's latest-release endpoint.
const LatestReleaseURL = "https://api.github.com/repos/" + ReleasesRepo + "/releases/latest"

// FetchLatestRelease asks GitHub (url: its latest-release endpoint) for mono-agent's latest release.
func FetchLatestRelease(ctx context.Context, url string) (*LatestRelease, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("network error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var release LatestRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return &release, nil
}

// HTTPGetAll fetches url and returns the full response body. Non-200
// statuses are errors.
func HTTPGetAll(url string) ([]byte, error) {
	resp, err := http.Get(url) //nolint:gosec
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("download failed: GitHub returned %d for %s", resp.StatusCode, url)
	}
	return io.ReadAll(resp.Body)
}

// The checksum helpers live in internal/updatecheck, shared with the
// desktop app's updater (wails-app/updater.go).
var (
	sha256Hex           = updatecheck.SHA256Hex
	verifyReleaseDigest = updatecheck.VerifyReleaseDigest
)

// updateAssetName returns the release asset name for the running
// platform: monoagentcli-<GOOS>-<GOARCH> (plus ".exe" on windows) —
// uniform across platforms, with no implicit amd64 fallback.
func updateAssetName() string {
	return updateAssetNameFor(runtime.GOOS, runtime.GOARCH)
}

func updateAssetNameFor(goos, goarch string) string {
	name := "monoagentcli-" + goos + "-" + goarch
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

func selfPathOrEmpty() string {
	p, _ := selfBinaryPath()
	return p
}

// selfSkip is the paths an update of this binary has just dealt with: this binary, as it is reached and as it really is.
func (d Deps) selfSkip(self string) []string {
	skip := []string{self}
	if exe, err := os.Executable(); err == nil {
		skip = append(skip, exe)
	}
	return skip
}

func (d Deps) printStale(list []StaleCLI) {
	for _, s := range list {
		fmt.Println(s.Message)
	}
}

func selfBinaryPath() (string, error) {
	// Try resolving via argv[0] first (most reliable for installed binaries)
	arg0 := os.Args[0]
	if p, err := exec.LookPath(arg0); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs, nil
		}
	}
	if abs, err := filepath.Abs(arg0); err == nil {
		if _, err := os.Stat(abs); err == nil {
			return abs, nil
		}
	}
	return "", fmt.Errorf("cannot locate own binary (argv[0]=%s)", arg0)
}

// installBinary atomically replaces target with data. The temp file is
// created next to target, not in os.TempDir: /tmp is often a separate
// filesystem (tmpfs), and a rename across filesystems fails with
// "invalid cross-device link".
func installBinary(data []byte, target string) error { return installBinaryChecked(data, target, nil) }

// installBinaryChecked is installBinary that first runs check on the new file, next to the target and executable, and
// installs it only if check passes: a new binary that cannot run never takes the old one's place.
func installBinaryChecked(data []byte, target string, check func(tmp string) error) error {
	r, err := installBinaryKeep(data, target, check)
	if err != nil {
		return err
	}
	r.Commit()
	return nil
}

// replaced is a binary that was swapped in, with the one it replaced kept under a name of its own (never a file of the
// user's) until the caller has seen the new one work: Commit then drops it, Restore puts it back.
type replaced struct{ target, bak string }

// Commit forgets the old binary.
func (r *replaced) Commit() { _ = os.Remove(r.bak) }

// Restore puts the old binary back in the new one's place (one rename, so the target is never missing).
func (r *replaced) Restore() error {
	if err := os.Rename(r.bak, r.target); err != nil {
		return fmt.Errorf("restoring %s from %s: %w", r.target, r.bak, err)
	}
	return nil
}

// installBinaryKeep is installBinaryChecked that keeps the old binary until Commit or Restore is called.
func installBinaryKeep(data []byte, target string, check func(tmp string) error) (*replaced, error) {
	tmp, err := os.CreateTemp(filepath.Dir(target), ".monoagentcli-update-*")
	if err != nil {
		return nil, fmt.Errorf("temp file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return nil, fmt.Errorf("write download: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return nil, fmt.Errorf("write download: %w", err)
	}
	if err := os.Chmod(tmpPath, 0755); err != nil {
		os.Remove(tmpPath)
		return nil, fmt.Errorf("chmod: %w", err)
	}
	if check != nil {
		if err := check(tmpPath); err != nil {
			os.Remove(tmpPath)
			return nil, fmt.Errorf("the new binary did not pass its check: %w", err)
		}
	}

	// the old binary moves to a name nobody else uses: a file the user keeps as <target>.bak is not ours to delete
	bakFile, err := os.CreateTemp(filepath.Dir(target), ".monoagentcli-previous-*")
	if err != nil {
		os.Remove(tmpPath)
		return nil, fmt.Errorf("backup: %w", err)
	}
	bak := bakFile.Name()
	bakFile.Close()
	if err := os.Rename(target, bak); err != nil {
		os.Remove(tmpPath)
		os.Remove(bak)
		return nil, fmt.Errorf("backup: %w", err)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		os.Rename(bak, target) // rollback
		os.Remove(tmpPath)
		return nil, fmt.Errorf("install: %w", err)
	}
	return &replaced{target: target, bak: bak}, nil
}
