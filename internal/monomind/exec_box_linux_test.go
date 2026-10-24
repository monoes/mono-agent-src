//go:build linux

package monomind

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/agentbox"
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

type boxRig struct {
	home, proj, state, bin string
}

// newBoxRig makes a scratch HOME with a fake state directory and a fake monomind under it
// (so the box must bind it back), running script.
func newBoxRig(t *testing.T, script string) boxRig {
	t.Helper()
	base := t.TempDir()
	r := boxRig{home: filepath.Join(base, "home"), proj: filepath.Join(base, "proj")}
	r.state = filepath.Join(r.home, ".monoagent")
	dir := filepath.Join(r.home, "tools", "bin")
	for _, d := range []string{r.state, r.proj, dir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(r.state, "monoagent.db"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.bin = filepath.Join(dir, "monomind")
	body := "#!/bin/sh\n" + script + "\necho '{\"v\":1,\"type\":\"done\",\"exit_code\":0}'\n"
	if err := os.WriteFile(r.bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", r.home)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	return r
}

func (r boxRig) opts(mode agentbox.Mode) ExecOptions {
	return ExecOptions{
		Bin: r.bin, Runtime: "claude", Prompt: "hello box", Cwd: r.proj, Access: AccessFull,
		Box: &agentbox.Box{Mode: mode, Cwd: r.proj, Home: r.home, StateDir: r.state},
	}
}

func TestExecRunsTheTurnInAStrictBox(t *testing.T) {
	needBwrap(t)
	r := newBoxRig(t, `
while [ $# -gt 0 ]; do if [ "$1" = "--prompt-file" ]; then pf=$2; fi; shift; done
{
  echo "db=$(cat "$HOME/.monoagent/monoagent.db" 2>/dev/null || echo none)"
  echo "prompt=$(cat "$pf")"
  echo "sock=${SSH_AUTH_SOCK:-unset}"
  echo planted > "$HOME/.bashrc" && echo "bashrc=tmpfs"
} > "$PWD/result.txt"`)
	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent.sock")
	if err := os.WriteFile(filepath.Join(r.home, ".bashrc"), []byte("orig"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Exec(context.Background(), r.opts(agentbox.Strict), nil); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(r.proj, "result.txt"))
	if err != nil {
		t.Fatalf("the fake monomind did not run in the box: %v", err)
	}
	for _, w := range []string{"db=none", "prompt=hello box", "sock=unset", "bashrc=tmpfs"} {
		if !strings.Contains(string(got), w) {
			t.Errorf("want %q in:\n%s", w, got)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(r.home, ".bashrc")); string(b) != "orig" {
		t.Errorf("host .bashrc changed: %q", b)
	}
}

func TestExecBoxCanaryRefusesAndStartsNothing(t *testing.T) {
	needBwrap(t)
	r := newBoxRig(t, `touch "$PWD/ran"`)
	o := r.opts(agentbox.Strict)
	o.Box.StateDir = r.proj // the working directory is bound read-write: not hidden
	if err := os.WriteFile(filepath.Join(r.proj, "monoagent.db"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Exec(context.Background(), o, nil)
	if !errors.Is(err, ErrSandboxRequired) || !agentbox.IsCode(err, agentbox.CodeBadPath) {
		t.Fatalf("err = %v, want ErrSandboxRequired wrapping the refusal", err)
	}
	if _, serr := os.Stat(filepath.Join(r.proj, "ran")); serr == nil {
		t.Fatal("the turn started although the canary failed")
	}
}

func TestExecBoxCancelLeavesNoProcess(t *testing.T) {
	needBwrap(t)
	old := KillGrace
	KillGrace = 300 * time.Millisecond
	defer func() { KillGrace = old }()
	const marker = "913.137"
	r := newBoxRig(t, `sleep `+marker+` &
touch "$PWD/started"
wait`)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _, _ = Exec(ctx, r.opts(agentbox.Strict), nil); close(done) }()
	waitFor(t, func() bool { _, err := os.Stat(filepath.Join(r.proj, "started")); return err == nil })
	cancel()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("Exec did not return after cancel")
	}
	waitFor(t, func() bool { return !processWithArg(marker) })
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("condition not met in 10s")
}

// processWithArg reports whether a host process has arg in its command line.
func processWithArg(arg string) bool {
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err == nil && strings.Contains(string(b), "sleep\x00"+arg) {
			return true
		}
	}
	return false
}

// A launcher that someone else could replace starts nothing.
func TestExecBoxRefusesAnUntrustedLauncher(t *testing.T) {
	needBwrap(t)
	r := newBoxRig(t, `touch "$PWD/ran"`)
	dir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bwrap"), []byte("#!/bin/sh\ntouch \""+r.proj+"/ran\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	o := r.opts(agentbox.Strict)
	o.Box.Bwrap = filepath.Join(dir, "bwrap")
	_, err := Exec(context.Background(), o, nil)
	if !errors.Is(err, ErrSandboxRequired) || !agentbox.IsCode(err, agentbox.CodeUntrusted) {
		t.Fatalf("err = %v", err)
	}
	if _, serr := os.Stat(filepath.Join(r.proj, "ran")); serr == nil {
		t.Fatal("the untrusted launcher ran")
	}
}
