//go:build linux

package agentbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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
	return Box{Mode: mode, Cwd: r.proj, Home: r.home, StateDir: r.state, Binaries: []string{r.tool}, PassEnv: []string{"MONOAGENT_X"}}
}

func (r rig) env() []string {
	return []string{"PATH=" + filepath.Dir(r.tool) + ":" + os.Getenv("PATH"), "HOME=" + r.home, "SSH_AUTH_SOCK=/tmp/agent.sock", "MONOAGENT_X=1", "AWS_SECRET_ACCESS_KEY=zzz",
		"MONOAGENT_GITHUB_CLIENT_SECRET=oauth-secret", "MONOAGENT_AITASK_TOKEN=ambient", "MONOAGENT_API_URL=http://127.0.0.1:9322", "MONOAGENT_API_TOKEN=bearer", "MONOAGENT_PROFILE=work"}
}

func runPlan(t *testing.T, p Plan, env []string) (string, error) {
	t.Helper()
	out, err := p.Command().CombinedOutput()
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
		"--new-session", "--cap-drop ALL", "--clearenv", "--bind-fd 3 " + r.proj, "--ro-bind-fd 4 " + r.tool,
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
	broken := Plan{Files: plan.Files, Argv: append(append(append([]string{}, plan.Argv[:i]...), "--ro-bind", r.state, r.state), plan.Argv[i:]...)}
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
	if err != nil || !strings.Contains(strings.Join(cp.Argv, " "), "--ro-bind-fd") && strings.Contains(strings.Join(cp.Argv, " "), good) {
		t.Errorf("the canary plan lacks the attachment bind: %v", err)
	}
}

func TestLauncherMustBeRootOwnedAndNeverComesFromPATH(t *testing.T) {
	needBwrap(t)
	real, err := findBwrap()
	if err != nil {
		t.Fatalf("the system bwrap is not trusted: %v", err)
	}
	data, _ := os.ReadFile(real)
	// A copy the user owns, in a private directory with ordinary permissions, is not trusted.
	dir := filepath.Join(t.TempDir(), "bin")
	os.MkdirAll(dir, 0o755)
	copyPath := filepath.Join(dir, "bwrap")
	os.WriteFile(copyPath, data, 0o755)
	if err := trustedLauncher(copyPath); !IsCode(err, CodeUntrusted) {
		t.Errorf("a user-owned launcher: %v", err)
	}
	r := newRig(t)
	b := r.box(Strict)
	b.Bwrap = copyPath
	if _, err := b.Wrap("true", nil, r.env()); !IsCode(err, CodeUntrusted) {
		t.Errorf("Wrap with a user-owned launcher: %v", err)
	}
	if err := b.Canary(context.Background()); !IsCode(err, CodeUntrusted) {
		t.Errorf("Canary with a user-owned launcher: %v", err)
	}
	// No system launcher: nothing on PATH is used instead.
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	old := launcherCandidates
	launcherCandidates = []string{"/nonexistent/bwrap"}
	_, err = findBwrap()
	launcherCandidates = old
	if !IsCode(err, CodeUnavailable) {
		t.Errorf("findBwrap with no system launcher: %v", err)
	}
	// The pinned launcher is the same for the turn and the canary.
	pinned, err := r.box(Strict).Pin()
	if err != nil || pinned.Bwrap != real {
		t.Fatalf("Pin: %q %v", pinned.Bwrap, err)
	}
	p, _ := pinned.Wrap("true", nil, r.env())
	cp, _, _ := pinned.canaryPlan()
	if p.Argv[0] != real || cp.Argv[0] != real {
		t.Errorf("turn %q and canary %q do not use the pinned launcher %q", p.Argv[0], cp.Argv[0], real)
	}
}

