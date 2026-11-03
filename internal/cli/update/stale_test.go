//go:build !windows

package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/autostart"
)

// The CLIs of the machine other than the one just updated: the daemon service's exe and the one on PATH. Everything here
// runs in temp dirs with fake binaries; nothing of the real home, PATH or service manager is touched.

const (
	newAsset = "#!/bin/sh\n# the new release\n"
	oldAsset = "#!/bin/sh\n# an old release\n"
)

// staleRig has a service definition naming exe, a registered fake service manager, and probe answers by path.
type staleRig struct {
	d        Deps
	exe      string
	unit     string
	versions map[string]string
	newVer   string // what a freshly written candidate binary (.monoagentcli-update-*) answers
}

func newStaleRig(t *testing.T, installed bool) *staleRig {
	t.Helper()
	dir := t.TempDir()
	exe := filepath.Join(dir, "daemon-bin", "monoagentcli")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte(oldAsset), 0o755); err != nil {
		t.Fatal(err)
	}
	unit := filepath.Join(dir, "monoagent-daemon.service")
	if err := os.WriteFile(unit, []byte("[Service]\nExecStart=\""+exe+"\" daemon\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &staleRig{exe: exe, unit: unit, versions: map[string]string{}, newVer: "0.215.7"}
	r.d = testDeps(t, "http://127.0.0.1:1")
	r.d.Installer = func() autostart.Installer { return &fakeAutostart{installed: installed, where: unit} }
	r.d.ProbeVersion = func(_ context.Context, p string) (string, error) {
		if v, ok := r.versions[p]; ok {
			return v, nil
		}
		if strings.HasPrefix(filepath.Base(p), ".monoagentcli-update-") && r.newVer != "" {
			return r.newVer, nil
		}
		return "", errors.New("no such fake")
	}
	return r
}

func TestTheDaemonServicesExeIsReplacedFromTheVerifiedAsset(t *testing.T) {
	r := newStaleRig(t, true)
	r.versions[r.exe] = "0.176.0"
	got := r.d.staleCLIs(context.Background(), "v0.215.7", nil, []byte(newAsset))
	if len(got) != 1 || got[0].Action != "updated" || got[0].Role != "daemon" || got[0].Version != "0.176.0" {
		t.Fatalf("stale = %+v", got)
	}
	if b, _ := os.ReadFile(r.exe); string(b) != newAsset {
		t.Fatalf("the daemon's exe was not replaced: %q", b)
	}
	if fi, _ := os.Stat(r.exe); fi.Mode()&0o111 == 0 {
		t.Error("the replaced exe is not executable")
	}
}

func TestTheDaemonServicesExeIsOnlyReportedWhenItCannotBeReplacedFromHere(t *testing.T) {
	t.Run("a link to a file that may be replaced: the file behind it is, the link stays", func(t *testing.T) {
		r := newStaleRig(t, true)
		real := filepath.Join(t.TempDir(), "share", "monoagentcli")
		if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(real, []byte(oldAsset), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(r.exe); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, r.exe); err != nil {
			t.Fatal(err)
		}
		r.versions[r.exe], r.versions[real] = "0.176.0", "0.176.0" // the probe asks the file the name resolves to
		got := r.d.staleCLIs(context.Background(), "v0.215.7", nil, []byte(newAsset))
		if len(got) != 1 || got[0].Action != "updated" || !got[0].runsDaemon {
			t.Fatalf("stale = %+v", got)
		}
		if b, _ := os.ReadFile(real); string(b) != newAsset {
			t.Fatalf("the file behind the link was not replaced: %q", b)
		}
		if fi, err := os.Lstat(r.exe); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("the link was replaced by a file: %v", err)
		}
	})
	for name, prep := range map[string]func(t *testing.T, r *staleRig, real string){
		"the folder of the link is writable by others": func(t *testing.T, r *staleRig, real string) {
			if err := os.Chmod(filepath.Dir(r.exe), 0o777); err != nil {
				t.Fatal(err)
			}
		},
		"the file behind the link is in a folder others can write": func(t *testing.T, r *staleRig, real string) {
			if err := os.Chmod(filepath.Dir(real), 0o777); err != nil {
				t.Fatal(err)
			}
		},
		"the file behind the link is writable by others": func(t *testing.T, r *staleRig, real string) {
			if err := os.Chmod(real, 0o777); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run("a link is not followed when "+name, func(t *testing.T) {
			r := newStaleRig(t, true)
			real := filepath.Join(t.TempDir(), "share", "monoagentcli")
			if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(real, []byte(oldAsset), 0o755); err != nil {
				t.Fatal(err)
			}
			_ = os.Remove(r.exe)
			if err := os.Symlink(real, r.exe); err != nil {
				t.Fatal(err)
			}
			prep(t, r, real)
			r.versions[r.exe], r.versions[real] = "0.176.0", "0.176.0"
			got := r.d.staleCLIs(context.Background(), "v0.215.7", nil, []byte(newAsset))
			if len(got) != 1 || got[0].Action != "stale" {
				t.Fatalf("stale = %+v", got)
			}
			if b, _ := os.ReadFile(real); string(b) != oldAsset {
				t.Fatal("a file behind a link that fails a rule was rewritten")
			}
		})
	}
	t.Run("no verified asset in hand (update --app)", func(t *testing.T) {
		r := newStaleRig(t, true)
		r.versions[r.exe] = "0.176.0"
		got := r.d.staleCLIs(context.Background(), "v0.215.7", nil, nil)
		if len(got) != 1 || got[0].Action != "stale" || !strings.Contains(got[0].Message, r.exe+" update") {
			t.Fatalf("stale = %+v", got)
		}
		if b, _ := os.ReadFile(r.exe); string(b) != oldAsset {
			t.Fatal("the exe was replaced with nothing verified in hand")
		}
	})
	t.Run("the service is not registered", func(t *testing.T) {
		r := newStaleRig(t, false)
		r.versions[r.exe] = "0.176.0"
		if got := r.d.staleCLIs(context.Background(), "v0.215.7", nil, []byte(newAsset)); len(got) != 0 {
			t.Fatalf("an unregistered service's exe was looked at: %+v", got)
		}
	})
}

// The monoagentcli on PATH is replaced by the same rules as the daemon's exe: from the verified asset, and only when it is safe
// to. Without an asset (update --app), or when a rule refuses it, it is reported with the exact command.
func TestAMonoagentcliOnPathIsReplacedUnderTheSameRulesAsTheDaemonsExe(t *testing.T) {
	onPath := func(t *testing.T) (*staleRig, string) {
		r := newStaleRig(t, false)
		bin := filepath.Join(t.TempDir(), "bin dir", "monoagentcli")
		if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(bin, []byte(oldAsset), 0o755); err != nil {
			t.Fatal(err)
		}
		r.d.LookPath = func(string) (string, error) { return bin, nil }
		r.versions[bin] = "0.176.0"
		return r, bin
	}
	t.Run("replaced from the verified asset", func(t *testing.T) {
		r, bin := onPath(t)
		got := r.d.staleCLIs(context.Background(), "v0.215.7", nil, []byte(newAsset))
		if len(got) != 1 || got[0].Role != "path" || got[0].Action != "updated" || got[0].Version != "0.176.0" {
			t.Fatalf("stale = %+v", got)
		}
		if b, _ := os.ReadFile(bin); string(b) != newAsset {
			t.Fatalf("the PATH CLI was not replaced: %q", b)
		}
		if fi, _ := os.Stat(bin); fi.Mode()&0o111 == 0 {
			t.Error("the replaced CLI is not executable")
		}
	})
	t.Run("no verified asset: reported with the quoted command", func(t *testing.T) {
		r, bin := onPath(t)
		got := r.d.staleCLIs(context.Background(), "v0.215.7", nil, nil)
		if len(got) != 1 || got[0].Action != "stale" || !strings.Contains(got[0].Message, "'"+bin+"' update") {
			t.Fatalf("stale = %+v", got)
		}
		if b, _ := os.ReadFile(bin); string(b) != oldAsset {
			t.Fatal("replaced with nothing verified in hand")
		}
	})
	for name, prep := range map[string]func(t *testing.T, bin string){
		"others can write the file":   func(t *testing.T, bin string) { _ = os.Chmod(bin, 0o777) },
		"others can write the folder": func(t *testing.T, bin string) { _ = os.Chmod(filepath.Dir(bin), 0o777) },
	} {
		t.Run("refused: "+name, func(t *testing.T) {
			r, bin := onPath(t)
			prep(t, bin)
			r.versions[realPath(bin)] = "0.176.0"
			got := r.d.staleCLIs(context.Background(), "v0.215.7", nil, []byte(newAsset))
			if len(got) != 1 || got[0].Action != "stale" || !strings.Contains(got[0].Message, "was not replaced") {
				t.Fatalf("stale = %+v", got)
			}
			if b, _ := os.ReadFile(bin); string(b) != oldAsset {
				t.Fatal("a CLI that a rule refuses was replaced")
			}
		})
	}
	t.Run("never as root", func(t *testing.T) {
		old := isRoot
		isRoot = func() bool { return true }
		defer func() { isRoot = old }()
		r, bin := onPath(t)
		got := r.d.staleCLIs(context.Background(), "v0.215.7", nil, []byte(newAsset))
		if len(got) != 1 || got[0].Action != "stale" {
			t.Fatalf("stale = %+v", got)
		}
		if b, _ := os.ReadFile(bin); string(b) != oldAsset {
			t.Fatal("replaced by a command running as root")
		}
	})
}

func TestACLIAsNewAsTheReleaseOrTheOneJustUpdatedIsLeftOut(t *testing.T) {
	r := newStaleRig(t, true)
	r.d.LookPath = func(string) (string, error) { return r.exe, nil } // the same file as the daemon's
	for name, c := range map[string]struct {
		version string
		skip    []string
		want    int
	}{
		"as new":       {"0.215.7", nil, 0},
		"newer":        {"0.300.0", nil, 0},
		"skipped":      {"0.176.0", []string{r.exe}, 0},
		"found once":   {"0.176.0", nil, 1}, // the daemon's exe and PATH name one file: one entry
		"a dev build":  {"dev", nil, 1},     // not a version number: could not tell, never replaced
		"not a number": {"banana", nil, 1},
	} {
		r.versions[r.exe] = c.version
		got := r.d.staleCLIs(context.Background(), "v0.215.7", c.skip, nil)
		if len(got) != c.want {
			t.Errorf("%s: %+v, want %d entries", name, got, c.want)
		}
	}
}

func TestACLIWhoseVersionCannotBeToldIsReportedWithItsCommand(t *testing.T) {
	r := newStaleRig(t, true) // no version answer for the exe
	got := r.d.staleCLIs(context.Background(), "v0.215.7", nil, []byte(newAsset))
	if len(got) != 1 || got[0].Action != "stale" || got[0].Version != "" || !strings.Contains(got[0].Message, "could not tell its version") {
		t.Fatalf("stale = %+v", got)
	}
	if b, _ := os.ReadFile(r.exe); string(b) != oldAsset {
		t.Fatal("a binary whose version is unknown was replaced")
	}
}

// The default probe runs the program in a scratch environment: an empty temporary HOME that is not the real one, a bare
// PATH, and no inherited variables, and a program that is not monoagentcli is not counted.
func TestTheVersionProbeRunsInAScratchEnvironment(t *testing.T) {
	realHome := t.TempDir()
	t.Setenv("HOME", realHome)
	t.Setenv("SECRET_FROM_THE_ENVIRONMENT", "x")
	dir := t.TempDir()
	seen := filepath.Join(dir, "seen")
	script := filepath.Join(dir, "monoagentcli")
	body := "#!/bin/sh\nprintf '%s|%s|%s|%s' \"$HOME\" \"$PATH\" \"$SECRET_FROM_THE_ENVIRONMENT\" \"$(ls -A \"$HOME\" | wc -l)\" > " + seen + "\nprintf '{\"version\":\"v0.176.0\",\"build_date\":\"2026-01-01T00:00:00Z\"}'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	d := Deps{}
	v, err := d.probeVersion(context.Background(), script)
	if err != nil || v != "0.176.0" {
		t.Fatalf("version = %q, %v", v, err)
	}
	b, _ := os.ReadFile(seen)
	parts := strings.Split(string(b), "|")
	if len(parts) != 4 || parts[0] == realHome || parts[0] == "" || parts[1] != "/usr/bin:/bin" || parts[2] != "" || strings.TrimSpace(parts[3]) != "0" {
		t.Fatalf("the probe's environment: %q", b)
	}
	if _, err := os.Stat(parts[0]); err == nil {
		t.Error("the scratch home was left behind")
	}

	// the older text form, and something that is not monoagentcli at all
	text := filepath.Join(dir, "old")
	_ = os.WriteFile(text, []byte("#!/bin/sh\necho 'monoagentcli v0.150.2 (built 2026-01-01)'\n"), 0o755)
	if v, err := d.probeVersion(context.Background(), text); err != nil || v != "0.150.2" {
		t.Fatalf("text form: %q, %v", v, err)
	}
	other := filepath.Join(dir, "other")
	_ = os.WriteFile(other, []byte("#!/bin/sh\necho 'GNU bash'\n"), 0o755)
	if v, err := d.probeVersion(context.Background(), other); err == nil {
		t.Fatalf("something that is not monoagentcli counted as version %q", v)
	}
}

// update --app reports what it left behind, in its JSON result and its progress lines.
func TestUpdateAppResultCarriesTheStaleCLIs(t *testing.T) {
	r := newStaleRig(t, true)
	r.versions[r.exe] = "0.176.0"
	app := filepath.Join(t.TempDir(), "MonoAgent")
	got := r.d.staleAfterApp(context.Background(), app, "", AppResult{NewVersion: "v0.215.7"})
	if len(got) != 1 || got[0].Path != r.exe {
		t.Fatalf("stale = %+v", got)
	}
	var out bytes.Buffer
	if err := r.d.WriteJSON(&out, AppResult{Success: true, NewVersion: "v0.215.7", StaleCLIs: got}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"stale_clis"`) || !strings.Contains(out.String(), fmt.Sprintf("%q", r.exe)) {
		t.Fatalf("result json: %s", out.String())
	}
	_ = exec.ErrNotFound
}

// update --app wires it in: the daemon service's exe (old, not replaced: nothing of it was downloaded) is in the result and in
// the progress lines, while the CLI bundled with the app, which the update just replaced, is not listed though it would
// still answer an old version.
func TestUpdateAppReportsTheStaleCLIsAndLeavesTheBundledOneOut(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("linux/amd64 install path")
	}
	latest := fakeAppRelease(t, tgz(t, map[string]string{"MonoAgent-linux-amd64": "new-app", "monoagentcli": "new-cli"}), false)
	app, bundled := appInstallDir(t)
	r := newStaleRig(t, true)
	r.versions[r.exe] = "0.176.0"
	r.versions[bundled] = "0.176.0"
	// the daemon service starts the CLI bundled with the app: the update just replaced it, so it is not listed
	other := filepath.Join(t.TempDir(), "monoagentcli")
	if err := os.WriteFile(other, []byte(oldAsset), 0o755); err != nil {
		t.Fatal(err)
	}
	r.versions[other] = "0.176.0"
	d := r.d
	d.LookPath = func(string) (string, error) { return other, nil } // a PATH CLI that is not the bundled one
	d.ReleaseClient, d.LatestReleaseURL = testDeps(t, latest).ReleaseClient, latest
	// the unit names the bundled CLI
	if err := os.WriteFile(r.unit, []byte("[Service]\nExecStart=\""+bundled+"\" daemon\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, progress := runUpdateAppCmd(t, d, "--app", app, "--current", "v1.0.0")
	if !res.Success || len(res.StaleCLIs) != 1 || res.StaleCLIs[0].Path != other || res.StaleCLIs[0].Action != "stale" {
		t.Fatalf("res = %+v", res)
	}
	if !strings.Contains(progress, other+" update") {
		t.Errorf("the progress lines do not carry the command: %s", progress)
	}
	if b, _ := os.ReadFile(bundled); string(b) != "new-cli" {
		t.Errorf("the bundled CLI is not the one the app update installed: %q", b)
	}
}

// `update` on a binary that is already current still names the stale ones.
func TestUpdateWhenAlreadyCurrentNamesTheStaleCLIs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"tag_name":"v9.9.9","assets":[]}`)
	}))
	t.Cleanup(srv.Close)
	r := newStaleRig(t, true)
	r.versions[r.exe] = "0.176.0"
	d := r.d
	d.LatestReleaseURL = srv.URL
	d.Version = func() string { return "v9.9.9" }
	d.JSONOutput = func() bool { return false }
	out := captureStdout(t, func() {
		if err := d.run(NewCommand(d)); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, r.exe+" update") {
		t.Fatalf("the stale daemon exe is not named:\n%s", out)
	}
}

