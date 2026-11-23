package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func tgz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

// fakeAppRelease serves a latest release with the linux app tarball and a
// SHA256SUMS.txt, and returns its latest-release endpoint; tamper serves a
// different archive than the one summed.
func fakeAppRelease(t *testing.T, archive []byte, tamper bool) string {
	t.Helper()
	sum := sha256.Sum256(archive)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			fmt.Fprintf(w, `{"tag_name":"v9.9.9","assets":[{"name":"MonoAgent-linux-amd64.tar.gz","browser_download_url":"%s/app"},{"name":"SHA256SUMS.txt","browser_download_url":"%s/sums"}]}`, srv.URL, srv.URL)
		case "/app":
			if tamper {
				_, _ = w.Write(append([]byte("x"), archive...))
				return
			}
			_, _ = w.Write(archive)
		case "/sums":
			fmt.Fprintf(w, "%s  MonoAgent-linux-amd64.tar.gz\n", hex.EncodeToString(sum[:]))
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/latest"
}

func runUpdateAppCmd(t *testing.T, d Deps, args ...string) (AppResult, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	cmd := NewCommand(d)
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("update: %v (%s)", err, errOut.String())
	}
	var res AppResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out.String())
	}
	return res, errOut.String()
}

func appInstallDir(t *testing.T) (app, cli string) {
	t.Helper()
	dir := t.TempDir()
	app, cli = filepath.Join(dir, "MonoAgent-linux-amd64"), filepath.Join(dir, "monoagentcli")
	for _, p := range []string{app, cli} {
		if err := os.WriteFile(p, []byte("old"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return app, cli
}

func TestUpdateAppLinuxReplacesAppAndBundledCLI(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("linux install path")
	}
	latest := fakeAppRelease(t, tgz(t, map[string]string{"MonoAgent-linux-amd64": "new-app", "monoagentcli": "new-cli"}), false)
	app, cli := appInstallDir(t)
	res, progress := runUpdateAppCmd(t, testDeps(t, latest), "--app", app, "--current", "v1.0.0")
	if !res.Success || res.NewVersion != "v9.9.9" || res.Restart != "quit" || res.Error != "" {
		t.Fatalf("res = %+v", res)
	}
	for p, want := range map[string]string{app: "new-app", cli: "new-cli"} {
		if b, _ := os.ReadFile(p); string(b) != want {
			t.Fatalf("%s = %q, want %q", p, b, want)
		}
	}
	if !strings.Contains(progress, `"kind":"line"`) || !strings.Contains(progress, "Checksum verified") {
		t.Fatalf("progress = %s", progress)
	}
}

func TestUpdateAppRefusesATamperedDownload(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("linux install path")
	}
	latest := fakeAppRelease(t, tgz(t, map[string]string{"MonoAgent-linux-amd64": "evil"}), true)
	app, cli := appInstallDir(t)
	res, _ := runUpdateAppCmd(t, testDeps(t, latest), "--app", app, "--current", "v1.0.0")
	if res.Success || res.Error == "" {
		t.Fatalf("a download that doesn't match SHA256SUMS must fail: %+v", res)
	}
	for _, p := range []string{app, cli} {
		if b, _ := os.ReadFile(p); string(b) != "old" {
			t.Fatalf("%s changed to %q after a failed verification", p, b)
		}
	}
}

func TestUpdateAppUpToDate(t *testing.T) {
	t.Parallel()
	latest := fakeAppRelease(t, tgz(t, map[string]string{"MonoAgent-linux-amd64": "x"}), false)
	app, _ := appInstallDir(t)
	res, _ := runUpdateAppCmd(t, testDeps(t, latest), "--app", app, "--current", "v9.9.9")
	if !res.Success || !res.UpToDate {
		t.Fatalf("res = %+v", res)
	}
	if b, _ := os.ReadFile(app); string(b) != "old" {
		t.Fatal("up to date must not install")
	}
}

func TestUpdateCheckAndAppAreExclusive(t *testing.T) {
	t.Parallel()
	cmd := NewCommand(testDeps(t, "http://127.0.0.1:1"))
	cmd.SetArgs([]string{"--check", "--app", "/x"})
	if err := cmd.Execute(); !isInvalidInput(err) {
		t.Fatalf("err = %v, want invalid input", err)
	}
}

// --json keeps the app's contract: every stderr line is an NDJSON progress
// record, stdout is the one result object.
func TestUpdateAppJSONProgressIsNDJSON(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("linux/amd64 install path")
	}
	latest := fakeAppRelease(t, tgz(t, map[string]string{"MonoAgent-linux-amd64": "new-app"}), false)
	app, _ := appInstallDir(t)
	res, progress := runUpdateAppCmd(t, testDeps(t, latest), "--app", app)
	if !res.Success || res.NewVersion != "v9.9.9" {
		t.Fatalf("res = %+v", res)
	}
	lines := strings.Split(strings.TrimSpace(progress), "\n")
	if len(lines) < 3 {
		t.Fatalf("progress = %q", progress)
	}
	for _, l := range lines {
		var ev struct{ Kind, Message string }
		if err := json.Unmarshal([]byte(l), &ev); err != nil || ev.Kind != "line" || ev.Message == "" {
			t.Fatalf("progress line %q is not {kind:line,message}", l)
		}
	}
}

