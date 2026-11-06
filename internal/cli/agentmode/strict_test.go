package agentmode

import (
	"bytes"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeScript(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "strict.sh")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func needBwrap(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bwrap not installed")
	}
	if out, err := exec.Command("bwrap", "--bind", "/", "/", "true").CombinedOutput(); err != nil {
		t.Skipf("bwrap cannot create namespaces here: %s", out)
	}
}

func runScript(t *testing.T, script, home, dir string, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("sh", append([]string{writeScript(t, script)}, args...)...)
	cmd.Dir = dir
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}, env...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

func TestStrictProfileStructure(t *testing.T) {
	out, err := StrictBwrapProfile(StrictOptions{AllowRW: []string{"~/.cache/go-build"}, AllowRO: []string{"/opt/tc"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{`--tmpfs "$H"`, "--tmpfs /tmp", "--tmpfs /run", "--ro-bind / /", "--unshare-pid", "--unshare-ipc",
		"--new-session", "--cap-drop ALL", "--clearenv", `--bind "$cwd" "$cwd"`, ".claude", ".codex", ".config/opencode", ".gemini",
		".config/github-copilot", ".ssh", ".config/autostart", "go-build", "/opt/tc"} {
		if !strings.Contains(out, w) {
			t.Errorf("strict profile lacks %q", w)
		}
	}
	if strings.Contains(out, "--bind / /") && !strings.Contains(out, "--ro-bind / /") {
		t.Error("root is writable")
	}
	if strings.Contains(out, "SSH_AUTH_SOCK") && !strings.Contains(out, "not present") {
		t.Error("SSH_AUTH_SOCK must not be passed")
	}
	for _, bad := range []string{"relative/path", "", "a\nb"} {
		if _, err := StrictBwrapProfile(StrictOptions{AllowRW: []string{bad}}); err == nil {
			t.Errorf("path %q accepted", bad)
		}
	}
}

func TestStrictFlagsAreChecked(t *testing.T) {
	for _, args := range [][]string{{"--allow-rw", "~/x"}, {"--strict", "--compat"}, {"--strict", "--runtime", "seatbelt"}} {
		cmd := NewCommand(Deps{})
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(append([]string{"sandbox"}, args...))
		if cmd.Execute() == nil {
			t.Errorf("%v accepted", args)
		}
	}
	cmd := NewCommand(Deps{})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"sandbox", "--strict", "--allow-rw", "~/cache"})
	if err := cmd.Execute(); err != nil || !strings.Contains(buf.String(), "--strict") {
		t.Fatalf("err=%v out=%s", err, buf.String())
	}
}

func TestStrictRealRun(t *testing.T) {
	needBwrap(t)
	base := t.TempDir()
	home := filepath.Join(base, "home")
	proj := filepath.Join(base, "proj")
	// Agent sockets live in /run and /tmp, which the profile replaces with empty tmpfs mounts.
	runtimeDir, err := os.MkdirTemp("/tmp", "sbx-run-")
	if err != nil {
		t.Skipf("cannot make a directory in /tmp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(runtimeDir) })
	for _, d := range []string{".monoagent", ".ssh", ".config/autostart", ".claude", "cache"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	for f, c := range map[string]string{".monoagent/monoagent.db": "secret", ".ssh/id_x": "key", ".bashrc": "orig", ".claude/login": "login"} {
		if err := os.WriteFile(filepath.Join(home, f), []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sock := filepath.Join(runtimeDir, "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	script, err := StrictBwrapProfile(StrictOptions{AllowRW: []string{"~/cache"}})
	if err != nil {
		t.Fatal(err)
	}
	probe := `
echo "db=$(cat "$HOME/.monoagent/monoagent.db" 2>/dev/null || echo none)"
echo "key=$(cat "$HOME/.ssh/id_x" 2>/dev/null || echo none)"
echo "bashrc_before=$(cat "$HOME/.bashrc" 2>/dev/null || echo none)"
echo "login=$(cat "$HOME/.claude/login" 2>/dev/null || echo none)"
echo planted > "$HOME/.bashrc" && echo "bashrc_write=ok"
mkdir -p "$HOME/.config/autostart" && echo planted > "$HOME/.config/autostart/x" && echo "autostart_write=ok"
mv "$HOME/.config" "$HOME/.config-moved" && echo "rename=ok"
if [ -S '` + sock + `' ]; then echo "sock=reachable"; else echo "sock=gone"; fi
touch "$PWD/made" && echo "cwd_write=ok"
touch "$HOME/cache/made" && echo "allow_rw=ok"
touch "$HOME/.local-bin-x" && echo "home_write=tmpfs"
echo "sshsock=${SSH_AUTH_SOCK:-unset}"
`
	out, err := runScript(t, script, home, proj, []string{"SSH_AUTH_SOCK=" + sock, "XDG_RUNTIME_DIR=" + runtimeDir}, "sh", "-c", probe)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	for _, w := range []string{"db=none", "key=none", "bashrc_before=none", "login=login", "bashrc_write=ok", "autostart_write=ok", "rename=ok",
		"sock=gone", "cwd_write=ok", "allow_rw=ok", "home_write=tmpfs", "sshsock=unset"} {
		if !strings.Contains(out, w) {
			t.Errorf("want %q in:\n%s", w, out)
		}
	}
	// The host is unchanged.
	if b, _ := os.ReadFile(filepath.Join(home, ".bashrc")); string(b) != "orig" {
		t.Errorf("host .bashrc changed: %q", b)
	}
	if _, err := os.Stat(filepath.Join(home, ".config/autostart/x")); err == nil {
		t.Error("autostart entry reached the host")
	}
	if _, err := os.Stat(filepath.Join(home, ".config")); err != nil {
		t.Error("renaming ~/.config inside moved it on the host")
	}
	if _, err := os.Stat(filepath.Join(home, ".local-bin-x")); err == nil {
		t.Error("a write to $HOME reached the host")
	}
	for _, f := range []string{filepath.Join(proj, "made"), filepath.Join(home, "cache", "made")} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("%s was not written on the host: %v", f, err)
		}
	}
}

// The path checks run in the script; they need no bwrap to refuse.
func TestStrictRefusesForbiddenPaths(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	proj := filepath.Join(base, "proj")
	for _, d := range []string{".ssh", ".config/autostart", ".local/bin", "proj"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, ".ssh"), filepath.Join(home, "proj", "innocent")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"~/.ssh", "~/.config/autostart/sub", "~/.config", "~/", "~/proj/innocent", "~/.local/bin", "/"} {
		script, err := StrictBwrapProfile(StrictOptions{AllowRW: []string{p}})
		if err != nil {
			continue // refused earlier, also fine
		}
		out, err := runScript(t, script, home, proj, nil, "true")
		if err == nil || !strings.Contains(out, "refusing") {
			t.Errorf("--allow-rw %s was not refused: err=%v out=%s", p, err, out)
		}
	}
	script, _ := StrictBwrapProfile(StrictOptions{})
	for _, dir := range []string{home, filepath.Join(home, ".ssh"), filepath.Join(home, ".config"), base} {
		out, err := runScript(t, script, home, dir, nil, "true")
		if err == nil || !strings.Contains(out, "refusing") {
			t.Errorf("working directory %s was not refused: err=%v out=%s", dir, err, out)
		}
	}
}

// The strict box gets a dead session bus address after the allowlist loop, so a CLI in it that touches the vault
// cannot make godbus autolaunch a bus, and the address wins even if it were ever allowlisted.
func TestStrictProfileGivesTheBoxADeadSessionBus(t *testing.T) {
	out, err := StrictBwrapProfile(StrictOptions{})
	if err != nil {
		t.Fatal(err)
	}
	const dead = "--setenv DBUS_SESSION_BUS_ADDRESS unix:path=/nonexistent"
	i, loop := strings.Index(out, dead), strings.Index(out, "\ndone\n")
	if i < 0 || loop < 0 || i < loop {
		t.Errorf("the dead bus address is missing or comes before the end of the allowlist loop (%d, %d)", i, loop)
	}
	for _, k := range []string{"DBUS_SESSION_BUS_ADDRESS", "DISPLAY", "WAYLAND_DISPLAY", "XDG_RUNTIME_DIR"} {
		if strings.Contains(shellWords(envAllowlist), k) {
			t.Errorf("the allowlist carries %s", k)
		}
	}
}
