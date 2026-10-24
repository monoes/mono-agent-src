//go:build linux

package agentbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func needBwrap(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bwrap not installed")
	}
	if out, err := exec.Command("bwrap", "--bind", "/", "/", "true").CombinedOutput(); err != nil {
		t.Skipf("bwrap cannot create namespaces here: %s", out)
	}
}

// rig is a scratch HOME with a fake state directory, a project and a fake tool.
type rig struct {
	home, proj, state, tool string
}

func newRig(t *testing.T) rig {
	t.Helper()
	base := t.TempDir()
	r := rig{home: filepath.Join(base, "home"), proj: filepath.Join(base, "proj")}
	r.state = filepath.Join(r.home, ".monoagent")
	toolDir := filepath.Join(r.home, "tools", "bin")
	for _, d := range []string{r.state, r.proj, toolDir, filepath.Join(r.home, ".config", "autostart")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, c string, mode os.FileMode) {
		if err := os.WriteFile(p, []byte(c), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(r.state, "monoagent.db"), "secret", 0o600)
	write(filepath.Join(r.home, ".bashrc"), "orig", 0o600)
	r.tool = filepath.Join(toolDir, "faketool")
	write(r.tool, "#!/bin/sh\necho tool-ran\n", 0o755)
	return r
}

func (r rig) box(mode Mode) Box {
	return Box{Mode: mode, Cwd: r.proj, Home: r.home, StateDir: r.state, Binaries: []string{r.tool}}
}

func (r rig) env() []string {
	return []string{"PATH=" + filepath.Dir(r.tool) + ":" + os.Getenv("PATH"), "HOME=" + r.home, "SSH_AUTH_SOCK=/tmp/agent.sock", "MONOAGENT_X=1", "AWS_SECRET_ACCESS_KEY=zzz"}
}

func runPlan(t *testing.T, p Plan, env []string) (string, error) {
	t.Helper()
	out, err := exec.Command(p.Argv[0], p.Argv[1:]...).CombinedOutput()
	return string(out), err
}

func TestStrictArgvStructure(t *testing.T) {
	needBwrap(t)
	r := newRig(t)
	p, err := r.box(Strict).Wrap("monomind", []string{"agent", "exec"}, r.env())
	if err != nil {
		t.Fatal(err)
	}
	j := strings.Join(p.Argv, " ")
	for _, w := range []string{"--ro-bind / /", "--tmpfs " + r.home, "--tmpfs /tmp", "--tmpfs /run", "--unshare-pid", "--unshare-ipc",
		"--new-session", "--cap-drop ALL", "--clearenv", "--bind " + r.proj + " " + r.proj, "--ro-bind " + r.tool + " " + r.tool,
		"--setenv MONOAGENT_X 1", "--die-with-parent -- monomind agent exec"} {
		if !strings.Contains(j, w) {
			t.Errorf("argv lacks %q:\n%s", w, j)
		}
	}
	for _, bad := range []string{"SSH_AUTH_SOCK", "AWS_SECRET", "--bind / /"} {
		if strings.Contains(j, bad) {
			t.Errorf("argv contains %q", bad)
		}
	}
}

func TestStrictRefusesForbiddenPaths(t *testing.T) {
	needBwrap(t)
	r := newRig(t)
	for _, p := range []string{"~/.ssh", "~/.monoagent", "~/.config", "~/", "/", "~/.local/bin", "relative"} {
		b := r.box(Strict)
		b.AllowRW = []string{p}
		if _, err := b.Wrap("true", nil, r.env()); !IsCode(err, CodeBadPath) {
			t.Errorf("--allow %s: %v", p, err)
		}
	}
	for _, cwd := range []string{r.home, r.state, filepath.Join(r.home, ".config")} {
		b := r.box(Strict)
		b.Cwd = cwd
		if _, err := b.Wrap("true", nil, r.env()); !IsCode(err, CodeBadPath) {
			t.Errorf("cwd %s: %v", cwd, err)
		}
	}
}

func TestStrictRealRun(t *testing.T) {
	needBwrap(t)
	r := newRig(t)
	if err := os.MkdirAll(filepath.Join(r.home, "cache"), 0o700); err != nil {
		t.Fatal(err)
	}
	b := r.box(Strict)
	b.AllowRW = []string{"~/cache"}
	probe := `echo "db=$(cat "$HOME/.monoagent/monoagent.db" 2>/dev/null || echo none)"
echo "tool=$(faketool)"
touch "$PWD/made" && echo cwd=ok
touch "$HOME/cache/made" && echo allow=ok
echo planted > "$HOME/.bashrc" && echo bashrc=tmpfs
echo "sock=${SSH_AUTH_SOCK:-unset} aws=${AWS_SECRET_ACCESS_KEY:-unset} x=${MONOAGENT_X:-unset}"`
	p, err := b.Wrap("sh", []string{"-c", probe}, r.env())
	if err != nil {
		t.Fatal(err)
	}
	out, err := runPlan(t, p, nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, w := range []string{"db=none", "tool=tool-ran", "cwd=ok", "allow=ok", "bashrc=tmpfs", "sock=unset aws=unset x=1"} {
		if !strings.Contains(out, w) {
			t.Errorf("want %q in:\n%s", w, out)
		}
	}
	if c, _ := os.ReadFile(filepath.Join(r.home, ".bashrc")); string(c) != "orig" {
		t.Errorf("host .bashrc changed: %q", c)
	}
	for _, f := range []string{filepath.Join(r.proj, "made"), filepath.Join(r.home, "cache", "made")} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("%s not written on the host", f)
		}
	}
}

