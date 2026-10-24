package agent

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/agentinstall"
	"github.com/monoes/mono-agent/internal/monomind"
)

func sp(s string) *string { return &s }

// fakeMachine scans as before until install runs, then as after.
type fakeMachine struct {
	before, after monomind.ScanResult
	binDir        string
	installed     []agentinstall.Recipe
	installErr    error
}

func (f *fakeMachine) machine() runtimeMachine {
	return runtimeMachine{
		scan: func(context.Context) (*monomind.ScanResult, error) {
			if len(f.installed) > 0 {
				return &f.after, nil
			}
			return &f.before, nil
		},
		install: func(_ context.Context, r agentinstall.Recipe, progress func(string)) error {
			f.installed = append(f.installed, r)
			progress("installing")
			return f.installErr
		},
		npmBinDir:  func(context.Context) (string, error) { return f.binDir, nil },
		loginPath:  func(context.Context) string { return "/usr/bin:/bin" },
		nodeDirFor: func(string) string { return "" },
	}
}

var yesScript = func(agentinstall.Recipe) bool { return true }

func npmEntry(id string, installed bool, bin, version string) monomind.ScanEntry {
	e := monomind.ScanEntry{ID: id, Installed: installed, InstallHint: "npm install -g " + id + "-pkg",
		Install: &monomind.InstallRecipe{Kind: "npm", Packages: []string{id + "-pkg"}}}
	if bin != "" {
		e.Binary = sp(bin)
	}
	if version != "" {
		e.Version = sp(version)
	}
	return e
}

func TestInstallRuntimeUnknownID(t *testing.T) {
	t.Parallel()
	f := &fakeMachine{before: monomind.ScanResult{Agents: []monomind.ScanEntry{npmEntry("codex", false, "", ""), npmEntry("claude", false, "", "")}}}
	_, err := installRuntime(context.Background(), f.machine(), "nope", false, yesScript, func(string) {})
	if exitCodeFor(err) != 2 || !strings.Contains(err.Error(), "known: claude, codex") {
		t.Fatalf("unknown id: %v (exit %d)", err, exitCodeFor(err))
	}
	if len(f.installed) != 0 {
		t.Fatal("installed something for an unknown id")
	}
}

func TestInstallRuntimeAlreadyInstalledIsANoOp(t *testing.T) {
	t.Parallel()
	f := &fakeMachine{before: monomind.ScanResult{Agents: []monomind.ScanEntry{npmEntry("claude", true, "/usr/bin/claude", "2.0.1")}}}
	msg, err := installRuntime(context.Background(), f.machine(), "claude", false, yesScript, func(string) {})
	if err != nil || len(f.installed) != 0 {
		t.Fatalf("already installed: %v, installs %v", err, f.installed)
	}
	if !strings.Contains(msg, "already installed") || !strings.Contains(msg, "2.0.1") {
		t.Fatalf("message %q should say it is already installed", msg)
	}
}

func TestInstallRuntimeFreshInstall(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	f := &fakeMachine{binDir: dir,
		before: monomind.ScanResult{Agents: []monomind.ScanEntry{npmEntry("claude", false, "", "")}},
		after:  monomind.ScanResult{Agents: []monomind.ScanEntry{npmEntry("claude", true, bin, "2.0.1")}}}
	var lines []string
	msg, err := installRuntime(context.Background(), f.machine(), "claude", false, yesScript, func(l string) { lines = append(lines, l) })
	if err != nil || !strings.Contains(msg, "installed at "+bin) {
		t.Fatalf("fresh install: %q, %v", msg, err)
	}
	// The folder is not on the user's PATH: the hint names the full path.
	if log := strings.Join(lines, "\n"); !strings.Contains(log, "sign in: run `"+bin+"`") {
		t.Errorf("sign-in hint should use the full path:\n%s", log)
	}
}

// --force on a runtime that came from somewhere else (mise) would npm-install
// a second copy that the first still shadows: refused before installing.
func TestInstallRuntimeForceRefusesAShadowedCopy(t *testing.T) {
	t.Parallel()
	f := &fakeMachine{binDir: t.TempDir(),
		before: monomind.ScanResult{Agents: []monomind.ScanEntry{npmEntry("claude", true, "/home/u/.local/share/mise/installs/node/24/bin/claude", "2.0.1")}}}
	_, err := installRuntime(context.Background(), f.machine(), "claude", true, yesScript, func(string) {})
	if exitCodeFor(err) != 3 || !strings.Contains(err.Error(), "mise") || !strings.Contains(err.Error(), "second copy") {
		t.Fatalf("want a refusal naming the other copy, got %v", err)
	}
	if len(f.installed) != 0 {
		t.Fatal("installed a second copy")
	}
}

// --force where npm installs over the same copy: an update when the
// version changed, and said plainly when it did not.
func TestInstallRuntimeForceReportsWhatChanged(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	f := &fakeMachine{binDir: dir,
		before: monomind.ScanResult{Agents: []monomind.ScanEntry{npmEntry("claude", true, bin, "2.0.1")}},
		after:  monomind.ScanResult{Agents: []monomind.ScanEntry{npmEntry("claude", true, bin, "2.1.0")}}}
	msg, err := installRuntime(context.Background(), f.machine(), "claude", true, yesScript, func(string) {})
	if err != nil || !strings.Contains(msg, "updated from 2.0.1 to 2.1.0") {
		t.Fatalf("upgrade: %q, %v", msg, err)
	}
	f.installed = nil
	f.after = f.before
	msg, err = installRuntime(context.Background(), f.machine(), "claude", true, yesScript, func(string) {})
	if err != nil || !strings.Contains(msg, "still 2.0.1") || strings.Contains(msg, "updated") {
		t.Fatalf("same version: %q, %v", msg, err)
	}
}

