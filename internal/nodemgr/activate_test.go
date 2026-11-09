package nodemgr

import (
	"context"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The GUI re-runs Activate after an install, update or removal instead of
// asking for a restart: PATH then holds exactly the current version.
func TestActivateAgain(t *testing.T) {
	m := testManager(t)
	home := t.TempDir()
	testhome.SetHome(t, home)
	m.Root, m.NpmRoot = filepath.Join(home, ".monoagent", "node"), filepath.Join(home, ".monoagent", "npm-global")
	m.BaseURL = fakeDist(t, m, map[string][]byte{
		"24.1.0": nodeArchive(t, m, "24.1.0"), "26.1.0": nodeArchive(t, m, "26.1.0"),
	}, false).URL
	ctx := context.Background()

	sys := t.TempDir()
	os.WriteFile(filepath.Join(sys, "node"), []byte("#!/bin/sh\necho v20.0.0\n"), 0o755)
	userPath := sys + string(os.PathListSeparator) + "/bin:/usr/bin"
	t.Setenv("PATH", userPath)
	t.Setenv(UserPathEnv, "")
	t.Setenv("NPM_CONFIG_PREFIX", "")
	t.Setenv("npm_config_prefix", "")

	if _, err := m.Install(ctx, "24.1.0", nil); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(m.NpmBinDir(), 0o755)
	Activate(ctx)
	if first := filepath.SplitList(os.Getenv("PATH"))[0]; first != m.BinDir("24.1.0") {
		t.Fatalf("PATH starts with %s", first)
	}

	// The user's own commands can still find the user's node.
	if got := os.Getenv(UserPathEnv); got != userPath {
		t.Fatalf("%s = %q, want %q", UserPathEnv, got, userPath)
	}
	if p, _ := exec.LookPath("node"); p != m.NodePath("24.1.0") {
		t.Fatalf("PATH lookup of node = %s", p)
	}
	if p, err := LookPathUser("node"); err != nil || p != filepath.Join(sys, "node") {
		t.Fatalf("LookPathUser(node) = %q, %v", p, err)
	}
	env := UserEnv([]string{"A=1", "PATH=" + os.Getenv("PATH")})
	if !slices.Contains(env, "PATH="+userPath) || !slices.Contains(env, "A=1") || len(env) != 2 {
		t.Fatalf("UserEnv = %v", env)
	}

	// Update: the new version replaces the old one on PATH.
	if _, err := m.Install(ctx, "26.1.0", nil); err != nil {
		t.Fatal(err)
	}
	if err := m.Prune("26.1.0"); err != nil {
		t.Fatal(err)
	}
	Activate(ctx)
	path := os.Getenv("PATH")
	if first := filepath.SplitList(path)[0]; first != m.BinDir("26.1.0") || strings.Contains(path, "24.1.0") {
		t.Fatalf("after an update PATH = %s", path)
	}
	// Activating again changed nothing about what the user's PATH was.
	if got := os.Getenv(UserPathEnv); got != userPath {
		t.Fatalf("%s changed to %q", UserPathEnv, got)
	}

	// Removal: nothing managed stays on PATH, nor the npm prefix it set.
	if err := m.Remove(""); err != nil {
		t.Fatal(err)
	}
	Activate(ctx)
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if strings.Contains(dir, ".monoagent") {
			t.Fatalf("%s still on PATH after removal", dir)
		}
	}
	if got := os.Getenv("NPM_CONFIG_PREFIX"); got != "" {
		t.Fatalf("NPM_CONFIG_PREFIX = %q after removal", got)
	}
	if _, err := os.Stat(m.Root); err == nil {
		t.Fatalf("%s left after removing everything", m.Root)
	}
}

// Without Activate having prepended anything, UserPath is just PATH.
func TestUserPathDefaultsToPath(t *testing.T) {
	t.Setenv(UserPathEnv, "")
	t.Setenv("PATH", "/x/bin")
	if UserPath() != "/x/bin" {
		t.Fatalf("UserPath = %q", UserPath())
	}
	if _, err := LookPathUser("definitely-not-a-command"); err == nil {
		t.Fatal("found a command that doesn't exist")
	}
}