// Only the binary and a verified package's manifest entries are bound, never what lies beside
// them: not the install prefix, not the package's configuration or credential-like files.
func TestBinaryBindsDoNotExposeNeighbours(t *testing.T) {
	needBwrap(t)
	r := newRig(t)
	os.WriteFile(filepath.Join(r.home, "tools", "secret.txt"), []byte("neighbour"), 0o600)
	os.WriteFile(filepath.Join(r.home, "tools", "bin", "other"), []byte("other"), 0o755)
	pkg := func(name, pkgName string, extra ...string) string {
		root := filepath.Join(r.home, "pkgs", name)
		os.MkdirAll(filepath.Join(root, "bin"), 0o755)
		os.MkdirAll(filepath.Join(root, "lib"), 0o755)
		os.MkdirAll(filepath.Join(root, "config"), 0o755)
		os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"name":"`+pkgName+`"}`), 0o644)
		os.WriteFile(filepath.Join(root, "lib", "lib.txt"), []byte("lib"), 0o644)
		os.WriteFile(filepath.Join(root, "config", "auth.json"), []byte("package-secret"), 0o600)
		os.WriteFile(filepath.Join(root, ".env.production"), []byte("package-secret"), 0o600)
		for _, e := range extra {
			os.MkdirAll(filepath.Dir(filepath.Join(root, e)), 0o755)
			os.WriteFile(filepath.Join(root, e), []byte("x"), 0o600)
		}
		cli := filepath.Join(root, "bin", "cli")
		os.WriteFile(cli, []byte("#!/bin/sh\nd=$(dirname \"$(readlink -f \"$0\")\")/..\ncat \"$d/lib/lib.txt\"\ncat \"$d/config/auth.json\" \"$d/.env.production\" 2>/dev/null\n"), 0o755)
		link := filepath.Join(r.home, "tools", "bin", name)
		os.Symlink(cli, link)
		return link
	}
	clean := pkg("clean", "monomind")
	tainted := pkg("tainted", "monomind", "dist/credentials.json")
	stranger := pkg("stranger", "left-pad")
	b := r.box(Strict)
	b.Binaries = []string{r.tool, clean, tainted, stranger}
	probe := `echo "neighbour=$(cat "` + filepath.Join(r.home, "tools", "secret.txt") + `" 2>/dev/null || echo none)"
echo "otherbin=$(cat "` + filepath.Join(r.home, "tools", "bin", "other") + `" 2>/dev/null || echo none)"
echo "clean=$(clean 2>/dev/null || echo failed)"
echo "tainted=$(tainted 2>/dev/null || echo failed)"
echo "stranger=$(stranger 2>/dev/null || echo failed)"`
	p, err := b.Wrap("sh", []string{"-c", probe}, r.env())
	if err != nil {
		t.Fatal(err)
	}
	out, err := runPlan(t, p, nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	// The verified package runs and reads its manifest entry; its configuration and
	// credential-like files are not in the box.
	for _, w := range []string{"neighbour=none", "otherbin=none", "clean=lib", "tainted=failed", "stranger=failed"} {
		if !strings.Contains(out, w) {
			t.Errorf("want %q in:\n%s", w, out)
		}
	}
	if strings.Contains(out, "package-secret") {
		t.Errorf("a package's credential-like file reached the box:\n%s", out)
	}
	notes := strings.Join(p.Notes, "|")
	if !strings.Contains(notes, "pkgs/tainted") || !strings.Contains(notes, "pkgs/stranger") {
		t.Errorf("no warning for the skipped packages: %v", p.Notes)
	}
}

func TestOnlyAllowlistedMonoagentVariablesPass(t *testing.T) {
	needBwrap(t)
	r := newRig(t)
	p, err := r.box(Strict).Wrap("sh", []string{"-c", `echo "secret=${MONOAGENT_GITHUB_CLIENT_SECRET:-unset} task=${MONOAGENT_AITASK_TOKEN:-unset} api=${MONOAGENT_API_URL:-unset}${MONOAGENT_API_TOKEN:-}${MONOAGENT_PROFILE:-} turn=${MONOAGENT_X:-unset}"`}, r.env())
	if err != nil {
		t.Fatal(err)
	}
	out, err := runPlan(t, p, nil)
	if err != nil || !strings.Contains(out, "secret=unset task=unset api=unset turn=1") {
		t.Fatalf("%v\n%s", err, out)
	}
}

// The box gets its own /dev: the host's /dev/shm is not shared.
func TestDevIsPrivate(t *testing.T) {
	needBwrap(t)
	r := newRig(t)
	marker := filepath.Join("/dev/shm", "agentbox-test-"+filepath.Base(r.home))
	for _, m := range []Mode{Strict, Compat} {
		p, err := r.box(m).Wrap("sh", []string{"-c", `echo x > ` + marker + `-inside; echo null=$(echo ok > /dev/null && echo ok); stat -c %i /dev/shm`}, r.env())
		if err != nil {
			t.Fatal(err)
		}
		out, err := runPlan(t, p, nil)
		if err != nil || !strings.Contains(out, "null=ok") {
			t.Fatalf("%s: %v\n%s", m, err, out)
		}
		if _, err := os.Stat(marker + "-inside"); err == nil {
			os.Remove(marker + "-inside")
			t.Errorf("%s: a write to /dev/shm reached the host", m)
		}
		host, _ := os.Stat("/dev/shm")
		hostIno := host.Sys().(*syscall.Stat_t).Ino
		if strings.Contains(out, fmt.Sprint(hostIno)+"\n") {
			t.Errorf("%s: /dev/shm is the host's (inode %d)", m, hostIno)
		}
	}
}

// An API token the caller issues for this turn (PassEnv) does pass; the ambient one never does.
func TestIssuedAPITokenPassesOnlyWhenNamed(t *testing.T) {
	needBwrap(t)
	r := newRig(t)
	b := r.box(Strict)
	b.PassEnv = []string{"MONOAGENT_API_TOKEN"}
	p, err := b.Wrap("sh", []string{"-c", `echo "tok=${MONOAGENT_API_TOKEN:-unset} url=${MONOAGENT_API_URL:-unset}"`}, r.env())
	if err != nil {
		t.Fatal(err)
	}
	if out, err := runPlan(t, p, nil); err != nil || !strings.Contains(out, "tok=bearer url=unset") {
		t.Fatalf("%v\n%s", err, out)
	}
}

// The launcher is not started with the caller's environment: bubblewrap's init keeps it
// readable in /proc/1/environ.
func TestLauncherDoesNotInheritTheAmbientEnvironment(t *testing.T) {
	needBwrap(t)
	t.Setenv("MONOAGENT_AMBIENT_MARKER", "leak-me")
	r := newRig(t)
	for _, m := range []Mode{Strict, Compat} {
		p, err := r.box(m).Wrap("sh", []string{"-c", `tr '\0' '\n' < /proc/1/environ 2>&1; echo done`}, os.Environ())
		if err != nil {
			t.Fatal(err)
		}
		out, err := runPlan(t, p, nil)
		if err != nil || !strings.Contains(out, "done") {
			t.Fatalf("%s: %v\n%s", m, err, out)
		}
		if strings.Contains(out, "leak-me") {
			t.Errorf("%s: the ambient environment is readable in /proc/1/environ:\n%s", m, out)
		}
	}
}

// No runtime's configuration can be brought back as a working directory, an allowed path
// or an attachment, the selected runtime's included.
func TestRuntimeDirectoriesAreProtected(t *testing.T) {
	needBwrap(t)
	r := newRig(t)
	for _, d := range []string{".codex", ".claude", ".gemini"} {
		os.MkdirAll(filepath.Join(r.home, d), 0o700)
		os.WriteFile(filepath.Join(r.home, d, "auth.json"), []byte("login"), 0o600)
	}
	b := r.box(Strict)
	b.Runtime = "claude"
	b.Cwd = filepath.Join(r.home, ".codex")
	if _, err := b.Wrap("true", nil, r.env()); !IsCode(err, CodeBadPath) {
		t.Errorf("cwd ~/.codex: %v", err)
	}
	for _, p := range []string{"~/.claude", "~/.codex", "~/.gemini/auth.json"} {
		b = r.box(Strict)
		b.Runtime = "claude"
		b.AllowRW = []string{p}
		if _, err := b.Wrap("true", nil, r.env()); !IsCode(err, CodeBadPath) {
			t.Errorf("allow %s: %v", p, err)
		}
	}
	b = r.box(Strict)
	b.Runtime = "claude"
	b.Attach = []string{filepath.Join(r.home, ".codex", "auth.json")}
	if _, err := b.Wrap("true", nil, r.env()); !IsCode(err, CodeBadPath) {
		t.Errorf("attach ~/.codex/auth.json: %v", err)
	}
}

// A state directory or database outside the home directory is masked wherever it is, with
// no symlink involved, and the canary checks it.
func TestExternalStateAndDatabaseAreMasked(t *testing.T) {
	needBwrap(t)
	r := newRig(t)
	ext := filepath.Join(filepath.Dir(r.home), "srv")
	os.MkdirAll(filepath.Join(ext, "state"), 0o700)
	os.WriteFile(filepath.Join(ext, "state", "monoagent.db"), []byte("secret"), 0o600)
	db := filepath.Join(ext, "agent.db")
	os.WriteFile(db, []byte("database"), 0o600)
	os.WriteFile(db+"-wal", []byte("wal"), 0o600)
	probe := `echo "state=$(cat '` + filepath.Join(ext, "state", "monoagent.db") + `' 2>/dev/null || echo none)"
echo "db=$(cat '` + db + `' 2>/dev/null || echo none)"
echo "wal=$(cat '` + db + `-wal' 2>/dev/null || echo none)"`
	for _, m := range []Mode{Strict, Compat} {
		b := r.box(m)
		b.StateDir = filepath.Join(ext, "state")
		b.Protect = []string{db, db + "-wal", db + "-shm"}
		p, err := b.Wrap("sh", []string{"-c", probe}, r.env())
		if err != nil {
			t.Fatal(err)
		}
		out, err := runPlan(t, p, nil)
		if err != nil || !strings.Contains(out, "state=none") || !strings.Contains(out, "db=none") || !strings.Contains(out, "wal=none") {
			t.Errorf("%s: protected files readable: %v\n%s", m, err, out)
		}
		if err := b.Canary(context.Background()); err != nil {
			t.Errorf("%s canary: %v", m, err)
		}
		b.Cwd = ext
		if _, err := b.Wrap("true", nil, r.env()); m == Strict && !IsCode(err, CodeBadPath) {
			t.Errorf("%s: cwd containing the database: %v", m, err)
		}
	}
}

// What was checked is what is bound: if a directory above a checked file is swapped for a
// symlink to somewhere else after the plan was made, the bind still carries the file that
// was checked, because it goes through a descriptor.
func TestBindsAreNotSwappableAfterTheCheck(t *testing.T) {
	needBwrap(t)
	r := newRig(t)
	dir := filepath.Dir(r.home)
	evil := filepath.Join(dir, "evil")
	os.MkdirAll(evil, 0o700)
	os.WriteFile(filepath.Join(evil, "att.txt"), []byte("secret"), 0o600)
	os.WriteFile(filepath.Join(evil, "allow.txt"), []byte("secret"), 0o600)
	att := filepath.Join(dir, "ad", "att.txt")
	allow := filepath.Join(dir, "ld", "allow.txt")
	for _, f := range []string{att, allow} {
		os.MkdirAll(filepath.Dir(f), 0o700)
		os.WriteFile(f, []byte("original"), 0o600)
	}
	b := r.box(Strict)
	b.Attach = []string{att}
	b.AllowRO = []string{allow}
	p, err := b.Wrap("sh", []string{"-c", `echo "att=$(cat '` + att + `')"; echo "allow=$(cat '` + allow + `')"`}, r.env())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	for _, d := range []string{filepath.Dir(att), filepath.Dir(allow)} {
		if err := os.Rename(d, d+".orig"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(evil, d); err != nil {
			t.Fatal(err)
		}
	}
	out, err := runPlan(t, p, nil)
	if err != nil || strings.Contains(out, "secret") || !strings.Contains(out, "att=original") || !strings.Contains(out, "allow=original") {
		t.Errorf("a swapped directory was followed: %v\n%s", err, out)
	}
}

// An authentication file must be a regular file whose real target is not protected; a
// symlink into a dotfiles repository is fine.
func TestAuthFilesAreValidated(t *testing.T) {
	needBwrap(t)
	run := func(setup func(r rig)) (Plan, error) {
		r := newRig(t)
		os.WriteFile(filepath.Join(r.home, ".ssh_key"), []byte("x"), 0o600)
		os.MkdirAll(filepath.Join(r.home, ".ssh"), 0o700)
		os.WriteFile(filepath.Join(r.home, ".ssh", "id_x"), []byte("sshkey"), 0o600)
		os.MkdirAll(filepath.Join(r.home, "dotfiles"), 0o700)
		os.WriteFile(filepath.Join(r.home, "dotfiles", "claude.json"), []byte("login"), 0o600)
		setup(r)
		b := r.box(Strict)
		b.Runtime = "claude"
		return b.Wrap("cat", []string{filepath.Join(r.home, ".claude.json")}, r.env())
	}
	if _, err := run(func(r rig) { os.Symlink(filepath.Join(r.home, ".ssh", "id_x"), filepath.Join(r.home, ".claude.json")) }); !IsCode(err, CodeBadPath) {
		t.Errorf("auth file linked to ~/.ssh: %v", err)
	}
	if _, err := run(func(r rig) { os.MkdirAll(filepath.Join(r.home, ".claude.json", "sub"), 0o700) }); !IsCode(err, CodeBadPath) {
		t.Errorf("auth file that is a directory: %v", err)
	}
	p, err := run(func(r rig) {
		os.Symlink(filepath.Join(r.home, "dotfiles", "claude.json"), filepath.Join(r.home, ".claude.json"))
	})
	if err != nil {
		t.Fatalf("a dotfiles symlink: %v", err)
	}
	if out, err := runPlan(t, p, nil); err != nil || !strings.Contains(out, "login") {
		t.Errorf("%v %s", err, out)
	}
}

// The selected runtime's login may be a link into a dotfiles repository, but never into
// another runtime's credentials.
func TestAuthFileCannotAliasAnotherRuntimesLogin(t *testing.T) {
	needBwrap(t)
	r := newRig(t)
	os.MkdirAll(filepath.Join(r.home, ".codex"), 0o700)
	os.WriteFile(filepath.Join(r.home, ".codex", "auth.json"), []byte("codex-login"), 0o600)
	os.Symlink(filepath.Join(r.home, ".codex", "auth.json"), filepath.Join(r.home, ".claude.json"))
	b := r.box(Strict)
	b.Runtime = "claude"
	if _, err := b.Wrap("true", nil, r.env()); !IsCode(err, CodeBadPath) {
		t.Fatalf("claude's login aliased to codex's credentials: %v", err)
	}
	// The selected runtime's own files are still its own.
	os.Remove(filepath.Join(r.home, ".claude.json"))
	os.MkdirAll(filepath.Join(r.home, ".claude"), 0o700)
	os.WriteFile(filepath.Join(r.home, ".claude", "real.json"), []byte("own"), 0o600)
	os.Symlink(filepath.Join(r.home, ".claude", "real.json"), filepath.Join(r.home, ".claude.json"))
	if _, err := b.Wrap("true", nil, r.env()); err != nil {
		t.Errorf("a runtime's login linked inside its own directory: %v", err)
	}
}

// The binary and package checks are made on what is pinned: a package directory swapped for a
// symlink after the plan was made does not change what is bound.
func TestPackageBindsAreNotSwappableAfterTheCheck(t *testing.T) {
	needBwrap(t)
	r := newRig(t)
	root := filepath.Join(r.home, "pkgs", "tool")
	os.MkdirAll(filepath.Join(root, "bin"), 0o755)
	os.MkdirAll(filepath.Join(root, "lib"), 0o755)
	os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"name":"monomind"}`), 0o644)
	os.WriteFile(filepath.Join(root, "lib", "lib.txt"), []byte("lib"), 0o644)
	cli := filepath.Join(root, "bin", "cli")
	os.WriteFile(cli, []byte("#!/bin/sh\ncat \"$(dirname \"$(readlink -f \"$0\")\")/../lib/lib.txt\"\n"), 0o755)
	link := filepath.Join(r.home, "tools", "bin", "tool")
	os.Symlink(cli, link)
	evil := filepath.Join(filepath.Dir(r.home), "evil")
	os.MkdirAll(filepath.Join(evil, "lib"), 0o755)
	os.MkdirAll(filepath.Join(evil, "bin"), 0o755)
	os.WriteFile(filepath.Join(evil, "lib", "lib.txt"), []byte("evil-lib"), 0o644)
	os.WriteFile(filepath.Join(evil, "package.json"), []byte(`{"name":"monomind"}`), 0o644)
	b := r.box(Strict)
	b.Binaries = []string{r.tool, link}
	p, err := b.Wrap("sh", []string{"-c", "tool"}, r.env())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := os.Rename(root, root+".orig"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(evil, root); err != nil {
		t.Fatal(err)
	}
	out, err := runPlan(t, p, nil)
	if err != nil || strings.Contains(out, "evil") || !strings.Contains(out, "lib") {
		t.Errorf("a swapped package directory was followed: %v\n%s", err, out)
	}
}

