// Package appupdate installs the desktop app — and the CLI bundled with it —
// from the latest release (the signed manifest first; the GitHub API and
// SHA256SUMS.txt only while release.AllowLegacyGitHubUpdates). `monoagentcli update --app` runs it; the desktop
// app only shells out to that command and shows its progress.
//
// Every asset is verified against the release's SHA256SUMS.txt before
// anything is written next to the app (internal/updatecheck, the same check
// `monoagentcli update` makes); a missing manifest or entry, a mismatch, a
// non-200 download or a fetch failure leaves the installation untouched.
// Files are staged next to their targets (same filesystem, so the final
// renames cannot fail with "invalid cross-device link") and swapped in
// with a rollback if any rename fails.
//
// The CLI that runs the update is usually the bundled one, so it replaces
// itself: on Unix the rename leaves the running process on the old inode;
// on Windows a running exe can be renamed but not deleted, so the old
// copies stay as *.bak until the restart script removes them.
package appupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/monoes/mono-agent/internal/release"
	"github.com/monoes/mono-agent/internal/updatecheck"
)

// Updater updates the app at Exe for GOOS/GOARCH from the release at APIURL.
// Everything OS-specific is a field, so tests run every OS path against a
// fake release server.
type Updater struct {
	Context context.Context // nil: context.Background()
	Client  *http.Client    // nil: http.DefaultClient
	APIURL  string          // GitHub's latest-release endpoint (legacy path)
	// Release is the signed-manifest client; nil: release.DefaultClient().
	Release *release.Client
	// Current is the installed version for the downgrade check ("" skips it);
	// Force allows installing an older release.
	Current      string
	Force        bool
	GOOS, GOARCH string
	Exe          string // the app binary, symlinks resolved
	// UpToDate reports whether the release tag is not newer than the app;
	// nil installs whatever the latest release is.
	UpToDate func(tag string) bool
	Progress func(string)
	// StartDetached launches the post-quit restart (macOS/Windows); it
	// must outlive the process that calls it.
	StartDetached func(name string, args ...string) error
}

// Result is a finished update. Restart tells the app what happens next:
// "quit" (the files are replaced; quit and start it again) or "relaunch"
// (a detached script starts the app again once it has quit).
type Result struct {
	UpToDate   bool
	NewVersion string
	Restart    string
	// CLI is the CLI of this platform from the release, as the verified download holds it (Linux: the tarball's
	// monoagentcli), so that the caller can bring other copies of the CLI onto the same release without downloading
	// or trusting anything else. Nil where the app's download carries none (macOS, Windows: reported only).
	CLI []byte
}

// AssetNameFor is the release asset of the desktop app on goos/goarch
// ("" when no desktop build is published for it).
func AssetNameFor(goos, goarch string) string {
	switch {
	case goos == "darwin" && goarch == "arm64":
		return "MonoAgent-darwin-arm64.zip"
	case goos == "windows":
		// amd64 only; Windows on ARM runs it under emulation.
		return "MonoAgent-windows-amd64.exe"
	case goos == "linux" && goarch == "amd64":
		return "MonoAgent-linux-amd64.tar.gz"
	}
	return ""
}

// bundledCLIAssetWindows is the CLI published next to the Windows app.
const bundledCLIAssetWindows = "monoagentcli-windows-amd64-bundled.exe"

type releaseInfo struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

