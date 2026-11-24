package monomind

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestIsInitializedAt(t *testing.T) {
	root := t.TempDir()
	if IsInitializedAt(root) {
		t.Fatal("expected false for a folder with no .monomind/config.yaml")
	}
	dir := filepath.Join(root, ".monomind")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if IsInitializedAt(root) {
		t.Fatal("expected false: .monomind/ exists but config.yaml does not (EnsureLayout creates the bare dir)")
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("name: test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !IsInitializedAt(root) {
		t.Fatal("expected true once .monomind/config.yaml exists")
	}
}

// TestInitFailureMessage_127KeepsOutputAndHints guards the reported
// "Failed: initialize monomind / exit status 127" with nothing else to go on.
func TestInitFailureMessage_127KeepsOutputAndHints(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	err := exec.Command("sh", "-c", "exit 127").Run()
	msg := InitFailureMessage("/x/bin/monomind", err, []string{"env: node: No such file or directory"})
	for _, want := range []string{"exit status 127", "/x/bin/monomind", "node", "env: node: No such file or directory"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q missing %q", msg, want)
		}
	}
}

// InitProfile runs `monomind init --yes --no-watch --no-install` in the
// folder with CI=true, streams its output, then registers the folder with
// Claude Code by one `claude -p` turn there.
func TestInitProfile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fakes")
	}
	root := t.TempDir()
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "calls")
	fake := "#!/bin/sh\necho \"monomind $* ci=$CI cwd=$(pwd)\" >> " + log + "\necho copying skills\nmkdir -p .monomind && echo x > .monomind/config.yaml\n"
	os.WriteFile(filepath.Join(bin, "monomind"), []byte(fake), 0o755)
	os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\necho \"claude $* cwd=$(pwd)\" >> "+log+"\n"), 0o755)
	t.Setenv(EnvOverride, filepath.Join(bin, "monomind"))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/bin:/usr/bin")

	var lines []string
	if err := InitProfile(context.Background(), InitOptions{Root: root, Progress: func(l string) { lines = append(lines, l) }}); err != nil {
		t.Fatal(err)
	}
	if !IsInitializedAt(root) {
		t.Fatal("folder not initialized")
	}
	calls, _ := os.ReadFile(log)
	realRoot, _ := filepath.EvalSymlinks(root)
	for _, want := range []string{
		"monomind init --yes --no-watch --no-install ci=true cwd=" + realRoot,
		"claude -p monomind initialized cwd=" + realRoot,
	} {
		if !strings.Contains(string(calls), want) {
			t.Errorf("calls\n%s\nmissing %q", calls, want)
		}
	}
	out := strings.Join(lines, "\n")
	if !strings.Contains(out, "copying skills") || !strings.Contains(out, "Registered with Claude Code") {
		t.Errorf("progress:\n%s", out)
	}
}

// A profile folder that already holds .claude/settings.json (hooks an agent may
// have written) is not registered by a `claude -p` turn there: claude would run
// those hooks outside any sandbox (r08-D07).
func TestInitProfileSkipsClaudeTurnWhenSettingsExist(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fakes")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".claude", "settings.local.json"), []byte(`{"hooks":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "calls")
	os.WriteFile(filepath.Join(bin, "monomind"), []byte("#!/bin/sh\nmkdir -p .monomind && echo x > .monomind/config.yaml\n"), 0o755)
	os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\necho \"claude $*\" >> "+log+"\n"), 0o755)
	t.Setenv(EnvOverride, filepath.Join(bin, "monomind"))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/bin:/usr/bin")
	var lines []string
	if err := InitProfile(context.Background(), InitOptions{Root: root, Progress: func(l string) { lines = append(lines, l) }}); err != nil {
		t.Fatal(err)
	}
	if calls, _ := os.ReadFile(log); len(calls) != 0 {
		t.Errorf("claude ran in a folder with settings: %s", calls)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "skipped") {
		t.Errorf("progress should say the step was skipped: %q", lines)
	}
}

// A monomind with capability init-json registers the folder itself
// (--register-claude-project): no claude turn, and Repin adds --force.
func TestInitProfileRegistersThroughMonomind(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fakes")
	}
	root := t.TempDir()
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "calls")
	fake := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then echo '{\"v\":1,\"version\":\"2.24.4\",\"min_caller\":\"1.0.0\",\"capabilities\":[\"agent-exec\",\"agent-scan\",\"org-json-v1\",\"init-json\"]}'; exit 0; fi\n" +
		"echo \"monomind $*\" >> " + log + "\n" +
		"case \"$*\" in *--json*) echo '{\"claude_project_registered\":true}';; *) mkdir -p .monomind && echo x > .monomind/config.yaml;; esac\n"
	os.WriteFile(filepath.Join(bin, "monomind"), []byte(fake), 0o755)
	os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\necho \"claude $*\" >> "+log+"\n"), 0o755)
	t.Setenv(EnvOverride, filepath.Join(bin, "monomind"))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/bin:/usr/bin")

	var lines []string
	if err := InitProfile(context.Background(), InitOptions{Root: root, Repin: true, Progress: func(l string) { lines = append(lines, l) }}); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(log)
	for _, want := range []string{
		"monomind init --yes --no-watch --no-install --force\n",
		"--if-missing --json --no-graph --register-claude-project --yes --no-watch --no-install\n",
	} {
		if !strings.Contains(string(calls), want) {
			t.Errorf("calls\n%s\nmissing %q", calls, want)
		}
	}
	if strings.Contains(string(calls), "claude ") {
		t.Errorf("a claude turn ran although monomind registers the folder:\n%s", calls)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "Registered with Claude Code") {
		t.Errorf("progress:\n%s", strings.Join(lines, "\n"))
	}
}

// Without claude on PATH the registration is skipped and said so; a
// failing init returns its last output lines.
func TestInitProfileWithoutClaudeAndFailing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fakes")
	}
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "monomind"), []byte("#!/bin/sh\necho ok\n"), 0o755)
	t.Setenv(EnvOverride, filepath.Join(bin, "monomind"))
	t.Setenv("PATH", t.TempDir())
	var lines []string
	if err := InitProfile(context.Background(), InitOptions{Root: t.TempDir(), Progress: func(l string) { lines = append(lines, l) }}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "claude CLI not found") {
		t.Errorf("progress should say the claude step was skipped: %q", lines)
	}

	os.WriteFile(filepath.Join(bin, "monomind"), []byte("#!/bin/sh\necho boom: no space left\nexit 3\n"), 0o755)
	err := InitProfile(context.Background(), InitOptions{Root: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "exit status 3") || !strings.Contains(err.Error(), "boom: no space left") {
		t.Fatalf("failing init: %v", err)
	}
}
