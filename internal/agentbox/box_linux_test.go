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

// A box that exposes the state directory must be refused by the canary.
func TestCanaryRefusesABrokenBox(t *testing.T) {
	needBwrap(t)
	r := newRig(t)
	b := r.box(Strict)
	b.StateDir = r.proj // the project is bound read-write: this "state" is readable
	if err := os.WriteFile(filepath.Join(r.proj, "monoagent.db"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := b.Canary(context.Background())
	if !IsCode(err, CodeCanary) || !strings.Contains(err.Error(), "state directory") {
		t.Fatalf("canary did not refuse: %v", err)
	}
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
