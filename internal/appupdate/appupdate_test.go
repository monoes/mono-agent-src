package appupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/monoes/mono-agent/internal/release"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeAppRelease serves a release with the given assets and a
// SHA256SUMS.txt generated from them (sumsOverride replaces it; sumsStatus
// makes fetching it fail; noSums leaves it out; missing lists assets
// advertised but answering 404).
type fakeAppRelease struct {
	assets       map[string][]byte
	sumsOverride string
	sumsStatus   int
	noSums       bool
	missing      map[string]bool
}

func (f fakeAppRelease) server(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/latest":
			var as []map[string]string
			for n := range f.assets {
				as = append(as, map[string]string{"name": n, "browser_download_url": srv.URL + "/dl/" + n})
			}
			if !f.noSums {
				as = append(as, map[string]string{"name": "SHA256SUMS.txt", "browser_download_url": srv.URL + "/sums"})
			}
			json.NewEncoder(w).Encode(map[string]any{"tag_name": "v9.9.9", "assets": as})
		case r.URL.Path == "/sums":
			if f.sumsStatus != 0 {
				http.Error(w, "nope", f.sumsStatus)
				return
			}
			if f.sumsOverride != "" {
				w.Write([]byte(f.sumsOverride))
				return
			}
			for n, b := range f.assets {
				w.Write([]byte(sha256hex(b) + "  " + n + "\n"))
			}
		case strings.HasPrefix(r.URL.Path, "/dl/"):
			n := strings.TrimPrefix(r.URL.Path, "/dl/")
			if f.missing[n] {
				http.NotFound(w, r)
				return
			}
			w.Write(f.assets[n])
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func sha256hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func linuxTarball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for n, body := range files {
		tw.WriteHeader(&tar.Header{Name: n, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func testUpdater(srv *httptest.Server, goos, goarch, exe string, started *[]string) Updater {
	// Release: an unkeyed client is unavailable, so the legacy API path under test runs
	// instead of the live signed manifest.
	return Updater{Release: &release.Client{}, Client: srv.Client(), APIURL: srv.URL + "/latest", GOOS: goos, GOARCH: goarch, Exe: exe,
		StartDetached: func(name string, args ...string) error {
			*started = append(*started, name+" "+strings.Join(args, " "))
			return nil
		}}
}

// linuxInstall makes <dir>/MonoAgent-linux-amd64 (+ optional CLI) and
// returns the app path.
func linuxInstall(t *testing.T, cliName string) string {
	t.Helper()
	dir := t.TempDir()
	exe := filepath.Join(dir, "MonoAgent-linux-amd64")
	os.WriteFile(exe, []byte("old app"), 0o755)
	if cliName != "" {
		os.WriteFile(filepath.Join(dir, cliName), []byte("old cli"), 0o755)
	}
	return exe
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

// dirNames lists dir's entries (to catch leftover staging/backup files).
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	es, _ := os.ReadDir(dir)
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

func TestAppUpdateLinuxInstallsAppAndBundledCLI(t *testing.T) {
	tgz := linuxTarball(t, map[string]string{"MonoAgent-linux-amd64": "new app", "monoagentcli": "new cli"})
	srv := fakeAppRelease{assets: map[string][]byte{"MonoAgent-linux-amd64.tar.gz": tgz}}.server(t)
	for _, existing := range []string{"monoagentcli", "monoagentcli-linux-amd64-bundled", ""} {
		t.Run("cli="+existing, func(t *testing.T) {
			exe := linuxInstall(t, existing)
			var started []string
			res, err := testUpdater(srv, "linux", "amd64", exe, &started).Run()
			if err != nil || res.NewVersion != "v9.9.9" || res.Restart != "quit" {
				t.Fatalf("result = %+v, %v", res, err)
			}
			dir := filepath.Dir(exe)
			if readFile(t, exe) != "new app" {
				t.Fatal("app not replaced")
			}
			cli := existing
			if cli == "" {
				cli = "monoagentcli" // the tarball's name when none was there
			}
			if readFile(t, filepath.Join(dir, cli)) != "new cli" {
				t.Fatalf("bundled CLI %s not updated", cli)
			}
			if got := dirNames(t, dir); len(got) != 2 {
				t.Fatalf("leftovers: %v", got)
			}
		})
	}
}

func TestAppUpdateFailsClosed(t *testing.T) {
	good := linuxTarball(t, map[string]string{"MonoAgent-linux-amd64": "new app", "monoagentcli": "new cli"})
	name := "MonoAgent-linux-amd64.tar.gz"
	for _, tc := range []struct {
		desc, want string
		rel        fakeAppRelease
	}{
		{"mismatch", "SHA-256 mismatch", fakeAppRelease{assets: map[string][]byte{name: good},
			sumsOverride: sha256hex([]byte("other")) + "  " + name + "\n"}},
		{"missing entry", "has no entry for " + name, fakeAppRelease{assets: map[string][]byte{name: good},
			sumsOverride: sha256hex(good) + "  " + name + "-old\n"}},
		{"sums fetch fails", "fetch SHA256SUMS.txt", fakeAppRelease{assets: map[string][]byte{name: good}, sumsStatus: 500}},
		{"no sums asset", "has no SHA256SUMS.txt", fakeAppRelease{assets: map[string][]byte{name: good}, noSums: true}},
		{"asset 404", "download error", fakeAppRelease{assets: map[string][]byte{name: good}, missing: map[string]bool{name: true}}},
		{"no app in tarball", "not found in downloaded archive", fakeAppRelease{assets: map[string][]byte{
			name: linuxTarball(t, map[string]string{"../../evil": "x"})}}},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			exe := linuxInstall(t, "monoagentcli")
			var started []string
			_, err := testUpdater(tc.rel.server(t), "linux", "amd64", exe, &started).Run()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}
			dir := filepath.Dir(exe)
			if readFile(t, exe) != "old app" || readFile(t, filepath.Join(dir, "monoagentcli")) != "old cli" {
				t.Fatal("installation changed on a failed update")
			}
			if got := dirNames(t, dir); len(got) != 2 {
				t.Fatalf("leftovers: %v", got)
			}
		})
	}
}

func TestAppUpdateUnsupportedPlatform(t *testing.T) {
	var started []string
	u := Updater{Release: &release.Client{}, GOOS: "linux", GOARCH: "arm64", StartDetached: func(string, ...string) error {
		started = append(started, "x")
		return nil
	}}
	if _, err := u.Run(); err == nil || !strings.Contains(err.Error(), "not supported on linux/arm64") || len(started) != 0 {
		t.Fatalf("err = %v, started = %v", err, started)
	}
}

func TestAppUpdateWindowsSwapsExeAndCLI(t *testing.T) {
	assets := map[string][]byte{
		"MonoAgent-windows-amd64.exe":            []byte("new app"),
		"monoagentcli-windows-amd64-bundled.exe": []byte("new cli"),
	}
	srv := fakeAppRelease{assets: assets}.server(t)
	dir := t.TempDir()
	exe := filepath.Join(dir, "MonoAgent-windows-amd64.exe")
	cli := filepath.Join(dir, "monoagentcli-windows-amd64-bundled.exe")
	os.WriteFile(exe, []byte("old app"), 0o755)
	os.WriteFile(cli, []byte("old cli"), 0o755)
	var started []string
	res, err := testUpdater(srv, "windows", "amd64", exe, &started).Run()
	if err != nil || res.Restart != "relaunch" {
		t.Fatalf("result = %+v, %v", res, err)
	}
	if readFile(t, exe) != "new app" || readFile(t, cli) != "new cli" {
		t.Fatal("exe or CLI not replaced")
	}
	// The running exe cannot be deleted on Windows: backups stay until the
	// restart script removes them.
	if readFile(t, exe+".bak") != "old app" {
		t.Fatal("no backup of the old exe")
	}
	bat := readFile(t, filepath.Join(dir, ".monoagent-update.bat"))
	if !strings.Contains(bat, `del /Q "`+exe+`.bak"`) || !strings.Contains(bat, `start "" "`+exe+`"`) {
		t.Fatalf("bat:\n%s", bat)
	}
	if len(started) != 1 || !strings.HasPrefix(started[0], "cmd.exe /C ") {
		t.Fatalf("restart = %v", started)
	}

	// A mismatching CLI asset aborts the whole update, the app included.
	bad := fakeAppRelease{assets: assets, sumsOverride: sha256hex(assets["MonoAgent-windows-amd64.exe"]) +
		"  MonoAgent-windows-amd64.exe\n" + sha256hex([]byte("x")) + "  monoagentcli-windows-amd64-bundled.exe\n"}
	dir2 := t.TempDir()
	exe2 := filepath.Join(dir2, "MonoAgent.exe")
	os.WriteFile(exe2, []byte("old app"), 0o755)
	os.WriteFile(filepath.Join(dir2, "monoagentcli.exe"), []byte("old cli"), 0o755)
	_, err = testUpdater(bad.server(t), "windows", "amd64", exe2, &started).Run()
	if err == nil || readFile(t, exe2) != "old app" || len(dirNames(t, dir2)) != 2 {
		t.Fatalf("tampered CLI: %v %v", err, dirNames(t, dir2))
	}
}

func TestAppUpdateMacOSSwapsBundle(t *testing.T) {
	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	for n, body := range map[string]string{
		"MonoAgent.app/Contents/MacOS/monoagent-ui": "new app",
		"MonoAgent.app/Contents/MacOS/monoagentcli": "new cli",
	} {
		w, _ := zw.Create(n)
		w.Write([]byte(body))
	}
	zw.Close()
	srv := fakeAppRelease{assets: map[string][]byte{"MonoAgent-darwin-arm64.zip": zbuf.Bytes()}}.server(t)
	apps := t.TempDir()
	macos := filepath.Join(apps, "MonoAgent.app", "Contents", "MacOS")
	os.MkdirAll(macos, 0o755)
	exe := filepath.Join(macos, "monoagent-ui")
	os.WriteFile(exe, []byte("old app"), 0o755)
	os.WriteFile(filepath.Join(macos, "monoagentcli"), []byte("old cli"), 0o755)
	var started []string
	res, err := testUpdater(srv, "darwin", "arm64", exe, &started).Run()
	if err != nil || res.Restart != "relaunch" {
		t.Fatalf("result = %+v, %v", res, err)
	}
	if readFile(t, exe) != "new app" || readFile(t, filepath.Join(macos, "monoagentcli")) != "new cli" {
		t.Fatal("bundle not replaced")
	}
	if string(res.CLI) != "new cli" {
		t.Fatalf("Result.CLI = %q, want the CLI inside the new bundle", res.CLI)
	}
	if got := dirNames(t, apps); len(got) != 1 {
		t.Fatalf("leftovers next to the bundle: %v", got)
	}
	if len(started) != 1 || !strings.Contains(started[0], "open '"+filepath.Join(apps, "MonoAgent.app")+"'") {
		t.Fatalf("restart = %v", started)
	}
}

func TestAppUpdateMacOSWithoutACLIOnlyLeavesResultCLINil(t *testing.T) {
	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	w, _ := zw.Create("MonoAgent.app/Contents/MacOS/monoagent-ui")
	w.Write([]byte("new app"))
	zw.Close()
	srv := fakeAppRelease{assets: map[string][]byte{"MonoAgent-darwin-arm64.zip": zbuf.Bytes()}}.server(t)
	apps := t.TempDir()
	macos := filepath.Join(apps, "MonoAgent.app", "Contents", "MacOS")
	os.MkdirAll(macos, 0o755)
	exe := filepath.Join(macos, "monoagent-ui")
	os.WriteFile(exe, []byte("old app"), 0o755)
	var started []string
	res, err := testUpdater(srv, "darwin", "arm64", exe, &started).Run()
	if err != nil || res.CLI != nil || readFile(t, exe) != "new app" {
		t.Fatalf("result = %+v, %v", res, err)
	}
}

func TestSwapAllRollsBack(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	os.WriteFile(a, []byte("old a"), 0o644)
	os.WriteFile(b, []byte("old b"), 0o644)
	sa, _ := stageBytes(dir, []byte("new a"))
	err := swapAll([]swapItem{{target: a, staged: sa}, {target: b, staged: filepath.Join(dir, "missing")}})
	if err == nil {
		t.Fatal("swap with a missing staged file succeeded")
	}
	if readFile(t, a) != "old a" || readFile(t, b) != "old b" || len(dirNames(t, dir)) != 2 {
		t.Fatalf("not rolled back: %v", dirNames(t, dir))
	}
}

func TestAppUpdateUpToDateInstallsNothing(t *testing.T) {
	tgz := linuxTarball(t, map[string]string{"MonoAgent-linux-amd64": "new app"})
	srv := fakeAppRelease{assets: map[string][]byte{"MonoAgent-linux-amd64.tar.gz": tgz}}.server(t)
	exe := linuxInstall(t, "monoagentcli")
	var started, progress []string
	u := testUpdater(srv, "linux", "amd64", exe, &started)
	u.Progress = func(m string) { progress = append(progress, m) }
	u.UpToDate = func(tag string) bool { return tag == "v9.9.9" }
	res, err := u.Run()
	if err != nil || !res.UpToDate || res.NewVersion != "" {
		t.Fatalf("result = %+v, %v", res, err)
	}
	if readFile(t, exe) != "old app" || len(dirNames(t, filepath.Dir(exe))) != 2 {
		t.Fatal("an up-to-date app must not be touched")
	}
	if strings.Join(progress, "|") != "Checking for updates..." {
		t.Fatalf("progress = %v", progress)
	}
}

func TestAppUpdateReportsProgress(t *testing.T) {
	tgz := linuxTarball(t, map[string]string{"MonoAgent-linux-amd64": "new app"})
	srv := fakeAppRelease{assets: map[string][]byte{"MonoAgent-linux-amd64.tar.gz": tgz}}.server(t)
	var started, progress []string
	u := testUpdater(srv, "linux", "amd64", linuxInstall(t, ""), &started)
	u.Progress = func(m string) { progress = append(progress, m) }
	if _, err := u.Run(); err != nil {
		t.Fatal(err)
	}
	want := "Checking for updates...|Downloading app update...|Checksum verified. Installing app update...|Update installed — restarting"
	if got := strings.Join(progress, "|"); got != want {
		t.Fatalf("progress = %q", got)
	}
}

// Every path in the restart scripts is quoted so a path with $, backticks
// or spaces can neither expand nor split.
func TestSwapScriptsQuotePaths(t *testing.T) {
	bundle := "/Apps/Mono $(x) `y` it's.app"
	if got := macOSReopenCommand(bundle); got != `sleep 2; open '/Apps/Mono $(x) `+"`y`"+` it'\''s.app'` {
		t.Fatalf("macOS reopen = %s", got)
	}
	w := windowsRestartScript(`C:\A B\app.exe`, []string{`C:\A B\app.exe`, `C:\A B\monoagentcli.exe`})
	for _, want := range []string{`del /Q "C:\A B\app.exe.bak"`, `del /Q "C:\A B\monoagentcli.exe.bak"`, `start "" "C:\A B\app.exe"`, `del /Q "%~f0"`} {
		if !strings.Contains(w, want+"\r\n") {
			t.Fatalf("windows script lacks %q:\n%s", want, w)
		}
	}
}

func TestSiblingCLIPrefersPlainName(t *testing.T) {
	dir := t.TempDir()
	if _, ok := SiblingCLI(dir, "windows", "amd64"); ok {
		t.Fatal("found a CLI in an empty dir")
	}
	bundled := filepath.Join(dir, "monoagentcli-windows-amd64-bundled.exe")
	os.WriteFile(bundled, []byte("x"), 0o755)
	if p, ok := SiblingCLI(dir, "windows", "amd64"); !ok || p != bundled {
		t.Fatalf("got %s %v", p, ok)
	}
	plain := filepath.Join(dir, "monoagentcli.exe")
	os.WriteFile(plain, []byte("x"), 0o755)
	if p, _ := SiblingCLI(dir, "windows", "amd64"); p != plain {
		t.Fatalf("got %s, want %s", p, plain)
	}
}