func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	old := os.Stdout
	rd, wr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = wr
	done := make(chan string)
	go func() { b, _ := io.ReadAll(rd); done <- string(b) }()
	defer func() { os.Stdout = old }()
	f()
	wr.Close()
	os.Stdout = old
	return <-done
}

// The whole `update`, against a fake release: this binary, the daemon service's exe and the CLI on PATH are all replaced from
// the verified asset, the daemon's exe before the daemon is restarted, so that the restart runs the new code.
func TestUpdateReplacesTheDaemonServicesExeBeforeItRestartsTheDaemon(t *testing.T) {
	asset := updateAssetName()
	sum := sha256.Sum256([]byte(newAsset))
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			fmt.Fprintf(w, `{"tag_name":"v9.9.9","assets":[{"name":%q,"browser_download_url":"%s/bin"},{"name":"SHA256SUMS.txt","browser_download_url":"%s/sums"}]}`, asset, srv.URL, srv.URL)
		case "/bin":
			_, _ = w.Write([]byte(newAsset))
		case "/sums":
			fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), asset)
		}
	}))
	t.Cleanup(srv.Close)

	r := newStaleRig(t, true)
	self := filepath.Join(t.TempDir(), "monoagentcli")
	if err := os.WriteFile(self, []byte(oldAsset), 0o755); err != nil {
		t.Fatal(err)
	}
	onPath := filepath.Join(t.TempDir(), "on-path", "monoagentcli")
	if err := os.MkdirAll(filepath.Dir(onPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(onPath, []byte(oldAsset), 0o755); err != nil {
		t.Fatal(err)
	}
	r.versions[r.exe], r.versions[onPath] = "0.176.0", "0.176.0"
	r.newVer = "9.9.9"

	d := r.d
	d.LatestReleaseURL = srv.URL + "/latest"
	d.Version = func() string { return "v1.0.0" }
	d.JSONOutput = func() bool { return false }
	d.SelfPath = func() (string, error) { return self, nil }
	d.LookPath = func(string) (string, error) { return onPath, nil }
	hb := &fakeHeartbeat{}
	hb.write(os.Getpid(), "v1.0.0")
	d.Heartbeat = hb.read
	fake := &fakeAutostart{installed: true, where: r.unit}
	var exeAtRestart string
	fake.onRestart = func() {
		b, _ := os.ReadFile(r.exe)
		exeAtRestart = string(b)
		hb.write(os.Getpid(), "v9.9.9")
	}
	d.Installer = func() autostart.Installer { return fake }
	d.ServicePID = func(context.Context, autostart.Installer) (int, error) { return os.Getpid(), nil }
	d.VerifyWait, d.VerifyTick = 300*time.Millisecond, 10*time.Millisecond

	out := captureStdout(t, func() {
		if err := d.run(NewCommand(d)); err != nil {
			t.Fatal(err)
		}
	})
	if b, _ := os.ReadFile(self); string(b) != newAsset {
		t.Errorf("this binary was not replaced: %q", b)
	}
	if b, _ := os.ReadFile(r.exe); string(b) != newAsset {
		t.Errorf("the daemon service's exe was not replaced: %q", b)
	}
	if fake.restarted != 1 || exeAtRestart != newAsset {
		t.Errorf("the daemon restarted %d times; its exe at that moment was %q, want the new one", fake.restarted, exeAtRestart)
	}
	if b, _ := os.ReadFile(onPath); string(b) != newAsset {
		t.Errorf("the CLI on PATH was not replaced: %q", b)
	}
	for _, dir := range []string{filepath.Dir(r.exe), filepath.Dir(onPath), filepath.Dir(self)} {
		if m, _ := filepath.Glob(filepath.Join(dir, ".monoagentcli-*")); len(m) != 0 {
			t.Errorf("files left behind in %s after a successful update: %v", dir, m)
		}
	}
	if strings.Count(out, "replaced it with v9.9.9") != 2 {
		t.Errorf("output does not say that the daemon's exe and the PATH CLI were both replaced:\n%s", out)
	}
}

// --- review findings

func script(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// A program that starts a background process holding its output, or prints without end, cannot hold the probe: the probe
// returns at the timeout, its whole process group is gone, and what it read of the output is bounded.
func TestTheProbeCannotBeHeldByAProgramItRuns(t *testing.T) {
	oldT, oldM := probeTimeout, probeMaxOut
	probeTimeout, probeMaxOut = 400*time.Millisecond, 4<<10
	defer func() { probeTimeout, probeMaxOut = oldT, oldM }()
	dir := t.TempDir()
	d := Deps{}

	pidFile := filepath.Join(dir, "pid")
	hang := script(t, dir, "hang", "sleep 60 &\necho $! > "+pidFile+"\nprintf '{\"version\":\"1.0.0\",\"build_date\":\"x\"}'\nsleep 60")
	start := time.Now()
	if _, err := d.probeVersion(context.Background(), hang); err == nil {
		t.Fatal("a program that never exits answered")
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("the probe was held for %s", took)
	}
	pid, _ := os.ReadFile(pidFile)
	time.Sleep(100 * time.Millisecond)
	if out, _ := exec.Command("sh", "-c", "kill -0 "+strings.TrimSpace(string(pid))+" 2>&1").CombinedOutput(); !strings.Contains(string(out), "No such process") {
		t.Errorf("the background process of the probed program is still running: %q", out)
	}

	flood := script(t, dir, "flood", "head -c 50000000 /dev/zero | tr '\\0' x")
	start = time.Now()
	if _, err := d.probeVersion(context.Background(), flood); err == nil {
		t.Fatal("endless output counted as a version")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("the probe was held by a flood of output")
	}
	cb := &cappedBuffer{max: 10}
	if n, _ := cb.Write([]byte("0123456789abcdef")); n != 16 || string(cb.b) != "0123456789" {
		t.Fatalf("capped buffer kept %q (wrote %d)", cb.b, n)
	}
}

// Only a real mono-agent answer counts: a version alone, or a text line without the build, is not one, and a version number
// is bounded so that it cannot overflow a comparison.
func TestOnlyAMonoagentAnswerWithARealVersionCounts(t *testing.T) {
	for out, want := range map[string]string{
		`{"version":"v1.2.3","build_date":"2026-01-01"}`: "1.2.3",
		"monoagentcli v0.150.2 (built 2026-01-01)":       "0.150.2",
		`{"version":"1.0"}`:                              "",
		"monoagentcli 0.1.0":                             "",
		"GNU bash":                                       "",
		`{"version":"","build_date":"x"}`:                "",
	} {
		got, err := parseProbe(out)
		if (err == nil) != (want != "") || got != want {
			t.Errorf("parseProbe(%q) = %q, %v; want %q", out, got, err, want)
		}
	}
	for v, ok := range map[string]bool{"1.2.3": true, "0.215.7": true, "1.2.3-rc.1": true, "1.2": false, "banana": false, "dev": false,
		"99999999999999999999.0.0": false, "1.2.3\n": false, "1.2.3; rm": false} {
		if versionOK(v) != ok {
			t.Errorf("versionOK(%q) = %v", v, !ok)
		}
	}
}

// A program is run to ask its version only if it, and the folder it is in, belong to this user (or root) and no one else
// can write them; and never when this command runs as root. Otherwise it is reported without being run.
func TestAProgramIsNotRunToAskItsVersionUnlessItIsSafeToRun(t *testing.T) {
	setup := func(t *testing.T, dirMode, fileMode os.FileMode) (string, string, *staleRig) {
		dir := filepath.Join(t.TempDir(), "bin")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		marker := filepath.Join(t.TempDir(), "ran")
		p := script(t, dir, "monoagentcli", "echo ran > "+marker+"\nprintf '{\"version\":\"0.176.0\",\"build_date\":\"x\"}'")
		if err := os.Chmod(p, fileMode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, dirMode); err != nil {
			t.Fatal(err)
		}
		r := newStaleRig(t, false)
		r.d.ProbeVersion = nil // the real probe
		r.d.LookPath = func(string) (string, error) { return p, nil }
		return p, marker, r
	}
	ran := func(marker string) bool { _, err := os.Stat(marker); return err == nil }

	t.Run("safe: run, and reported stale", func(t *testing.T) {
		p, marker, r := setup(t, 0o755, 0o755)
		got := r.d.staleCLIs(context.Background(), "v0.215.7", nil, nil)
		if !ran(marker) || len(got) != 1 || got[0].Version != "0.176.0" || got[0].Path != p {
			t.Fatalf("ran %v, stale = %+v", ran(marker), got)
		}
	})
	for name, c := range map[string]struct{ dir, file os.FileMode }{
		"a folder others can write":    {0o777, 0o755},
		"a folder the group can write": {0o775, 0o755},
		"a file others can write":      {0o755, 0o757},
		"a file the group can write":   {0o755, 0o775},
	} {
		t.Run(name, func(t *testing.T) {
			_, marker, r := setup(t, c.dir, c.file)
			got := r.d.staleCLIs(context.Background(), "v0.215.7", nil, nil)
			if ran(marker) {
				t.Fatal("a program that others can plant was executed")
			}
			if len(got) != 1 || got[0].Action != "stale" || !strings.Contains(got[0].Message, "not run to ask") {
				t.Fatalf("stale = %+v", got)
			}
		})
	}
	t.Run("as root nothing is run or replaced", func(t *testing.T) {
		old := isRoot
		isRoot = func() bool { return true }
		defer func() { isRoot = old }()
		_, marker, r := setup(t, 0o755, 0o755)
		got := r.d.staleCLIs(context.Background(), "v0.215.7", nil, nil)
		if ran(marker) {
			t.Fatal("a program was executed by a command running as root")
		}
		if len(got) != 1 || !strings.Contains(got[0].Message, "runs as root") {
			t.Fatalf("stale = %+v", got)
		}
		if why := replaceable(r.exe); !strings.Contains(why, "root") {
			t.Fatalf("replaceable as root: %q", why)
		}
	})
}

func TestTheDaemonExeIsNotReplacedWhenOthersCanWriteItOrItsFolder(t *testing.T) {
	for name, mod := range map[string]func(r *staleRig) error{
		"the file":   func(r *staleRig) error { return os.Chmod(r.exe, 0o775) },
		"the folder": func(r *staleRig) error { return os.Chmod(filepath.Dir(r.exe), 0o777) },
	} {
		r := newStaleRig(t, true)
		r.versions[r.exe] = "0.176.0"
		if err := mod(r); err != nil {
			t.Fatal(err)
		}
		got := r.d.staleCLIs(context.Background(), "v0.215.7", nil, []byte(newAsset))
		if len(got) != 1 || got[0].Action != "stale" || !strings.Contains(got[0].Message, "others can write") {
			t.Errorf("%s writable: %+v", name, got)
		}
		if b, _ := os.ReadFile(r.exe); string(b) != oldAsset {
			t.Errorf("%s writable: the exe was replaced", name)
		}
	}
}

// The CLI of the desktop app, a wrapper in a unit, and one file reached as both the daemon's exe and the PATH CLI.
func TestTheBundledCLIAWrapperAndASharedFileAreNeverReplaced(t *testing.T) {
	t.Run("the CLI beside the app", func(t *testing.T) {
		r := newStaleRig(t, true)
		r.versions[r.exe] = "0.176.0"
		if err := os.WriteFile(filepath.Join(filepath.Dir(r.exe), "MonoAgent-linux-amd64"), []byte("app"), 0o755); err != nil {
			t.Fatal(err)
		}
		if got := r.d.staleCLIs(context.Background(), "v0.215.7", nil, []byte(newAsset)); len(got) != 0 {
			t.Fatalf("the CLI of the desktop app was looked at: %+v", got)
		}
		if b, _ := os.ReadFile(r.exe); string(b) != oldAsset {
			t.Fatal("the bundled CLI was replaced")
		}
	})
	t.Run("inside a macOS bundle", func(t *testing.T) {
		if !isBundledCLI("/Applications/MonoAgent.app/Contents/MacOS/monoagentcli") {
			t.Fatal("a CLI inside an .app is not recognised as the app's")
		}
	})
	t.Run("a wrapper", func(t *testing.T) {
		r := newStaleRig(t, true)
		if err := os.WriteFile(r.unit, []byte("[Service]\nExecStart=/usr/bin/env monoagentcli daemon\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		r.versions["/usr/bin/env"] = "0.176.0"
		if got := r.d.staleCLIs(context.Background(), "v0.215.7", nil, []byte(newAsset)); len(got) != 0 {
			t.Fatalf("a wrapper named in the unit was treated as a CLI: %+v", got)
		}
	})
	t.Run("one file, two names", func(t *testing.T) {
		r := newStaleRig(t, true)
		r.versions[r.exe] = "0.176.0"
		link := filepath.Join(t.TempDir(), "monoagentcli")
		if err := os.Symlink(r.exe, link); err != nil {
			t.Fatal(err)
		}
		r.d.LookPath = func(string) (string, error) { return link, nil }
		r.versions[link] = "0.176.0"
		got := r.d.staleCLIs(context.Background(), "v0.215.7", nil, []byte(newAsset))
		if len(got) != 1 || got[0].Role != "path" || got[0].Action != "updated" || !got[0].runsDaemon {
			t.Fatalf("stale = %+v", got)
		}
		if b, _ := os.ReadFile(r.exe); string(b) != newAsset {
			t.Fatal("the file both names lead to was not replaced, so the daemon's restart would run the old one")
		}
		if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("the link was replaced by a file: %v", err)
		}
	})
}

// The new binary must run and say the released version before it takes the old one's place.
func TestTheDaemonExeIsKeptWhenTheNewBinaryDoesNotPassItsCheck(t *testing.T) {
	r := newStaleRig(t, true)
	r.versions[r.exe] = "0.176.0"
	r.newVer = "0.1.0" // the candidate says the wrong version
	got := r.d.staleCLIs(context.Background(), "v0.215.7", nil, []byte(newAsset))
	if len(got) != 1 || got[0].Action != "stale" || !strings.Contains(got[0].Message, "did not pass its check") {
		t.Fatalf("stale = %+v", got)
	}
	if b, _ := os.ReadFile(r.exe); string(b) != oldAsset {
		t.Fatal("the old exe was replaced by a binary that failed its check")
	}
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(r.exe), ".monoagentcli-update-*"))
	if len(matches) != 0 {
		t.Fatalf("the failed candidate was left behind: %v", matches)
	}
}

func TestShellQuotingOfAPathInACommandTheOperatorPastes(t *testing.T) {
	for in, want := range map[string]string{
		"/home/u/bin/monoagentcli":    "/home/u/bin/monoagentcli",
		"/home/u/my bin/monoagentcli": "'/home/u/my bin/monoagentcli'",
		"/home/u/it's/monoagentcli":   `'/home/u/it'\''s/monoagentcli'`,
		"/home/u/100%/monoagentcli":   "'/home/u/100%/monoagentcli'",
		"/home/u/a\nb/monoagentcli":   `$'/home/u/a\x0ab/monoagentcli'`,
		"/home/u/\x1b[31m/it's/m":     `$'/home/u/\x1b[31m/it\'s/m'`,
	} {
		if got := shQuote(in); got != want {
			t.Errorf("shQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- second review

// safeCLI makes <base>/bin/monoagentcli, a script that records that it ran and how it was called, with every folder from
// base down 0755; it returns the script path and the marker that shows it ran.
func safeCLI(t *testing.T, base string) (cli, marker string) {
	t.Helper()
	dir := filepath.Join(base, "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	marker = filepath.Join(t.TempDir(), "ran")
	cli = script(t, dir, "monoagentcli", "echo \"$0\" > "+marker+"\nprintf '{\"version\":\"0.176.0\",\"build_date\":\"x\"}'")
	return cli, marker
}

func realProbeRig(t *testing.T, cliOnPath string) *staleRig {
	t.Helper()
	r := newStaleRig(t, false)
	r.d.ProbeVersion = nil
	r.d.LookPath = func(string) (string, error) { return cliOnPath, nil }
	return r
}

// The permission checks and the run use the same file: the one the name resolves to. A link in a folder others can write is
// not run even though what it points at is safe, and a link in a safe folder runs its target, not the name.
func TestTheProbeRunsTheFileThePermissionChecksLookedAt(t *testing.T) {
	base := t.TempDir()
	cli, marker := safeCLI(t, filepath.Join(base, "real"))

	t.Run("a link in a folder others can write", func(t *testing.T) {
		linkDir := filepath.Join(t.TempDir(), "linkdir")
		if err := os.MkdirAll(linkDir, 0o755); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(linkDir, "monoagentcli")
		if err := os.Symlink(cli, link); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(linkDir, 0o777); err != nil {
			t.Fatal(err)
		}
		got := realProbeRig(t, link).d.staleCLIs(context.Background(), "v0.215.7", nil, nil)
		if _, err := os.Stat(marker); err == nil {
			t.Fatal("a program was run through a link that others can swap")
		}
		if len(got) != 1 || !strings.Contains(got[0].Message, "not run to ask") {
			t.Fatalf("stale = %+v", got)
		}
	})
	t.Run("a link in a safe folder runs its target", func(t *testing.T) {
		linkDir := filepath.Join(t.TempDir(), "linkdir")
		if err := os.MkdirAll(linkDir, 0o755); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(linkDir, "monoagentcli")
		if err := os.Symlink(cli, link); err != nil {
			t.Fatal(err)
		}
		got := realProbeRig(t, link).d.staleCLIs(context.Background(), "v0.215.7", nil, nil)
		ran, _ := os.ReadFile(marker)
		if len(got) != 1 || got[0].Version != "0.176.0" {
			t.Fatalf("stale = %+v", got)
		}
		if strings.TrimSpace(string(ran)) != realPath(cli) {
			t.Fatalf("the program was run as %q, want the resolved %q", ran, realPath(cli))
		}
	})
}

// Every folder above the program is checked, not only the one that holds it: a grandparent others can write (and that is not
// sticky) lets them rename the folder below it and put their own file in its place.
func TestEveryFolderAboveAProgramIsChecked(t *testing.T) {
	for name, c := range map[string]struct {
		mode os.FileMode // the grandparent
		ok   bool
	}{
		"others can write the grandparent":       {0o777, false},
		"the group can write the grandparent":    {0o775, false},
		"a sticky grandparent others can write":  {0o777 | os.ModeSticky, true},
		"a grandparent only the owner can write": {0o755, true},
	} {
		t.Run(name, func(t *testing.T) {
			grand := filepath.Join(t.TempDir(), "grand")
			cli, marker := safeCLI(t, filepath.Join(grand, "mid"))
			if err := os.Chmod(grand, c.mode); err != nil {
				t.Fatal(err)
			}
			defer os.Chmod(grand, 0o755)
			within(t, 10*time.Second, func() {
				got := realProbeRig(t, cli).d.staleCLIs(context.Background(), "v0.215.7", nil, nil)
				_, ran := os.Stat(marker)
				if (ran == nil) != c.ok {
					t.Errorf("ran = %v, want %v: %+v", ran == nil, c.ok, got)
				}
				// the same walk decides whether the daemon's exe may be replaced
				if why := replaceable(cli); (why == "") != c.ok {
					t.Errorf("replaceable = %q, want ok=%v", why, c.ok)
				}
			})
		})
	}
}

// Ownership: a file or folder of another user is not run or replaced.
func TestAFileOrFolderOfAnotherUserIsNeitherRunNorReplaced(t *testing.T) {
	cli, marker := safeCLI(t, t.TempDir())
	old := uidOf
	uidOf = func() int { return os.Getuid() + 12345 } // as if this were another user
	defer func() { uidOf = old }()
	got := realProbeRig(t, cli).d.staleCLIs(context.Background(), "v0.215.7", nil, nil)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a program of another user was run")
	}
	if len(got) != 1 || !strings.Contains(got[0].Message, "not run to ask") {
		t.Fatalf("stale = %+v", got)
	}
	if why := replaceable(cli); !strings.Contains(why, "not owned by this user") {
		t.Fatalf("replaceable = %q", why)
	}
}

// Only the app's executable makes a CLI "the app's": a note or a folder whose name starts the same does not hide it.
func TestOnlyTheAppsExecutableMakesACLITheBundledOne(t *testing.T) {
	for name, c := range map[string]struct {
		neighbour string
		dir       bool
		bundled   bool
	}{
		"the linux app":       {"MonoAgent-linux-amd64", false, true},
		"the windows app":     {"MonoAgent-windows-amd64.exe", false, true},
		"the macOS binary":    {"MonoAgent", false, true},
		"a backup folder":     {"MonoAgentBackup", true, false},
		"a note":              {"MonoAgent-notes.txt", false, false},
		"another program":     {"MonoAgentX", false, false},
		"the app, wrong case": {"monoagent-linux-amd64", false, false},
	} {
		dir := t.TempDir()
		cli := filepath.Join(dir, "monoagentcli")
		if err := os.WriteFile(cli, []byte(oldAsset), 0o755); err != nil {
			t.Fatal(err)
		}
		n := filepath.Join(dir, c.neighbour)
		if c.dir {
			_ = os.Mkdir(n, 0o755)
		} else {
			_ = os.WriteFile(n, []byte("x"), 0o644)
		}
		if got := isBundledCLI(cli); got != c.bundled {
			t.Errorf("%s: isBundledCLI = %v, want %v", name, got, c.bundled)
		}
	}
}

// A path with control characters is shown quoted, so it cannot write to the terminal.
func TestAPathWithControlCharactersIsShownQuoted(t *testing.T) {
	r := newStaleRig(t, false)
	evil := filepath.Join(t.TempDir(), "a\nb\x1b[31m", "monoagentcli")
	r.d.LookPath = func(string) (string, error) { return evil, nil }
	r.versions[evil] = "0.176.0"
	got := r.d.staleCLIs(context.Background(), "v0.215.7", nil, nil)
	if len(got) != 1 || strings.ContainsAny(got[0].Message, "\x1b\n\r") {
		t.Fatalf("a raw control character reached the message: %+v", got)
	}
}

// --- third review (tests only)

// within fails the test, rather than hanging it, when f takes longer than d.
func within(t *testing.T, d time.Duration, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("still running after %s", d)
	}
}

// A link in a safe folder that points at a file in an UNSAFE folder is not run: the folders of the resolved file are walked as
// well as those of the name.
func TestALinkInASafeFolderToAFileInAnUnsafeOneIsNotRun(t *testing.T) {
	unsafeBase := filepath.Join(t.TempDir(), "unsafe")
	cli, marker := safeCLI(t, unsafeBase)
	if err := os.Chmod(filepath.Dir(cli), 0o777); err != nil { // the folder that holds the file
		t.Fatal(err)
	}
	defer os.Chmod(filepath.Dir(cli), 0o755)
	linkDir := filepath.Join(t.TempDir(), "safe")
	if err := os.MkdirAll(linkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(linkDir, "monoagentcli")
	if err := os.Symlink(cli, link); err != nil {
		t.Fatal(err)
	}
	within(t, 10*time.Second, func() {
		got := realProbeRig(t, link).d.staleCLIs(context.Background(), "v0.215.7", nil, nil)
		if _, err := os.Stat(marker); err == nil {
			t.Error("a program in a folder others can write was run through a link in a safe one")
		}
		if len(got) != 1 || !strings.Contains(got[0].Message, "not run to ask") {
			t.Errorf("stale = %+v", got)
		}
	})
}

// A folder above the program that belongs to another user is refused, though the file itself is the user's: the owner is
// compared for the ancestors too. The first owner comparison (the file's) sees this user; the later ones (the folders') see
// another.
func TestAFolderOfAnotherUserAboveAProgramIsRefused(t *testing.T) {
	cli, marker := safeCLI(t, t.TempDir())
	old := uidOf
	calls := 0
	uidOf = func() int {
		calls++
		if calls == 1 {
			return os.Getuid()
		}
		return os.Getuid() + 12345
	}
	defer func() { uidOf = old }()
	within(t, 10*time.Second, func() {
		got := realProbeRig(t, cli).d.staleCLIs(context.Background(), "v0.215.7", nil, nil)
		if _, err := os.Stat(marker); err == nil {
			t.Error("a program under a folder of another user was run")
		}
		if len(got) != 1 || !strings.Contains(got[0].Message, "the folder") {
			t.Errorf("stale = %+v", got)
		}
	})
}

// --- rollback and bounded restart (#1358)

type rollbackRig struct {
	*staleRig
	fake *fakeAutostart
	hb   *fakeHeartbeat
}

func newRollbackRig(t *testing.T) *rollbackRig {
	t.Helper()
	r := newStaleRig(t, true)
	r.versions[r.exe] = "0.176.0"
	rr := &rollbackRig{staleRig: r, fake: &fakeAutostart{installed: true, where: r.unit}, hb: &fakeHeartbeat{}}
	rr.hb.write(os.Getpid(), "v1.0.0")
	rr.d.Heartbeat = rr.hb.read
	rr.d.Installer = func() autostart.Installer { return rr.fake }
	rr.d.ServicePID = func(context.Context, autostart.Installer) (int, error) { return os.Getpid(), nil }
	rr.d.VerifyWait, rr.d.VerifyTick = 200*time.Millisecond, 10*time.Millisecond
	return rr
}

// update: replace, restart the daemon, settle.
func (rr *rollbackRig) update(t *testing.T) (*DaemonUpdate, []StaleCLI) {
	t.Helper()
	list := rr.d.staleCLIs(context.Background(), "v0.215.7", nil, []byte(newAsset))
	return rr.d.settleReplaced(context.Background(), list, rr.d.daemonAfterUpdate(context.Background(), "v0.215.7")), list
}

func leftovers(t *testing.T, r *staleRig) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(filepath.Dir(r.exe), ".monoagentcli-*"))
	return m
}

func TestTheOldBinaryIsDroppedOnlyAfterTheNewOneBroughtTheDaemonUp(t *testing.T) {
	rr := newRollbackRig(t)
	rr.fake.onRestart = func() { rr.hb.write(os.Getpid(), "v0.215.7") }
	du, _ := rr.update(t)
	if du == nil || du.Action != "restarted" || du.RolledBack {
		t.Fatalf("du = %+v", du)
	}
	if b, _ := os.ReadFile(rr.exe); string(b) != newAsset {
		t.Fatalf("the new binary is not in place: %q", b)
	}
	if l := leftovers(t, rr.staleRig); len(l) != 0 {
		t.Fatalf("the old binary was kept after the daemon came up on the new one: %v", l)
	}
}

func TestTheOldBinaryIsPutBackWhenTheDaemonDoesNotComeUpOnTheNewOne(t *testing.T) {
	rr := newRollbackRig(t)
	calls := 0
	rr.fake.onRestart = func() { // the restart on the new binary brings nothing up; the one after the put-back does
		calls++
		if calls == 2 {
			rr.hb.write(os.Getpid()+1, "v1.0.0") // the rolled-back daemon is a new process
		}
	}
	du, _ := rr.update(t)
	if du == nil || du.Action != "failed" || du.Reason != "no_heartbeat" || !du.RolledBack {
		t.Fatalf("du = %+v", du)
	}
	if b, _ := os.ReadFile(rr.exe); string(b) != oldAsset {
		t.Fatalf("the old binary was not put back: %q", b)
	}
	if rr.fake.restarted != 2 || !strings.Contains(du.Message, "Rolled back") || !strings.Contains(du.Message, "is running it again") {
		t.Fatalf("restarted %d times; message: %s", rr.fake.restarted, du.Message)
	}
	if l := leftovers(t, rr.staleRig); len(l) != 0 {
		t.Fatalf("files left behind: %v", l)
	}
}

func TestAFailedRollbackSaysWhatToDo(t *testing.T) {
	t.Run("the daemon still does not come up", func(t *testing.T) {
		rr := newRollbackRig(t) // no restart brings a heartbeat
		du, _ := rr.update(t)
		if b, _ := os.ReadFile(rr.exe); string(b) != oldAsset {
			t.Fatal("the old binary was not put back")
		}
		if !du.RolledBack || !strings.Contains(du.Message, "no live heartbeat followed") || !strings.Contains(du.Message, "monoagentcli daemon start") {
			t.Fatalf("message: %s", du.Message)
		}
	})
	t.Run("the put-back is refused by the path rules", func(t *testing.T) {
		rr := newRollbackRig(t)
		list := rr.d.staleCLIs(context.Background(), "v0.215.7", nil, []byte(newAsset))
		if err := os.Chmod(filepath.Dir(rr.exe), 0o777); err != nil { // the folder became writable by others since
			t.Fatal(err)
		}
		defer os.Chmod(filepath.Dir(rr.exe), 0o755)
		du := rr.d.settleReplaced(context.Background(), list, rr.d.daemonAfterUpdate(context.Background(), "v0.215.7"))
		if du.RolledBack || !strings.Contains(du.Message, "could not be rolled back") || !strings.Contains(du.Message, "mv -f") {
			t.Fatalf("du = %+v", du)
		}
		if b, _ := os.ReadFile(rr.exe); string(b) != newAsset {
			t.Fatal("a file that fails the path rules was moved")
		}
		if l := leftovers(t, rr.staleRig); len(l) != 1 {
			t.Fatalf("the previous binary must be kept for the manual command: %v", l)
		}
	})
}

// A restart that the service manager never answers does not hang the update: it ends at the bound, and the roll back is tried.
func TestARestartThatNeverAnswersEndsAtTheBoundAndKeepsThePreviousBinary(t *testing.T) {
	old := restartBound
	restartBound = 150 * time.Millisecond
	defer func() { restartBound = old }()
	rr := newRollbackRig(t)
	rr.fake.block = true
	start := time.Now()
	var du *DaemonUpdate
	within(t, 10*time.Second, func() { du, _ = rr.update(t) })
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("the update took %s", took)
	}
	if du == nil || du.Action != "failed" || du.Reason != "timed_out" || du.RolledBack {
		t.Fatalf("du = %+v", du)
	}
	// a restart that may still be in flight is not raced by a second one, and the new binary is not pulled from under it
	if rr.fake.restarted != 1 {
		t.Fatalf("restarted %d times, want only the one that timed out", rr.fake.restarted)
	}
	if b, _ := os.ReadFile(rr.exe); string(b) != newAsset {
		t.Fatalf("the binary was rolled back under a restart that may still be running: %q", b)
	}
	if kept := leftovers(t, rr.staleRig); len(kept) != 1 || !strings.Contains(du.Message, "kept at") || !strings.Contains(du.Message, "mv -f") {
		t.Fatalf("the previous binary must be kept for the operator: %v; message: %s", kept, du.Message)
	}
}

// A daemon that has started its process but not yet written a heartbeat of the new version may be migrating: the old binary is
// not put under it, it is kept.
func TestADaemonThatIsStillStartingIsNotRolledBack(t *testing.T) {
	rr := newRollbackRig(t)
	calls := 0
	rr.d.ServicePID = func(context.Context, autostart.Installer) (int, error) { // the daemon's pid before the restart, a new process after
		calls++
		if calls == 1 {
			return os.Getpid(), nil
		}
		return os.Getpid() + 7, nil
	}
	du, _ := rr.update(t)
	if du == nil || du.Action != "failed" || du.Reason != "still_starting" || du.RolledBack || rr.fake.restarted != 1 {
		t.Fatalf("du = %+v, restarted %d", du, rr.fake.restarted)
	}
	if b, _ := os.ReadFile(rr.exe); string(b) != newAsset {
		t.Fatalf("the binary was rolled back under a daemon that may be starting: %q", b)
	}
	if kept := leftovers(t, rr.staleRig); len(kept) != 1 || !strings.Contains(du.Message, "may still be starting") || !strings.Contains(du.Message, "mv -f") {
		t.Fatalf("kept %v; message: %s", kept, du.Message)
	}
}

// Only a daemon that was stopped and not started again, or whose process is gone, is rolled back; any other failure of the
// service manager is not the binary's.
func TestOnlyTheRightFailuresRollTheBinaryBack(t *testing.T) {
	t.Run("stopped and not started again", func(t *testing.T) {
		rr := newRollbackRig(t)
		calls := 0
		rr.fake.restartErr = &autostart.StoppedError{Err: errors.New("start failed")}
		rr.fake.onRestart = func() {}
		old := rr.fake
		_ = old
		// the second call (the rollback's) must work: the fake fails the first only
		fail := rr.fake.restartErr
		rr.d.Installer = func() autostart.Installer {
			return &failFirstRestart{fakeAutostart: rr.fake, err: fail, calls: &calls, hb: rr.hb}
		}
		du, _ := rr.update(t)
		if du == nil || du.Reason != "stopped" || !du.RolledBack {
			t.Fatalf("du = %+v", du)
		}
		if b, _ := os.ReadFile(rr.exe); string(b) != oldAsset {
			t.Fatalf("not rolled back after a daemon stopped and not started: %q", b)
		}
	})
	t.Run("any other failure of the service manager", func(t *testing.T) {
		rr := newRollbackRig(t)
		rr.fake.restartErr = errors.New("systemctl: access denied")
		du, _ := rr.update(t)
		if du == nil || du.Action != "failed" || du.Reason != "" || du.RolledBack {
			t.Fatalf("du = %+v", du)
		}
		if b, _ := os.ReadFile(rr.exe); string(b) != newAsset {
			t.Fatalf("rolled back for a failure that is not the binary's: %q", b)
		}
	})
}

// failFirstRestart is a service manager whose first restart fails with err and whose later ones bring a daemon up.
type failFirstRestart struct {
	*fakeAutostart
	err   error
	calls *int
	hb    *fakeHeartbeat
}

func (f *failFirstRestart) Restart(ctx context.Context) error {
	*f.calls++
	f.fakeAutostart.restarted++
	if *f.calls == 1 {
		return f.err
	}
	f.hb.write(os.Getpid()+1, "v1.0.0")
	return nil
}

// A heartbeat that the daemon that was restarted is still writing does not pass for the rolled-back daemon.
func TestTheRolledBackDaemonIsAnotherProcessThanTheOneRestarted(t *testing.T) {
	rr := newRollbackRig(t)
	rr.fake.onRestart = func() { rr.hb.write(os.Getpid(), "v1.0.0") } // the same pid keeps answering, on the old version
	du, _ := rr.update(t)
	if du == nil || !du.RolledBack || !strings.Contains(du.Message, "no live heartbeat followed") {
		t.Fatalf("du = %+v", du)
	}
}

// A file the user keeps as <binary>.bak is not ours to delete, and the CLI on PATH, which the daemon does not start, is kept
// whatever happens to the daemon.
func TestTheUsersOwnBakFileAndAPathCLIAreLeftAlone(t *testing.T) {
	rr := newRollbackRig(t)
	if err := os.WriteFile(rr.exe+".bak", []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	onPath := filepath.Join(t.TempDir(), "pathbin", "monoagentcli")
	if err := os.MkdirAll(filepath.Dir(onPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(onPath, []byte(oldAsset), 0o755); err != nil {
		t.Fatal(err)
	}
	rr.d.LookPath = func(string) (string, error) { return onPath, nil }
	rr.versions[onPath] = "0.176.0"
	du, list := rr.update(t) // the daemon does not come up: its exe is rolled back, the PATH CLI is not
	if b, _ := os.ReadFile(rr.exe + ".bak"); string(b) != "mine" {
		t.Fatalf("the user's own .bak file was changed: %q", b)
	}
	if b, _ := os.ReadFile(onPath); string(b) != newAsset {
		t.Fatalf("the PATH CLI was rolled back with the daemon's: %q", b)
	}
	if len(list) != 2 || du == nil || !du.RolledBack {
		t.Fatalf("list %+v, du %+v", list, du)
	}
	if l, _ := filepath.Glob(filepath.Join(filepath.Dir(onPath), ".monoagentcli-previous-*")); len(l) != 0 {
		t.Fatalf("the PATH CLI's previous binary was kept: %v", l)
	}
}

// With no daemon running, the new binary is not in doubt: the old one is dropped.
func TestWithNoDaemonRunningTheOldBinaryIsDropped(t *testing.T) {
	rr := newRollbackRig(t)
	rr.hb = &fakeHeartbeat{} // no heartbeat: no daemon
	rr.d.Heartbeat = rr.hb.read
	du, _ := rr.update(t)
	if du != nil {
		t.Fatalf("du = %+v", du)
	}
	if b, _ := os.ReadFile(rr.exe); string(b) != newAsset {
		t.Fatalf("not replaced: %q", b)
	}
	if l := leftovers(t, rr.staleRig); len(l) != 0 {
		t.Fatalf("left behind: %v", l)
	}
}

// --- the swap

// The swap keeps the target in place at every moment: the new file is renamed over it, and the old one is held by a link or a
// copy under a name of its own until Commit; Restore puts it back. The new file has exactly the mode 0755.
func TestTheSwapReplacesInOneRenameAndKeepsTheOldFileUntilCommit(t *testing.T) {
	for name, noLink := range map[string]bool{"with a hard link": false, "with a copy where there are no links": true} {
		t.Run(name, func(t *testing.T) {
			if noLink {
				old := linkFile
				linkFile = func(string, string) error { return errors.New("no hard links here") }
				defer func() { linkFile = old }()
			}
			dir := t.TempDir()
			target := filepath.Join(dir, "monoagentcli")
			if err := os.WriteFile(target, []byte(oldAsset), 0o700); err != nil {
				t.Fatal(err)
			}
			r, err := installBinaryKeep([]byte(newAsset), target, nil)
			if err != nil {
				t.Fatal(err)
			}
			if b, _ := os.ReadFile(target); string(b) != newAsset {
				t.Fatalf("not replaced: %q", b)
			}
			if fi, _ := os.Stat(target); fi.Mode().Perm() != 0o755 {
				t.Errorf("the new file's mode is %v, want 0755", fi.Mode().Perm())
			}
			if b, _ := os.ReadFile(r.bak); string(b) != oldAsset {
				t.Fatalf("the old file is not kept: %q", b)
			}
			if err := r.Restore(); err != nil {
				t.Fatal(err)
			}
			if b, _ := os.ReadFile(target); string(b) != oldAsset {
				t.Fatalf("not restored: %q", b)
			}
			if m, _ := filepath.Glob(filepath.Join(dir, ".monoagentcli-*")); len(m) != 0 {
				t.Fatalf("left behind: %v", m)
			}
		})
	}
}

// `update` found through a link replaces the file behind it and keeps the link.
func TestUpdateOfABinaryFoundThroughALinkReplacesTheFileBehindIt(t *testing.T) {
	asset := updateAssetName()
	sum := sha256.Sum256([]byte(newAsset))
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			fmt.Fprintf(w, `{"tag_name":"v9.9.9","assets":[{"name":%q,"browser_download_url":"%s/bin"},{"name":"SHA256SUMS.txt","browser_download_url":"%s/sums"}]}`, asset, srv.URL, srv.URL)
		case "/bin":
			_, _ = w.Write([]byte(newAsset))
		case "/sums":
			fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), asset)
		}
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	real := filepath.Join(dir, "share", "monoagentcli")
	link := filepath.Join(dir, "bin", "monoagentcli")
	for _, d := range []string{filepath.Dir(real), filepath.Dir(link)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(real, []byte(oldAsset), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	r := newStaleRig(t, false)
	r.newVer = "9.9.9"
	d := r.d
	d.LatestReleaseURL = srv.URL + "/latest"
	d.Version = func() string { return "v1.0.0" }
	d.JSONOutput = func() bool { return false }
	d.SelfPath = func() (string, error) { return link, nil }
	captureStdout(t, func() {
		if err := d.run(NewCommand(d)); err != nil {
			t.Fatal(err)
		}
	})
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link was replaced by a file: %v", err)
	}
	if b, _ := os.ReadFile(real); string(b) != newAsset {
		t.Fatalf("the file behind the link was not replaced: %q", b)
	}
}

// --- review 1358-c/d

// The common layout: `update` replaces the very file the daemon service starts. A new version that cannot bring the daemon up
// is put back for it too, and an update that went well drops the old one.
func TestUpdateOfTheBinaryTheDaemonServiceStartsIsRolledBackWhenItDoesNotComeUp(t *testing.T) {
	for _, tc := range []struct {
		name       string
		comesUp    bool
		wantBinary string
	}{{"the daemon comes up", true, newAsset}, {"the daemon does not come up", false, oldAsset}} {
		t.Run(tc.name, func(t *testing.T) {
			asset := updateAssetName()
			sum := sha256.Sum256([]byte(newAsset))
			var srv *httptest.Server
			srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/latest":
					fmt.Fprintf(w, `{"tag_name":"v9.9.9","assets":[{"name":%q,"browser_download_url":"%s/bin"},{"name":"SHA256SUMS.txt","browser_download_url":"%s/sums"}]}`, asset, srv.URL, srv.URL)
				case "/bin":
					_, _ = w.Write([]byte(newAsset))
				case "/sums":
					fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), asset)
				}
			}))
			t.Cleanup(srv.Close)
			rr := newRollbackRig(t)
			rr.newVer = "9.9.9"
			rr.d.LatestReleaseURL = srv.URL + "/latest"
			rr.d.Version = func() string { return "v1.0.0" }
			rr.d.JSONOutput = func() bool { return false }
			rr.d.SelfPath = func() (string, error) { return rr.exe, nil } // this binary is the daemon's exe
			restarts := 0
			rr.fake.onRestart = func() {
				restarts++
				switch {
				case restarts == 1 && tc.comesUp:
					rr.hb.write(os.Getpid(), "v9.9.9")
				case restarts == 2:
					rr.hb.write(os.Getpid()+1, "v1.0.0")
				}
			}
			out := captureStdout(t, func() {
				if err := rr.d.run(NewCommand(rr.d)); err != nil {
					t.Fatal(err)
				}
			})
			if b, _ := os.ReadFile(rr.exe); string(b) != tc.wantBinary {
				t.Fatalf("the binary is %q; output:\n%s", b, out)
			}
			if !tc.comesUp && !strings.Contains(out, "Rolled back") {
				t.Fatalf("the rollback is not reported:\n%s", out)
			}
			if l := leftovers(t, rr.staleRig); len(l) != 0 {
				t.Fatalf("left behind: %v", l)
			}
		})
	}
}