// A failure is a result in --json mode (exit 0, error set), so the app can
// show it.
func TestUpdateAppJSONReportsErrors(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	cmd := NewCommand(testDeps(t, "http://127.0.0.1:1/latest"))
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--app", filepath.Join(t.TempDir(), "MonoAgent-linux-amd64")})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("--json failure must exit 0: %v", err)
	}
	var res AppResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil || res.Success || res.Error == "" {
		t.Fatalf("res = %+v (%v) %s", res, err, out.String())
	}
}

// The CLI the app runs is the bundled one next to it, so `update --app`
// replaces the running binary. Run a copy of this test binary as that
// bundled CLI and let it update itself.
func TestUpdateAppReplacesTheRunningBundledCLI(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("linux/amd64 install path")
	}
	latest := fakeAppRelease(t, tgz(t, map[string]string{"MonoAgent-linux-amd64": "new-app", "monoagentcli": "new-cli"}), false)
	dir := t.TempDir()
	app, cli := filepath.Join(dir, "MonoAgent-linux-amd64"), filepath.Join(dir, "monoagentcli")
	if err := os.WriteFile(app, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cli, bin, 0o755); err != nil {
		t.Fatal(err)
	}
	c := exec.Command(cli, "-test.run=^TestUpdateAppHelperProcess$", "--", "--app", app, "--current", "v1.0.0")
	c.Env = append(os.Environ(), "MONOAGENT_UPDATE_APP_HELPER="+latest)
	var out, errOut bytes.Buffer
	c.Stdout, c.Stderr = &out, &errOut
	if err := c.Run(); err != nil {
		t.Fatalf("bundled CLI: %v\n%s\n%s", err, out.String(), errOut.String())
	}
	var res AppResult
	// The helper's stdout ends with the test runner's "PASS".
	if err := json.NewDecoder(&out).Decode(&res); err != nil || !res.Success || res.NewVersion != "v9.9.9" {
		t.Fatalf("res = %+v (%v)\nstderr = %s", res, err, errOut.String())
	}
	for p, want := range map[string]string{app: "new-app", cli: "new-cli"} {
		if b, _ := os.ReadFile(p); string(b) != want {
			t.Fatalf("%s = %.20q, want %q", p, b, want)
		}
	}
	if es, _ := os.ReadDir(dir); len(es) != 2 {
		var names []string
		for _, e := range es {
			names = append(names, e.Name())
		}
		t.Fatalf("leftovers: %v", names)
	}
}

// TestUpdateAppHelperProcess is the bundled CLI of
// TestUpdateAppReplacesTheRunningBundledCLI; it does nothing otherwise.
func TestUpdateAppHelperProcess(t *testing.T) {
	url := os.Getenv("MONOAGENT_UPDATE_APP_HELPER")
	if url == "" {
		t.Skip("helper process")
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	cmd := NewCommand(testDeps(t, url))
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
}

// With no --current the app update compares against this binary's own version, so a
// release older than it is not installed.
func TestUpdateAppDefaultsCurrentToTheBinaryVersion(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("linux install path")
	}
	latest := fakeAppRelease(t, tgz(t, map[string]string{"MonoAgent-linux-amd64": "new"}), false)
	app, _ := appInstallDir(t)
	d := testDeps(t, latest)
	d.Version = func() string { return "v99.0.0" }
	res, _ := runUpdateAppCmd(t, d, "--app", app)
	if res.NewVersion != "" {
		t.Fatalf("an older release must not be installed: %+v", res)
	}
	if b, _ := os.ReadFile(app); string(b) != "old" {
		t.Fatal("the app changed")
	}
}