func TestInstallRuntimeManualIsRefused(t *testing.T) {
	t.Parallel()
	e := monomind.ScanEntry{ID: "grok", InstallHint: "install the Grok CLI per https://docs.x.ai"}
	f := &fakeMachine{before: monomind.ScanResult{Agents: []monomind.ScanEntry{e}}}
	_, err := installRuntime(context.Background(), f.machine(), "grok", false, yesScript, func(string) {})
	if exitCodeFor(err) != 3 || !strings.Contains(err.Error(), "docs.x.ai") || len(f.installed) != 0 {
		t.Fatalf("manual: %v, installs %v", err, f.installed)
	}
}

func TestInstallRuntimeDeclinedScriptIsNotRun(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("no vendor scripts on Windows")
	}
	e := monomind.ScanEntry{ID: "antigravity", InstallHint: "curl -fsSL https://antigravity.google/cli/install.sh | bash"}
	f := &fakeMachine{before: monomind.ScanResult{Agents: []monomind.ScanEntry{e}}}
	asked := false
	_, err := installRuntime(context.Background(), f.machine(), "antigravity", false,
		func(r agentinstall.Recipe) bool { asked = true; return false }, func(string) {})
	if !asked || exitCodeFor(err) != 3 || !strings.Contains(err.Error(), "antigravity.google") || len(f.installed) != 0 {
		t.Fatalf("declined: asked %v, %v, installs %v", asked, err, f.installed)
	}
}

func TestInstallRuntimeInstallerFailure(t *testing.T) {
	t.Parallel()
	f := &fakeMachine{binDir: t.TempDir(), installErr: errors.New("npm exploded"),
		before: monomind.ScanResult{Agents: []monomind.ScanEntry{npmEntry("codex", false, "", "")}}}
	if _, err := installRuntime(context.Background(), f.machine(), "codex", false, yesScript, func(string) {}); err == nil || !strings.Contains(err.Error(), "npm exploded") {
		t.Fatalf("installer failure: %v", err)
	}
}

func TestSignInHint(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX paths")
	}
	npmBin := "/home/u/.monoagent/npm-global/bin/claude"
	node := "/home/u/.monoagent/node/24.1.0/bin"
	cases := []struct {
		bin, login, path, node, want string
	}{
		// On the user's PATH, node too: the bare command.
		{"/usr/bin/claude", "", "/usr/bin:/bin", "/usr/bin", "run `claude` once in a terminal if it asks you to log in"},
		// Managed Node: neither folder is on the user's PATH.
		{npmBin, "", "/usr/bin:/bin", node, "run `PATH=" + node + ":\"$PATH\" " + npmBin + "` once in a terminal if it asks you to log in"},
		// monomind's own login command, first word is the binary.
		{npmBin, "claude login", "/usr/bin", "", npmBin + " login"},
		// A login command that doesn't start with the binary name.
		{npmBin, "open https://x.example/login", "/usr/bin", "", "open https://x.example/login (claude is at " + npmBin + ")"},
		// Unknown login PATH: full path to be safe.
		{"/usr/bin/codex", "", "", "", "run `/usr/bin/codex` once in a terminal if it asks you to log in"},
		// A path with a space is quoted.
		{"/opt/my tools/codex", "", "/usr/bin", "", "run `'/opt/my tools/codex'` once in a terminal if it asks you to log in"},
	}
	for _, c := range cases {
		if got := signInHint(c.bin, c.login, c.path, c.node); got != c.want {
			t.Errorf("signInHint(%q, %q, %q, %q)\n got %s\nwant %s", c.bin, c.login, c.path, c.node, got, c.want)
		}
	}
}

func TestKiloInstallUsesScanRecipe(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bin := filepath.Join(dir, "kilo")
	entry := monomind.ScanEntry{ID: "kilo", InstallHint: "npm install -g @kilocode/cli",
		Install: &monomind.InstallRecipe{Kind: "npm", Packages: []string{"@kilocode/cli"}}, LoginHint: sp("kilo auth login")}
	after := entry
	after.Installed, after.Binary = true, sp(bin)
	f := &fakeMachine{binDir: dir, before: monomind.ScanResult{Agents: []monomind.ScanEntry{entry}},
		after: monomind.ScanResult{Agents: []monomind.ScanEntry{after}}}
	var lines []string
	_, err := installRuntime(context.Background(), f.machine(), "kilo", false, yesScript, func(l string) { lines = append(lines, l) })
	if err != nil {
		t.Fatal(err)
	}
	if len(f.installed) != 1 || f.installed[0].Kind != agentinstall.KindNpm || len(f.installed[0].Packages) != 1 || f.installed[0].Packages[0] != "@kilocode/cli" {
		t.Fatalf("wrong recipe: %+v", f.installed)
	}
	if log := strings.Join(lines, "\n"); !strings.Contains(log, "kilo auth login") {
		t.Fatalf("missing scan login hint: %s", log)
	}
	old := &fakeMachine{before: monomind.ScanResult{Agents: []monomind.ScanEntry{npmEntry("codex", true, "", "")}}}
	_, err = installRuntime(context.Background(), old.machine(), "kilo", false, yesScript, func(string) {})
	if exitCodeFor(err) != 2 || len(old.installed) != 0 {
		t.Fatalf("unknown Kilo installed: %v, %+v", err, old.installed)
	}
}
