package monomind

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// shimFixture is the #301 layout: monomind reached through a mise shim,
// and a project whose .tool-versions points node at a planted .cache/n.
type shimFixture struct {
	home, data, shims, project, log string
	mono, node                      string // the real installs' bin dirs
}

func testHandshake(t *testing.T) string {
	b, err := json.Marshal(VersionInfo{V: ProtocolVersion, Version: "9.0.0", MinCaller: "1.0.0", Capabilities: RequiredCapabilities})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// newShimFixture builds it with monomind installed under
// installs/<monoInstall>/bin (node's own dir when monoInstall is
// "node/22.0.0"), node under installs/node/22.0.0/bin, and PATH = the shims
// dir first. The mise shim run in the project picks the planted binaries:
// the fixture checks that before returning, so the tests test the attack.
func newShimFixture(t *testing.T, monoInstall string) *shimFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell shims are unix-only")
	}
	f := &shimFixture{home: t.TempDir(), project: t.TempDir()}
	f.data = filepath.Join(f.home, ".local", "share", "mise")
	f.shims = filepath.Join(f.data, "shims")
	f.mono = filepath.Join(f.data, "installs", filepath.FromSlash(monoInstall), "bin")
	f.node = filepath.Join(f.data, "installs", "node", "22.0.0", "bin")
	f.log = filepath.Join(t.TempDir(), "pin.log")
	for k, v := range map[string]string{
		"HOME": f.home, "MISE_DATA_DIR": f.data, "PIN_LOG": f.log, EnvOverride: "",
		"XDG_DATA_HOME": "", "RTX_DATA_DIR": "", "ASDF_DATA_DIR": "", "VOLTA_HOME": "", "MISE_NODE_VERSION": "",
	} {
		t.Setenv(k, v)
	}
	tools := t.TempDir()
	src, err := os.ReadFile(filepath.Join("testdata", "fake-mise.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tools, "mise"), src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(f.shims, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"monomind", "node", "codex", "opencode"} {
		if err := os.Symlink(filepath.Join(tools, "mise"), filepath.Join(f.shims, name)); err != nil {
			t.Fatal(err)
		}
	}
	build := exec.Command("sh", filepath.Join("testdata", "make-shim-fixture.sh"), f.mono, f.node, f.project, testHandshake(t))
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v: %s", err, out)
	}
	t.Setenv("PATH", f.shims+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	ResetCapabilityCache()
	t.Cleanup(ResetCapabilityCache)

	attack := exec.Command(filepath.Join(f.shims, "monomind"), "--version", "--json")
	attack.Dir = f.project
	if out, err := attack.CombinedOutput(); err != nil {
		t.Fatalf("shim in project: %v: %s", err, out)
	}
	if !strings.Contains(f.readLog(t), "PLANTED") {
		t.Fatalf("fixture: the shim run in the project did not pick the planted binary; log:\n%s", f.readLog(t))
	}
	if err := os.Remove(f.log); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *shimFixture) readLog(t *testing.T) string {
	b, err := os.ReadFile(f.log)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

// assertRanReal fails when anything planted ran, and unless the real
// monomind ran on the real node.
func (f *shimFixture) assertRanReal(t *testing.T) {
	t.Helper()
	log := f.readLog(t)
	if strings.Contains(log, "PLANTED") {
		t.Fatalf("a planted binary ran:\n%s", log)
	}
	for _, want := range []string{"node " + filepath.Join(f.node, "node"), "monomind " + filepath.Join(f.mono, "monomind")} {
		if !strings.Contains(log, want) {
			t.Fatalf("log lacks %q:\n%s", want, log)
		}
	}
}

func TestFindResolvesMiseShimFromNeutralDir(t *testing.T) {
	f := newShimFixture(t, "node/22.0.0")
	// The process itself runs in the project, as `monoagentcli org …`
	// started there would.
	t.Chdir(f.project)
	bin, err := Find()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(f.mono, "monomind"); bin != want {
		t.Fatalf("Find() = %s, want the installed %s", bin, want)
	}
}

func TestOrgCommandsNeverRunPlantedBinary(t *testing.T) {
	for _, install := range []string{
		"node/22.0.0",           // node next to monomind (a mise node's npm -g)
		"npm-monomindcli/1.0.0", // node only through the shim on PATH
	} {
		t.Run(install, func(t *testing.T) {
			f := newShimFixture(t, install)
			ctx := context.Background()
			if _, err := OrgStatus(ctx, f.project, "growth"); err != nil {
				t.Fatalf("OrgStatus: %v", err)
			}
			f.assertRanReal(t)
			os.Remove(f.log)
			if _, err := OrgRun(ctx, f.project, "growth", "", false); err != nil {
				t.Fatalf("OrgRun: %v", err)
			}
			f.assertRanReal(t)
			os.Remove(f.log)
			if _, err := runOrgText(ctx, f.project, "validate", "growth"); err != nil {
				t.Fatalf("runOrgText: %v", err)
			}
			f.assertRanReal(t)
		})
	}
}

// An activated shell's per-project mise setting in this process's own
// environment doesn't reach the resolution either.
func TestShimResolutionDropsProjectManagerEnv(t *testing.T) {
	f := newShimFixture(t, "node/22.0.0")
	t.Setenv("MISE_NODE_VERSION", "path:"+filepath.Join(f.project, ".cache", "n"))
	bin, err := Find()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(f.mono, "monomind"); bin != want {
		t.Fatalf("Find() = %s, want %s", bin, want)
	}
}

// A shim whose global pick lies outside the manager's installs dir fails
// closed instead of running it.
func TestShimResolvingOutsideInstallsFailsClosed(t *testing.T) {
	f := newShimFixture(t, "node/22.0.0")
	if err := os.WriteFile(filepath.Join(f.home, ".tool-versions"), []byte("node path:"+filepath.Join(f.project, ".cache", "n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Find()
	if err == nil || !strings.Contains(err.Error(), "outside mise's installs dir") {
		t.Fatalf("Find() err = %v, want an outside-installs refusal", err)
	}
	if _, err := OrgStatus(context.Background(), f.project, "growth"); err == nil {
		t.Fatal("OrgStatus ran with an unpinnable monomind")
	}
	if strings.Contains(f.readLog(t), "PLANTED") {
		t.Fatalf("a planted binary ran:\n%s", f.readLog(t))
	}
}

func TestProjectLocalBinaryFailsClosed(t *testing.T) {
	f := newShimFixture(t, "node/22.0.0")
	planted := filepath.Join(f.project, ".cache", "n", "bin", "monomind")
	outside := filepath.Join(f.mono, "monomind")
	link := filepath.Join(f.project, "monomind-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	for _, bin := range []string{planted, link} {
		t.Setenv(EnvOverride, bin)
		ResetCapabilityCache()
		_, err := OrgStatus(context.Background(), f.project, "growth")
		if err == nil || !strings.Contains(err.Error(), "project-local binary") {
			t.Fatalf("%s: OrgStatus err = %v, want a project-local refusal", bin, err)
		}
		if err := CheckOutside(bin, f.project); err == nil {
			t.Fatalf("CheckOutside(%s) passed", bin)
		}
	}
	if strings.Contains(f.readLog(t), "PLANTED") {
		t.Fatalf("a planted binary ran:\n%s", f.readLog(t))
	}
	if _, err := Exec(context.Background(), ExecOptions{Bin: planted, Runtime: "claude", Prompt: "p", Cwd: f.project}, nil); err == nil ||
		!strings.Contains(err.Error(), "project-local binary") {
		t.Fatalf("Exec err = %v, want a project-local refusal", err)
	}
}

// A plain install (npm -g, Homebrew, /usr/bin) is returned as found, runs
// on the node next to it, and passes the project check.
func TestNormalInstallUnchanged(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fakes are unix-only")
	}
	prefix := filepath.Join(t.TempDir(), "npm-global", "bin")
	project := t.TempDir()
	log := filepath.Join(t.TempDir(), "pin.log")
	t.Setenv("PIN_LOG", log)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	build := exec.Command("sh", filepath.Join("testdata", "make-shim-fixture.sh"), prefix, prefix, project, testHandshake(t))
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v: %s", err, out)
	}
	t.Setenv("PATH", "/usr/bin"+string(os.PathListSeparator)+"/bin")
	t.Setenv(EnvOverride, filepath.Join(prefix, "monomind"))
	ResetCapabilityCache()
	t.Cleanup(ResetCapabilityCache)

	bin, err := Find()
	if err != nil {
		t.Fatal(err)
	}
	if bin != filepath.Join(prefix, "monomind") {
		t.Fatalf("Find() = %s, want it unchanged", bin)
	}
	if _, err := OrgStatus(context.Background(), project, "growth"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "node "+filepath.Join(prefix, "node")) || strings.Contains(string(b), "PLANTED") {
		t.Fatalf("log:\n%s", b)
	}
	env := PinEnv([]string{"A=1", "PATH=/x" + string(os.PathListSeparator) + prefix}, bin)
	if got := env[len(env)-1]; got != "PATH="+prefix+string(os.PathListSeparator)+"/x" {
		t.Fatalf("PinEnv PATH = %s", got)
	}
}

// Volta: monomind in ~/.volta/bin links to volta-shim, which picks per
// project; `volta which` from the neutral dir gives the image install.
func TestFindResolvesVoltaShim(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fakes are unix-only")
	}
	home, project := t.TempDir(), t.TempDir()
	volta := filepath.Join(home, ".volta")
	image := filepath.Join(volta, "tools", "image", "node", "22.0.0", "bin")
	log := filepath.Join(t.TempDir(), "pin.log")
	for k, v := range map[string]string{"HOME": home, "VOLTA_HOME": volta, "PIN_LOG": log, EnvOverride: "", "MISE_DATA_DIR": "", "ASDF_DATA_DIR": ""} {
		t.Setenv(k, v)
	}
	build := exec.Command("sh", filepath.Join("testdata", "make-shim-fixture.sh"), image, image, project, testHandshake(t))
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v: %s", err, out)
	}
	bin := filepath.Join(volta, "bin")
	// volta-shim runs the planted binary whenever the cwd has a
	// .tool-versions (standing in for package.json's "volta" pin).
	shim := "#!/bin/sh\nif [ -f .tool-versions ]; then exec ./.cache/n/bin/$(basename \"$0\") \"$@\"; fi\nexec " + image + "/$(basename \"$0\") \"$@\"\n"
	voltaCLI := "#!/bin/sh\n[ \"$1\" = which ] || exit 2\nif [ -f .tool-versions ]; then echo \"$PWD/.cache/n/bin/$2\"; else echo " + image + "/$2; fi\n"
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"volta-shim": shim, "volta": voltaCLI} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"monomind", "node"} {
		if err := os.Symlink(filepath.Join(bin, "volta-shim"), filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	ResetCapabilityCache()
	t.Cleanup(ResetCapabilityCache)
	t.Chdir(project)

	got, err := Find()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(image, "monomind"); got != want {
		t.Fatalf("Find() = %s, want %s", got, want)
	}
	if _, err := OrgStatus(context.Background(), project, "growth"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(log); strings.Contains(string(b), "PLANTED") || !strings.Contains(string(b), "node "+filepath.Join(image, "node")) {
		t.Fatalf("log:\n%s", b)
	}
}

// The agent CLIs monomind starts by name in the project root (codex here)
// are passed pinned, and the shims dir is dropped from its PATH, so a CLI
// that can't be pinned (opencode, not installed) fails to start rather
// than running the project's. rev278 reproduced a planted codex run with
// --sandbox danger-full-access through `agent exec --runtime codex`.
func TestAgentRuntimesPinnedAndShimsDropped(t *testing.T) {
	t.Run("bin-paths", func(t *testing.T) { testAgentRuntimesPinned(t, false) })
	t.Run("fallback", func(t *testing.T) { testAgentRuntimesPinned(t, true) })
}

func testAgentRuntimesPinned(t *testing.T, fallback bool) {
	f := newShimFixture(t, "node/22.0.0")
	codex := filepath.Join(f.data, "installs", "npm-openai-codex", "1.0.0", "bin", "codex")
	if err := os.MkdirAll(filepath.Dir(codex), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(codex, []byte("#!/bin/sh\necho \"codex $0\" >>\"$PIN_LOG\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_RUNTIMES", "codex opencode")
	if fallback {
		// mise can't list its global tools: its shims dir stays on PATH,
		// last, and the unpinnable opencode must still not reach it.
		t.Setenv("FAKE_MISE_NO_BIN_PATHS", "1")
	}
	ctx := context.Background()
	for name, run := range map[string]func() error{
		"org status": func() error { _, err := OrgStatus(ctx, f.project, "growth"); return err },
		"agent exec": func() error {
			_, err := Exec(ctx, ExecOptions{Runtime: "codex", Prompt: "p", Cwd: f.project, Access: AccessFull}, nil)
			return err
		},
	} {
		if err := run(); err != nil && name == "org status" {
			t.Fatalf("%s: %v", name, err)
		}
		log := f.readLog(t)
		if strings.Contains(log, "PLANTED") || !strings.Contains(log, "codex "+codex) {
			t.Fatalf("%s ran:\n%s", name, log)
		}
		os.Remove(f.log)
	}

	bin, err := Find()
	if err != nil {
		t.Fatal(err)
	}
	// The operator's own override naming the shim is pinned too.
	env := PinEnv(append(os.Environ(), "CODEX_CLI_BIN="+filepath.Join(f.shims, "codex"), "OPENCODE_BIN=opencode"), bin)
	got := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = v
	}
	if got["CODEX_CLI_BIN"] != codex {
		t.Errorf("CODEX_CLI_BIN = %q, want %s", got["CODEX_CLI_BIN"], codex)
	}
	if v, ok := got["OPENCODE_BIN"]; ok != fallback || (fallback && v != filepath.Join(f.home, ".monoagent", "unpinned", "opencode")) {
		t.Errorf("OPENCODE_BIN = %q (set %v), want it unset, or the unpinned path in the fallback", v, ok)
	}
	if !fallback {
		for _, p := range filepath.SplitList(got["PATH"]) {
			if p == f.shims {
				t.Errorf("PATH still has the shims dir: %s", got["PATH"])
			}
		}
	}
}

func pathOf(env []string) []string {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			return filepath.SplitList(v)
		}
	}
	return nil
}

func envValue(env []string, key string) (string, bool) {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			return v, true
		}
	}
	return "", false
}