// Run updates the app. Any error leaves the installation as it was.
func (u Updater) Run() (Result, error) {
	if u.Context == nil {
		u.Context = context.Background()
	}
	if u.Client == nil {
		u.Client = http.DefaultClient
	}
	if u.Progress == nil {
		u.Progress = func(string) {}
	}
	asset := AssetNameFor(u.GOOS, u.GOARCH)
	if asset == "" {
		return Result{}, fmt.Errorf("app update not supported on %s/%s", u.GOOS, u.GOARCH)
	}
	u.Progress("Checking for updates...")
	rc := u.Release
	if rc == nil {
		rc = release.DefaultClient()
	}
	var manifest *release.Manifest
	var rel *releaseInfo
	tag := ""
	rcc := *rc
	rcc.Force = rcc.Force || u.Force
	rc = &rcc
	m, err := rc.Latest(u.Context)
	switch {
	case err == nil:
		manifest, tag = m, m.Version
	case !errors.Is(err, release.ErrUnavailable):
		return Result{}, fmt.Errorf("the signed release manifest was rejected: %w; nothing was installed", err)
	case !release.AllowLegacyGitHubUpdates:
		return Result{}, fmt.Errorf("cannot verify an update: %w", err)
	default:
		if rel, err = u.fetchRelease(); err != nil {
			return Result{}, err
		}
		tag = rel.TagName
	}
	if u.UpToDate != nil && u.UpToDate(tag) {
		return Result{UpToDate: true}, nil
	}
	if err := release.CheckDowngrade(tag, u.Current, u.Force); err != nil {
		return Result{}, fmt.Errorf("%w; the installed app was kept", err)
	}
	u.Progress("Downloading app update...")
	want := []string{asset}
	// The Windows CLI is a separate asset; update it only when the app has
	// one next to it (otherwise the user runs a CLI from PATH, not ours).
	winCLI := ""
	if u.GOOS == "windows" {
		if p, ok := SiblingCLI(filepath.Dir(u.Exe), u.GOOS, u.GOARCH); ok {
			winCLI = p
			want = append(want, bundledCLIAssetWindows)
		}
	}
	var files map[string][]byte
	if manifest != nil {
		files, err = u.downloadSigned(rc, manifest, want)
	} else {
		files, err = u.downloadVerified(rel, want)
	}
	if err != nil {
		return Result{}, err
	}

	u.Progress("Checksum verified. Installing app update...")
	restart := "relaunch"
	switch u.GOOS {
	case "linux":
		err = installLinux(u.Exe, files[asset])
		restart = "quit"
	case "windows":
		err = u.installWindows(files[asset], winCLI, files[bundledCLIAssetWindows])
	case "darwin":
		err = u.installMacOS(files[asset])
	}
	if err != nil {
		return Result{}, err
	}
	u.Progress("Update installed — restarting")
	res := Result{NewVersion: tag, Restart: restart}
	if u.GOOS == "linux" {
		if _, cli, err := readLinuxTarball(files[asset]); err == nil {
			res.CLI = cli
		}
	}
	return res, nil
}

func (u Updater) fetchRelease() (*releaseInfo, error) {
	req, err := http.NewRequestWithContext(u.Context, "GET", u.APIURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %v", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := u.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("network error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var rel releaseInfo
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("parse error: %v", err)
	}
	return &rel, nil
}

// downloadSigned downloads each named asset listed in the signed manifest; the
// client returns bytes only after the size and sha256 pass.
func (u Updater) downloadSigned(rc *release.Client, m *release.Manifest, names []string) (map[string][]byte, error) {
	assets := map[string]release.Asset{}
	for _, n := range names {
		// Every desktop asset, including the Windows bundled CLI exe, is
		// kind "app" in the manifest.
		a, err := m.Asset(n, u.GOOS, u.GOARCH, "app")
		if err != nil {
			return nil, err
		}
		assets[n] = a
	}
	out := map[string][]byte{}
	for _, n := range names {
		data, err := rc.Download(u.Context, assets[n])
		if err != nil {
			return nil, fmt.Errorf("%w; nothing was installed", err)
		}
		out[n] = data
	}
	return out, nil
}

// downloadVerified downloads each named asset and checks it against the
// release's SHA256SUMS.txt (exact entry per name). Any failure is an error
// and nothing is returned.
func (u Updater) downloadVerified(rel *releaseInfo, names []string) (map[string][]byte, error) {
	urls := map[string]string{}
	for _, a := range rel.Assets {
		urls[a.Name] = a.BrowserDownloadURL
	}
	sumsURL := urls[updatecheck.SumsAssetName]
	if sumsURL == "" {
		return nil, fmt.Errorf("release %s has no %s — cannot verify the download, refusing to update", rel.TagName, updatecheck.SumsAssetName)
	}
	for _, n := range names {
		if urls[n] == "" {
			return nil, fmt.Errorf("no app asset for %s/%s in release %s (wanted %s)", u.GOOS, u.GOARCH, rel.TagName, n)
		}
	}
	sums, err := u.getAll(sumsURL)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %v — cannot verify the download, nothing was installed", updatecheck.SumsAssetName, err)
	}
	out := map[string][]byte{}
	for _, n := range names {
		data, err := u.getAll(urls[n])
		if err != nil {
			return nil, fmt.Errorf("download error: %v", err)
		}
		if err := updatecheck.VerifyReleaseDigest(data, sums, n); err != nil {
			return nil, err
		}
		out[n] = data
	}
	return out, nil
}

// getAll fetches url; any status but 200 is an error (an HTML error page
// must never be installed as the binary).
func (u Updater) getAll(url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(u.Context, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := u.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}
	return io.ReadAll(resp.Body)
}