// The canary does not see the ambient value of a variable the turn passes explicitly, and
// does not look tools up on the ambient PATH.
func TestCanaryEnvironment(t *testing.T) {
	needBwrap(t)
	t.Setenv("MONOAGENT_API_TOKEN", "ambient-broad-token")
	r := newRig(t)
	b := r.box(Strict)
	b.PassEnv = []string{"MONOAGENT_API_TOKEN"}
	plan, _, err := b.canaryPlan()
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Close()
	j := strings.Join(plan.Argv, " ")
	if strings.Contains(j, "ambient-broad-token") {
		t.Error("the ambient token reached the canary")
	}
	if !strings.Contains(j, " -- /bin/sh ") {
		t.Errorf("the canary does not run an absolute shell")
	}
	if !strings.Contains(j, "--setenv PATH /usr/bin:/bin") {
		t.Error("the canary does not use a fixed PATH")
	}
}

// An executable found on PATH that resolves into a credential location must not be bound back
// into the hidden home: the plan is refused instead.
func TestRuntimeBinaryInsideForbiddenLocationIsRefused(t *testing.T) {
	needBwrap(t)
	r := newRig(t)
	secret := filepath.Join(r.home, ".ssh", "id_probe")
	if err := os.MkdirAll(filepath.Dir(secret), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("#!/bin/sh\necho fake-private-material\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(filepath.Dir(r.tool), "claude")); err != nil {
		t.Fatal(err)
	}
	b := r.box(Strict)
	b.Runtime = "claude"
	if p, err := b.Wrap("/bin/sh", []string{"-c", "true"}, r.env()); !IsCode(err, CodeBadPath) {
		p.Close()
		t.Fatalf("wrap: want %s, got %v", CodeBadPath, err)
	}
}

// install.sh puts a regular monoagentcli in ~/.local/bin when /usr/local/bin is not writable;
// that executable (bound read-only as one file) must still run in a strict box.
func TestRegularLocalBinExecutableRunsInStrictBox(t *testing.T) {
	needBwrap(t)
	r := newRig(t)
	cli := filepath.Join(r.home, ".local", "bin", "monoagentcli")
	if err := os.MkdirAll(filepath.Dir(cli), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cli, []byte("#!/bin/sh\necho cli-ran\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	b := r.box(Strict)
	b.Binaries = append(b.Binaries, cli)
	p, err := b.Wrap(cli, nil, r.env())
	if err != nil {
		t.Fatalf("a regular ~/.local/bin executable was refused: %v", err)
	}
	defer p.Close()
	out, err := runPlan(t, p, nil)
	if err != nil || !strings.Contains(out, "cli-ran") {
		t.Fatalf("%v %s", err, out)
	}
	// only the executable is bound, not the directory it sits in
	sibling := filepath.Join(r.home, ".local", "bin", "sibling")
	if err := os.WriteFile(sibling, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	p2, err := b.Wrap("/bin/sh", []string{"-c", `test ! -e "$HOME/.local/bin/sibling" && echo hidden`}, r.env())
	if err != nil {
		t.Fatal(err)
	}
	defer p2.Close()
	if out, err := runPlan(t, p2, nil); err != nil || !strings.Contains(out, "hidden") {
		t.Fatalf("sibling in ~/.local/bin reachable: %v %s", err, out)
	}
}

// The ~/.local/bin exception covers files directly in that directory only; an executable
// nested below it stays a forbidden target.
func TestLocalBinDescendantExecutableIsRefused(t *testing.T) {
	needBwrap(t)
	r := newRig(t)
	cli := filepath.Join(r.home, ".local", "bin", "private", "runner")
	if err := os.MkdirAll(filepath.Dir(cli), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cli, []byte("#!/bin/sh\necho nested\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	b := r.box(Strict)
	b.Binaries = append(b.Binaries, cli)
	plan, err := b.Wrap(cli, nil, r.env())
	if !IsCode(err, CodeBadPath) {
		plan.Close()
		t.Fatalf("a nested ~/.local/bin executable was accepted: %v", err)
	}
}