func indexOf(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return -1
}

// The shims dir is replaced by mise's global tool bin dirs, after the
// system dirs; when mise can't list them, the shims dir goes last. The
// pinned agent CLIs are the same either way.
func TestPinEnvGlobalToolDirs(t *testing.T) {
	f := newShimFixture(t, "node/22.0.0")
	codexDir := filepath.Join(f.data, "installs", "npm-openai-codex", "1.0.0", "bin")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexDir, "codex"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	bin, err := Find()
	if err != nil {
		t.Fatal(err)
	}
	env := PinEnv(os.Environ(), bin)
	path := pathOf(env)
	if path[0] != f.node || indexOf(path, f.shims) >= 0 || indexOf(path, codexDir) < indexOf(path, "/bin") || indexOf(path, "/bin") < 0 {
		t.Fatalf("PATH = %v, want the pinned node first, the system dirs, then %s, and no shims", path, codexDir)
	}
	if v, _ := envValue(env, "CODEX_CLI_BIN"); v != filepath.Join(codexDir, "codex") {
		t.Fatalf("CODEX_CLI_BIN = %q", v)
	}
	if v, ok := envValue(env, "OPENCODE_BIN"); ok {
		t.Fatalf("OPENCODE_BIN = %q, want it unset (no shims on PATH)", v)
	}

	t.Setenv("FAKE_MISE_NO_BIN_PATHS", "1")
	ResetCapabilityCache()
	env = PinEnv(os.Environ(), bin)
	path = pathOf(env)
	if path[len(path)-1] != f.shims || indexOf(path, codexDir) >= 0 {
		t.Fatalf("fallback PATH = %v, want the shims dir last", path)
	}
	if v, _ := envValue(env, "CODEX_CLI_BIN"); v != filepath.Join(codexDir, "codex") {
		t.Fatalf("fallback CODEX_CLI_BIN = %q", v)
	}
	// opencode (a shim, but not installed) would reach the shim at the end
	// of PATH: it is pointed at a file that doesn't exist instead.
	if v, _ := envValue(env, "OPENCODE_BIN"); v != filepath.Join(f.home, ".monoagent", "unpinned", "opencode") {
		t.Fatalf("fallback OPENCODE_BIN = %q", v)
	}
	if _, err := os.Stat(filepath.Join(f.home, ".monoagent", "unpinned")); !os.IsNotExist(err) {
		t.Fatalf("the unpinned dir exists: %v", err)
	}
}