// installWindows swaps the verified exe (and bundled CLI) in beside the
// running app. Windows lets a running exe be renamed but not deleted, so
// the old copies stay as *.bak until the restart script removes them —
// that includes the CLI running this update.
func (u Updater) installWindows(appExe []byte, cliPath string, cliExe []byte) error {
	dir := filepath.Dir(u.Exe)
	var swaps []swapItem
	staged, err := stageBytes(dir, appExe)
	if err != nil {
		return err
	}
	swaps = append(swaps, swapItem{target: u.Exe, staged: staged})
	if cliPath != "" {
		s, err := stageBytes(dir, cliExe)
		if err != nil {
			os.Remove(staged)
			return err
		}
		swaps = append(swaps, swapItem{target: cliPath, staged: s})
	}
	if err := swapAll(swaps); err != nil {
		return err
	}
	var targets []string
	for _, s := range swaps {
		targets = append(targets, s.target)
	}
	batPath := filepath.Join(dir, ".monoagent-update.bat")
	if err := os.WriteFile(batPath, []byte(windowsRestartScript(u.Exe, targets)), 0o755); err != nil {
		return nil // installed; only the restart and cleanup are skipped
	}
	_ = u.StartDetached("cmd.exe", "/C", batPath)
	return nil
}

// windowsRestartScript waits for the app (and the CLI) to exit, removes
// the *.bak copies of targets, starts exe and deletes itself. Every path
// is double-quoted so spaces cannot split it.
func windowsRestartScript(exe string, targets []string) string {
	var dels []string
	for _, t := range targets {
		dels = append(dels, fmt.Sprintf("del /Q \"%s\"\r\n", t+".bak"))
	}
	return "@echo off\r\ntimeout /t 2 /nobreak > nul\r\n" + strings.Join(dels, "") +
		fmt.Sprintf("start \"\" \"%s\"\r\ndel /Q \"%%~f0\"\r\n", exe)
}

// installMacOS unpacks the verified zip next to the .app bundle (same
// volume), swaps the bundle — which holds the bundled CLI — and reopens it
// after the app quits.
func (u Updater) installMacOS(zipData []byte) error {
	// exe = /path/MonoAgent.app/Contents/MacOS/<bin>: the bundle is 3 up.
	bundle := filepath.Dir(filepath.Dir(filepath.Dir(u.Exe)))
	if !strings.HasSuffix(bundle, ".app") {
		return fmt.Errorf("app is not inside a .app bundle (%s); update it by hand", bundle)
	}
	stage, err := os.MkdirTemp(filepath.Dir(bundle), ".monoagent-app-update-*")
	if err != nil {
		return fmt.Errorf("stage dir: %v", err)
	}
	defer os.RemoveAll(stage)
	zipPath := filepath.Join(stage, "app.zip")
	if err := os.WriteFile(zipPath, zipData, 0o600); err != nil {
		return fmt.Errorf("write download: %v", err)
	}
	// System unzip keeps the symlinks inside .app bundles.
	if out, err := exec.Command("unzip", "-q", zipPath, "-d", stage).CombinedOutput(); err != nil {
		return fmt.Errorf("unzip: %v — %s", err, out)
	}
	newApp := filepath.Join(stage, "MonoAgent.app")
	if st, err := os.Stat(newApp); err != nil || !st.IsDir() {
		return fmt.Errorf("MonoAgent.app not found in downloaded archive")
	}
	if err := swapAll([]swapItem{{target: bundle, staged: newApp}}); err != nil {
		return err
	}
	os.RemoveAll(bundle + ".bak")
	_ = u.StartDetached("/bin/bash", "-c", macOSReopenCommand(bundle))
	return nil
}

// macOSReopenCommand opens bundle again once the app has quit.
func macOSReopenCommand(bundle string) string {
	return "sleep 2; open " + shQuote(bundle)
}

// shQuote wraps a string in single quotes for safe use as a POSIX shell word,
// escaping any embedded single quotes. Single-quoted strings undergo no
// $-expansion or command substitution, unlike Go's %q double-quoted form.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// cliSiblingNames are the file names the CLI has next to the desktop app:
// "monoagentcli" (".exe" on Windows; macOS .app bundles and the current
// Linux tarball), then the release's bundled name
// "monoagentcli-<goos>-<goarch>-bundled[.exe]" (older Linux tarballs, and
// the separate Windows download saved beside MonoAgent-windows-amd64.exe).
// It mirrors the app's own lookup (wails-app/app.go cliSiblingNames).
func cliSiblingNames(goos, goarch string) []string {
	ext := ""
	if goos == "windows" {
		ext = ".exe"
	}
	return []string{
		"monoagentcli" + ext,
		"monoagentcli-" + goos + "-" + goarch + "-bundled" + ext,
	}
}

// SiblingCLI returns the first CLI found in execDir under cliSiblingNames.
func SiblingCLI(execDir, goos, goarch string) (string, bool) {
	for _, n := range cliSiblingNames(goos, goarch) {
		p := filepath.Join(execDir, n)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, true
		}
	}
	return "", false
}