func TestCompatRealRun(t *testing.T) {
	needBwrap(t)
	r := newRig(t)
	p, err := r.box(Compat).Wrap("sh", []string{"-c", `echo "db=$(cat "$HOME/.monoagent/monoagent.db" 2>/dev/null || echo none)"; (echo x >> "$HOME/.bashrc") 2>/dev/null && echo bashrc=writable || echo bashrc=readonly`}, r.env())
	if err != nil {
		t.Fatal(err)
	}
	out, err := runPlan(t, p, nil)
	if err != nil || !strings.Contains(out, "db=none") || !strings.Contains(out, "bashrc=readonly") {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestCanaryPassesInBothModes(t *testing.T) {
	needBwrap(t)
	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent.sock") // the canary must see the filter work
	r := newRig(t)
	for _, m := range []Mode{Strict, Compat} {
		if err := r.box(m).Canary(context.Background()); err != nil {
			t.Errorf("%s canary: %v", m, err)
		}
	}
}

// A mount plan that exposes the state directory is refused by the canary.
func TestCanaryRefusesABrokenBox(t *testing.T) {
	needBwrap(t)
	r := newRig(t)
	plan, marker, err := r.box(Strict).canaryPlan()
	if err != nil {
		t.Fatal(err)
	}
	if err := runCanary(context.Background(), plan, marker); err != nil {
		t.Fatalf("intact box: %v", err)
	}
	i := indexOf(plan.Argv, "--die-with-parent")
	broken := Plan{Argv: append(append(append([]string{}, plan.Argv[:i]...), "--ro-bind", r.state, r.state), plan.Argv[i:]...)}
	err = runCanary(context.Background(), broken, marker)
	if !IsCode(err, CodeCanary) || !strings.Contains(err.Error(), "state directory") {
		t.Fatalf("canary did not refuse: %v", err)
	}
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

// Only the selected runtime's login is bound, read-only; its settings directory is a
// disposable tmpfs; another runtime's directory is not bound at all.
func TestStrictBindsOnlyTheSelectedRuntimesAuthReadOnly(t *testing.T) {
	needBwrap(t)
	r := newRig(t)
	for f, c := range map[string]string{".claude/.credentials.json": "claude-login", ".claude/settings.json": "hooks", ".codex/auth.json": "codex-login"} {
		p := filepath.Join(r.home, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	b := r.box(Strict)
	b.Runtime = "claude"
	probe := `echo "login=$(cat "$HOME/.claude/.credentials.json" 2>/dev/null || echo none)"
echo "other=$(cat "$HOME/.codex/auth.json" 2>/dev/null || echo none)"
echo "oldsettings=$(cat "$HOME/.claude/settings.json" 2>/dev/null || echo none)"
(echo x >> "$HOME/.claude/.credentials.json") 2>/dev/null && echo credwrite=ok || echo credwrite=denied
echo '{"hooks":1}' > "$HOME/.claude/settings.json" && echo settingswrite=tmpfs`
	p, err := b.Wrap("sh", []string{"-c", probe}, r.env())
	if err != nil {
		t.Fatal(err)
	}
	out, err := runPlan(t, p, nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, w := range []string{"login=claude-login", "other=none", "oldsettings=none", "credwrite=denied", "settingswrite=tmpfs"} {
		if !strings.Contains(out, w) {
			t.Errorf("want %q in:\n%s", w, out)
		}
	}
	if c, _ := os.ReadFile(filepath.Join(r.home, ".claude/settings.json")); string(c) != "hooks" {
		t.Errorf("host settings changed: %q", c)
	}
	if c, _ := os.ReadFile(filepath.Join(r.home, ".claude/.credentials.json")); string(c) != "claude-login" {
		t.Errorf("host credentials changed: %q", c)
	}
}

// A forbidden directory that is a symlink to somewhere else: the target is masked, refused
// as a working directory or allowed path, and the canary checks it.
func TestRelocatedForbiddenDirectoriesAreMasked(t *testing.T) {
	needBwrap(t)
	r := newRig(t)
	elsewhere := filepath.Join(filepath.Dir(r.home), "elsewhere")
	realState := filepath.Join(elsewhere, "state")
	realSSH := filepath.Join(elsewhere, "ssh")
	for _, d := range []string{realState, realSSH} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(realState, "monoagent.db"), []byte("secret"), 0o600)
	os.WriteFile(filepath.Join(realSSH, "id_x"), []byte("key"), 0o600)
	os.RemoveAll(r.state)
	if err := os.Symlink(realState, r.state); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realSSH, filepath.Join(r.home, ".ssh")); err != nil {
		t.Fatal(err)
	}
	probe := `echo "db=$(cat '` + realState + `/monoagent.db' 2>/dev/null || echo none)"; echo "key=$(cat '` + realSSH + `/id_x' 2>/dev/null || echo none)"`
	for _, m := range []Mode{Strict, Compat} {
		b := r.box(m)
		p, err := b.Wrap("sh", []string{"-c", probe}, r.env())
		if err != nil {
			t.Fatal(err)
		}
		out, err := runPlan(t, p, nil)
		if err != nil || !strings.Contains(out, "db=none") || !strings.Contains(out, "key=none") {
			t.Errorf("%s: relocated targets readable: %v\n%s", m, err, out)
		}
		if err := b.Canary(context.Background()); err != nil {
			t.Errorf("%s canary: %v", m, err)
		}
	}
	for _, bad := range []string{realState, realSSH, elsewhere} {
		b := r.box(Strict)
		b.Cwd = bad
		if _, err := b.Wrap("true", nil, r.env()); !IsCode(err, CodeBadPath) {
			t.Errorf("cwd %s: %v", bad, err)
		}
		b = r.box(Strict)
		b.AllowRO = []string{bad}
		if _, err := b.Wrap("true", nil, r.env()); !IsCode(err, CodeBadPath) {
			t.Errorf("allow %s: %v", bad, err)
		}
	}
}

// Attachments must be canonical regular files outside the protected locations, and the
// canary probes the plan that includes them.
func TestAttachmentsAreCheckedAndProbed(t *testing.T) {
	needBwrap(t)
	r := newRig(t)
	good := filepath.Join(filepath.Dir(r.home), "notes.txt")
	os.WriteFile(good, []byte("notes"), 0o600)
	link := filepath.Join(filepath.Dir(r.home), "link.txt")
	os.Symlink(filepath.Join(r.state, "monoagent.db"), link)
	for name, att := range map[string]string{
		"state directory": r.state, "file in state": filepath.Join(r.state, "monoagent.db"),
		"symlink to state": link, "directory": r.proj, "missing": filepath.Join(r.proj, "nope"), "relative": "notes.txt",
	} {
		b := r.box(Strict)
		b.Attach = []string{att}
		if _, err := b.Wrap("true", nil, r.env()); !IsCode(err, CodeBadPath) {
			t.Errorf("attachment %s (%s) not refused: %v", name, att, err)
		}
	}
	b := r.box(Strict)
	b.Attach = []string{good}
	p, err := b.Wrap("sh", []string{"-c", `cat "` + good + `"`}, r.env())
	if err != nil {
		t.Fatal(err)
	}
	if out, err := runPlan(t, p, nil); err != nil || !strings.Contains(out, "notes") {
		t.Fatalf("good attachment: %v %s", err, out)
	}
	if err := b.Canary(context.Background()); err != nil {
		t.Fatalf("canary with an attachment: %v", err)
	}
	cp, _, err := b.canaryPlan()
	if err != nil || !strings.Contains(strings.Join(cp.Argv, " "), "--ro-bind "+good+" "+good) {
		t.Errorf("the canary plan lacks the attachment bind: %v", err)
	}
}

func TestLauncherMustBeTrusted(t *testing.T) {
	needBwrap(t)
	real, err := findBwrap()
	if err != nil {
		t.Fatalf("the system bwrap is not trusted: %v", err)
	}
	data, _ := os.ReadFile(real)
	dir := filepath.Join(t.TempDir(), "bin")
	os.MkdirAll(dir, 0o755)
	copyPath := filepath.Join(dir, "bwrap")
	os.WriteFile(copyPath, data, 0o755)
	if err := trustedLauncher(copyPath); err != nil {
		t.Skipf("scratch ancestors are not private on this machine: %v", err)
	}
	// Replaceable by others: refused.
	os.Chmod(dir, 0o777)
	if err := trustedLauncher(copyPath); !IsCode(err, CodeUntrusted) {
		t.Errorf("a launcher in a world-writable directory: %v", err)
	}
	os.Chmod(dir, 0o755)
	os.Chmod(copyPath, 0o775)
	if err := trustedLauncher(copyPath); !IsCode(err, CodeUntrusted) {
		t.Errorf("a group-writable launcher: %v", err)
	}
	// Wrap and the canary refuse it, and a pinned box uses one path for both.
	r := newRig(t)
	b := r.box(Strict)
	b.Bwrap = copyPath
	if _, err := b.Wrap("true", nil, r.env()); !IsCode(err, CodeUntrusted) {
		t.Errorf("Wrap with an untrusted launcher: %v", err)
	}
	if err := b.Canary(context.Background()); !IsCode(err, CodeUntrusted) {
		t.Errorf("Canary with an untrusted launcher: %v", err)
	}
	pinned, err := r.box(Strict).Pin()
	if err != nil || pinned.Bwrap != real {
		t.Fatalf("Pin: %q %v", pinned.Bwrap, err)
	}
	p, _ := pinned.Wrap("true", nil, r.env())
	cp, _, _ := pinned.canaryPlan()
	if p.Argv[0] != real || cp.Argv[0] != real {
		t.Errorf("turn %q and canary %q do not use the pinned launcher %q", p.Argv[0], cp.Argv[0], real)
	}
	// A hostile PATH entry is never consulted while a system launcher exists.
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	if got, err := findBwrap(); err != nil || got != real {
		t.Errorf("findBwrap with a planted PATH entry: %q %v", got, err)
	}
}

// Only the binary (and a clean package root) is bound, never what lies beside it.
func TestBinaryBindsDoNotExposeNeighbours(t *testing.T) {
	needBwrap(t)
	r := newRig(t)
	os.WriteFile(filepath.Join(r.home, "tools", "secret.txt"), []byte("neighbour"), 0o600)
	os.WriteFile(filepath.Join(r.home, "tools", "bin", "other"), []byte("other"), 0o755)
	pkg := func(name string, extra ...string) string {
		root := filepath.Join(r.home, "pkgs", name)
		os.MkdirAll(filepath.Join(root, "bin"), 0o755)
		os.WriteFile(filepath.Join(root, "package.json"), []byte("{}"), 0o644)
		os.WriteFile(filepath.Join(root, "lib.txt"), []byte("lib"), 0o644)
		for _, e := range extra {
			os.WriteFile(filepath.Join(root, e), []byte("x"), 0o600)
		}
		cli := filepath.Join(root, "bin", "cli")
		os.WriteFile(cli, []byte("#!/bin/sh\ncat \"$(dirname \"$(readlink -f \"$0\")\")/../lib.txt\"\n"), 0o755)
		link := filepath.Join(r.home, "tools", "bin", name)
		os.Symlink(cli, link)
		return link
	}
	clean, dirty := pkg("clean"), pkg("dirty", ".env")
	b := r.box(Strict)
	b.Binaries = []string{r.tool, clean, dirty}
	probe := `echo "neighbour=$(cat "` + filepath.Join(r.home, "tools", "secret.txt") + `" 2>/dev/null || echo none)"
echo "otherbin=$(cat "` + filepath.Join(r.home, "tools", "bin", "other") + `" 2>/dev/null || echo none)"
echo "clean=$(clean 2>/dev/null || echo failed)"
echo "dirty=$(dirty 2>/dev/null || echo failed)"`
	p, err := b.Wrap("sh", []string{"-c", probe}, r.env())
	if err != nil {
		t.Fatal(err)
	}
	// PATH for the lookup includes the tools dir (r.env()).
	out, err := runPlan(t, p, nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, w := range []string{"neighbour=none", "otherbin=none", "clean=lib", "dirty=failed"} {
		if !strings.Contains(out, w) {
			t.Errorf("want %q in:\n%s", w, out)
		}
	}
	if !strings.Contains(strings.Join(p.Notes, "|"), "pkgs/dirty") {
		t.Errorf("no warning for the skipped package directory: %v", p.Notes)
	}
}