// Relative PATH entries resolve against the project root, and absolute
// ones inside it are the project's: neither reaches a command run there,
// nor does an agent CLI override naming a project file (it is pointed at
// the missing unpinned path).
func TestPinEnvInDropsProjectPaths(t *testing.T) {
	f := newShimFixture(t, "node/22.0.0")
	bin, err := Find()
	if err != nil {
		t.Fatal(err)
	}
	projBin := filepath.Join(f.project, "bin")
	sep := string(os.PathListSeparator)
	t.Setenv("PATH", strings.Join([]string{".", "node_modules/.bin", projBin, "/usr/bin", "/bin"}, sep))
	env := PinEnvIn(append(os.Environ(), "CODEX_CLI_BIN="+filepath.Join(f.project, ".cache", "n", "bin", "codex")), bin, f.project)
	for _, p := range pathOf(env) {
		if !filepath.IsAbs(p) || strings.HasPrefix(p, f.project) {
			t.Fatalf("PATH kept %q: %v", p, pathOf(env))
		}
	}
	if v, _ := envValue(env, "CODEX_CLI_BIN"); v != filepath.Join(f.home, ".monoagent", "unpinned", "codex") {
		t.Fatalf("CODEX_CLI_BIN = %q, want the unpinned path", v)
	}
}