// A previous binary left by an update that was killed is removed by the next one that works in the same folder; a recent one,
// and anything else, stays.
func TestAStalePreviousBinaryIsSweptAndNothingElse(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string, age time.Duration) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(-age)
		_ = os.Chtimes(p, at, at)
		return p
	}
	oldPrev := mk(fmt.Sprintf(".monoagentcli-previous-%d-aaa", time.Now().Add(-48*time.Hour).Unix()), 0)
	newPrev := mk(fmt.Sprintf(".monoagentcli-previous-%d-bbb", time.Now().Add(-time.Minute).Unix()), 0)
	noTime := mk(".monoagentcli-previous-notatime", 48*time.Hour)
	other := mk("something-else", 48*time.Hour)
	bak := mk("monoagentcli.bak", 48*time.Hour)
	target := mk("monoagentcli", time.Hour)
	if _, err := installBinaryKeep([]byte(newAsset), target, nil); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]bool{oldPrev: false, newPrev: true, noTime: true, other: true, bak: true} {
		if _, err := os.Stat(p); (err == nil) != want {
			t.Errorf("%s: exists = %v, want %v", filepath.Base(p), err == nil, want)
		}
	}
}

// `update` found through a link it cannot follow does not swap the link for a file.
func TestUpdateThroughADanglingLinkIsRefused(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "monoagentcli")
	if err := os.Symlink(filepath.Join(dir, "gone"), link); err != nil {
		t.Fatal(err)
	}
	if !isLink(link) {
		t.Fatal("isLink is false for a link")
	}
	if isLink(dir) {
		t.Fatal("isLink is true for a folder")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"tag_name":"v9.9.9","assets":[]}`)
	}))
	t.Cleanup(srv.Close)
	r := newStaleRig(t, false)
	d := r.d
	d.LatestReleaseURL = srv.URL
	d.Version = func() string { return "v1.0.0" }
	d.JSONOutput = func() bool { return false }
	d.SelfPath = func() (string, error) { return link, nil }
	var err error
	captureStdout(t, func() { err = d.run(NewCommand(d)) })
	if err == nil || !strings.Contains(err.Error(), "cannot be followed") {
		t.Fatalf("err = %v", err)
	}
	if !isLink(link) {
		t.Fatal("the link was replaced")
	}
}

// The copy that keeps the old binary where there are no hard links has the old file's mode whatever the umask, and is synced.
func TestTheCopyOfTheOldBinaryKeepsItsModeWhateverTheUmask(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.WriteFile(src, []byte(oldAsset), 0o755); err != nil {
		t.Fatal(err)
	}
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	dst := filepath.Join(dir, "dst")
	if err := copyFileMode(src, dst); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(dst); fi.Mode().Perm() != 0o755 {
		t.Fatalf("the copy's mode is %v, want 0755", fi.Mode().Perm())
	}
	if b, _ := os.ReadFile(dst); string(b) != oldAsset {
		t.Fatalf("copy = %q", b)
	}
}

// `update` of itself runs the new binary first and installs it only if it says the released version.
func TestUpdateKeepsItselfWhenTheNewBinaryDoesNotSayTheReleasedVersion(t *testing.T) {
	asset := updateAssetName()
	sum := sha256.Sum256([]byte(newAsset))
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			fmt.Fprintf(w, `{"tag_name":"v9.9.9","assets":[{"name":%q,"browser_download_url":"%s/bin"},{"name":"SHA256SUMS.txt","browser_download_url":"%s/sums"}]}`, asset, srv.URL, srv.URL)
		case "/bin":
			_, _ = w.Write([]byte(newAsset))
		case "/sums":
			fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), asset)
		}
	}))
	t.Cleanup(srv.Close)
	self := filepath.Join(t.TempDir(), "monoagentcli")
	if err := os.WriteFile(self, []byte(oldAsset), 0o755); err != nil {
		t.Fatal(err)
	}
	r := newStaleRig(t, false)
	r.newVer = "0.1.0" // the downloaded binary says another version
	d := r.d
	d.LatestReleaseURL = srv.URL + "/latest"
	d.Version = func() string { return "v1.0.0" }
	d.JSONOutput = func() bool { return false }
	d.SelfPath = func() (string, error) { return self, nil }
	var err error
	captureStdout(t, func() { err = d.run(NewCommand(d)) })
	if err == nil || !strings.Contains(err.Error(), "did not pass its check") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(self); string(b) != oldAsset {
		t.Fatalf("the binary was replaced by one that failed its check: %q", b)
	}
}

// The backup of one binary survives the installing of another in the same folder, even though a hard link carries the old
// file's modification time (which may be a year old): it is the time the backup was made that counts, not the file's.
func TestABackupJustMadeIsNotSweptByTheNextInstallInTheSameFolder(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(oldAsset), 0o755); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(-400 * 24 * time.Hour) // a binary built over a year ago
		_ = os.Chtimes(p, at, at)
		return p
	}
	a, b := mk("monoagentcli"), mk("monoagentcli-other")
	ra, err := installBinaryKeep([]byte(newAsset), a, nil)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := installBinaryKeep([]byte(newAsset), b, nil) // sweeps the folder first
	if err != nil {
		t.Fatal(err)
	}
	if err := ra.Restore(); err != nil {
		t.Fatalf("the first backup was swept by the second install: %v", err)
	}
	if x, _ := os.ReadFile(a); string(x) != oldAsset {
		t.Fatalf("restored %q", x)
	}
	rb.Commit()
}