// In the fallback (shims last on PATH), an agent CLI whose pin lands in
// the project — here through a direnv-style <project>/bin ahead of the
// shims — is pointed at the missing unpinned path, not unset: unset,
// monomind would start it by name through the shims.
func TestPinEnvInFallbackProjectPathAhead(t *testing.T) {
	f := newShimFixture(t, "node/22.0.0")
	codex := filepath.Join(f.data, "installs", "npm-openai-codex", "1.0.0", "bin", "codex")
	planted := filepath.Join(f.project, "bin", "codex")
	for path, who := range map[string]string{codex: "codex", planted: "PLANTED"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("#!/bin/sh\necho \""+who+" $0\" >>\"$PIN_LOG\"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sep := string(os.PathListSeparator)
	t.Setenv("PATH", strings.Join([]string{filepath.Join(f.project, "bin"), f.shims, "/usr/bin", "/bin"}, sep))
	t.Setenv("FAKE_MISE_NO_BIN_PATHS", "1")
	t.Setenv("FAKE_RUNTIMES", "codex")
	ResetCapabilityCache()
	if _, err := OrgStatus(context.Background(), f.project, "growth"); err != nil {
		t.Fatal(err)
	}
	if log := f.readLog(t); strings.Contains(log, "PLANTED") {
		t.Fatalf("a planted binary ran:\n%s", log)
	}
	bin, err := Find()
	if err != nil {
		t.Fatal(err)
	}
	env := PinEnvIn(os.Environ(), bin, f.project)
	if v, _ := envValue(env, "CODEX_CLI_BIN"); v != filepath.Join(f.home, ".monoagent", "unpinned", "codex") {
		t.Fatalf("CODEX_CLI_BIN = %q, want the unpinned path", v)
	}
	if path := pathOf(env); path[len(path)-1] != f.shims || indexOf(path, filepath.Join(f.project, "bin")) >= 0 {
		t.Fatalf("PATH = %v", path)
	}
}

// `mise bin-paths` runs from home with the project-scoped settings
// scrubbed: neither the project's .tool-versions (this process's cwd is
// the project) nor MISE_NODE_VERSION pointing into it adds a dir, though
// the same mise run in the project lists the planted one.
func TestMiseBinPathsIgnoreProjectSteering(t *testing.T) {
	f := newShimFixture(t, "node/22.0.0")
	steer := exec.Command(filepath.Join(filepath.Dir(mustEval(t, filepath.Join(f.shims, "node"))), "mise"), "bin-paths")
	steer.Dir = f.project
	if out, err := steer.Output(); err != nil || !strings.Contains(string(out), f.project) || !strings.Contains(string(out), ".cache/n/bin") {
		t.Fatalf("fixture: mise bin-paths in the project = %q, %v; want it steered into the project", out, err)
	}
	t.Chdir(f.project)
	t.Setenv("MISE_NODE_VERSION", "path:"+filepath.Join(f.project, ".cache", "n"))
	ResetCapabilityCache()
	// The raw listing is unsteered (not only the installs filter after it).
	raw, err := runManager(managerMise, shimIn(f.shims), "bin-paths")
	if err != nil || strings.Contains(raw, f.project) {
		t.Fatalf("mise bin-paths = %q, %v; want no project dir", raw, err)
	}
	dirs, err := managerBinPaths(managerMise, f.shims)
	if err != nil {
		t.Fatal(err)
	}
	if indexOf(dirs, f.node) < 0 {
		t.Fatalf("bin-paths = %v, want %s", dirs, f.node)
	}
	for _, d := range dirs {
		if strings.HasPrefix(d, f.project) {
			t.Fatalf("bin-paths = %v: %s is the project's", dirs, d)
		}
	}
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestShimManagerDetection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix paths")
	}
	home := t.TempDir()
	custom := filepath.Join(t.TempDir(), "asdf-data")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("ASDF_DATA_DIR", custom)
	t.Setenv("MISE_DATA_DIR", "")
	t.Setenv("VOLTA_HOME", "")
	t.Setenv("NODENV_ROOT", "")
	t.Setenv("PROTO_HOME", "")
	write := func(path, body string) string {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	other := t.TempDir()
	for path, want := range map[string]string{
		write(filepath.Join(other, "a", "monomind"), "#!/usr/bin/env bash\n# asdf-plugin: nodejs 22.0.0\nexec asdf exec \"monomind\" \"$@\"\n"): managerAsdf,
		write(filepath.Join(other, "m", "monomind"), "#!/bin/sh\nexec mise x -- monomind \"$@\"\n"):                                             managerMise,
		write(filepath.Join(custom, "shims", "monomind"), "#!/bin/sh\n"):                                                                        managerAsdf,
		write(filepath.Join(home, ".local", "share", "mise", "shims", "monomind"), "#!/bin/sh\n"):                                               managerMise,
		write(filepath.Join(home, ".volta", "bin", "monomind"), "#!/bin/sh\n"):                                                                  managerVolta,
		write(filepath.Join(other, "n", "monomind"), "#!/usr/bin/env bash\nset -e\nexec \"/usr/local/bin/nodenv\" exec \"$program\" \"$@\"\n"):  managerNodenv,
		write(filepath.Join(home, ".nodenv", "shims", "monomind"), "#!/bin/sh\n"):                                                               managerNodenv,
		write(filepath.Join(home, ".proto", "shims", "monomind"), "#!/bin/sh\n"):                                                                managerProto,
		write(filepath.Join(other, "plain", "monomind"), "#!/usr/bin/env node\nconsole.log(1)\n"):                                               "",
	} {
		if got := shimManager(path); got != want {
			t.Errorf("shimManager(%s) = %q, want %q", path, got, want)
		}
	}
}

// proto has no `which` for an npm global's bins: its shim fails closed.
func TestProtoShimFailsClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix paths")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("PROTO_HOME", "")
	shim := filepath.Join(home, ".proto", "shims", "monomind")
	if err := os.MkdirAll(filepath.Dir(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shim, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvOverride, shim)
	ResetCapabilityCache()
	t.Cleanup(ResetCapabilityCache)
	if _, err := Find(); err == nil || !strings.Contains(err.Error(), "proto shim") {
		t.Fatalf("Find() err = %v, want a proto refusal", err)
	}
}

// asdf's global tools come from `asdf current` (0.14's bash output, and
// 0.16's Go output with its header and Installed column) and `asdf where
// <tool>`; a tool that is set but not installed (where fails) is skipped.
func TestAsdfBinPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake asdf is a shell script")
	}
	for format, current := range map[string]string{
		"0.14": "nodejs          22.0.0          /home/u/.tool-versions\n" +
			"python          3.12.0          /home/u/.tool-versions\n" +
			"ruby            3.3.0           Not installed. Run \"asdf install ruby 3.3.0\"\n" +
			"golang          ______          No version is set. Run \"asdf <global|shell|local> golang <version>\"\n",
		"0.16": "Name            Version         Source                   Installed\n" +
			"nodejs          22.0.0          /home/u/.tool-versions   true\n" +
			"python          3.12.0          /home/u/.tool-versions   true\n" +
			"ruby            3.3.0           /home/u/.tool-versions   false\n",
	} {
		t.Run(format, func(t *testing.T) {
			data := t.TempDir()
			t.Setenv("HOME", t.TempDir())
			t.Setenv("USERPROFILE", os.Getenv("HOME"))
			t.Setenv("ASDF_DATA_DIR", data)
			t.Setenv("PATH", "/usr/bin"+string(os.PathListSeparator)+"/bin")
			want := []string{}
			for _, tv := range []string{"nodejs/22.0.0", "python/3.12.0"} {
				d := filepath.Join(data, "installs", filepath.FromSlash(tv), "bin")
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
				want = append(want, d)
			}
			script := "#!/bin/sh\ncase \"$1\" in\n" +
				"current) cat <<'OUT'\n" + current + "OUT\n;;\n" +
				"where) for d in \"$ASDF_DATA_DIR\"/installs/$2/*; do [ -d \"$d\" ] && { echo \"$d\"; exit 0; }; done\n" +
				"echo \"Version not installed\" >&2; exit 1 ;;\n*) exit 2 ;;\nesac\n"
			if err := os.MkdirAll(filepath.Join(data, "bin"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(data, "bin", "asdf"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			got, err := listBinPaths(managerAsdf, "")
			if err != nil || strings.Join(got, " ") != strings.Join(want, " ") {
				t.Fatalf("listBinPaths = %v, %v; want %v", got, err, want)
			}
		})
	}
}

func TestScrubManagerEnv(t *testing.T) {
	in := []string{"PATH=/bin", "MISE_DATA_DIR=/d", "MISE_NODE_VERSION=path:/p", "__MISE_DIFF=x", "ASDF_NODEJS_VERSION=1", "ASDF_DATA_DIR=/a", "MISE_OVERRIDE_CONFIG_FILENAMES=x.toml", "VOLTA_HOME=/v"}
	got := strings.Join(scrubManagerEnv(in), " ")
	if want := "PATH=/bin MISE_DATA_DIR=/d ASDF_DATA_DIR=/a VOLTA_HOME=/v"; got != want {
		t.Fatalf("scrubManagerEnv = %q, want %q", got, want)
	}
}
