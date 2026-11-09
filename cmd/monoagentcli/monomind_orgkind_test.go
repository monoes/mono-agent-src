package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/testhome"
)

// The binary vouches for the org kind of the role shells its org runs start.
func TestOrgLaunchesIssueAnOrgKindToken(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	if monomind.OrgKindEnv == nil {
		t.Fatal("no org kind hook installed")
	}
	env, _, done := monomind.OrgKindEnv()
	if len(env) != 2 || env[0] != browserctl.EnvAgentKind+"=org" || !strings.HasPrefix(env[1], browserctl.EnvAgentKindToken+"=") {
		t.Fatalf("env = %q", env)
	}
	record := filepath.Join(home, ".monoagent", "agent-kinds", strings.TrimPrefix(env[1], browserctl.EnvAgentKindToken+"=")+".json")
	if _, err := os.Stat(record); err != nil {
		t.Fatalf("no record: %v", err)
	}
	done()
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Errorf("the record outlives the run: %v", err)
	}
}

// An org started detached (org run --start) is still kind org after the
// launcher that started it exits, while the org runs, and no longer once
// the org has ended.
func TestDetachedOrgKeepsItsKindAfterTheLauncherExits(t *testing.T) {
	const helperEnv = "MONOAGENT_TEST_ORG_LAUNCHER"
	if root := os.Getenv(helperEnv); root != "" {
		if err := monomind.OrgRunStart(context.Background(), root, "sec", ""); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if runtime.GOOS == "windows" {
		t.Skip("the fake monomind needs sh")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node.js not installed") // org run --start drains its output through node
	}
	// The launcher exits from inside the test, so a test home of its own would
	// never be removed: it keeps this test's HOME (testhome.KeepHomeEnv), which
	// lies in a temp dir of the launcher's where a leftover would show.
	tmp, root := t.TempDir(), t.TempDir()
	home := filepath.Join(tmp, "home")
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatal(err)
	}
	testhome.SetHome(t, home)
	rt := filepath.Join(root, ".monomind", "orgs", "sec", "runtime.json")
	if err := os.MkdirAll(filepath.Dir(rt), 0o755); err != nil {
		t.Fatal(err)
	}
	seen := filepath.Join(root, "token")
	bin := filepath.Join(t.TempDir(), "monomind")
	script := "#!/bin/sh\n" +
		`if [ "$1" = "--version" ]; then echo '{"v":1,"version":"2.24.1","min_caller":"1.0.0","capabilities":["agent-exec","agent-scan","org-json-v1"]}'; exit 0; fi` + "\n" +
		`echo "$MONOAGENT_AGENT_KIND_TOKEN" > '` + seen + "'\n" +
		`printf '{"status":"running","run":"r1","pid":%s,"updated":"%s"}' $$ "$(date -u +%Y-%m-%dT%H:%M:%S.000Z)" > '` + rt + "'\nexec sleep 60\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	launcher := exec.Command(os.Args[0], "-test.run=^TestDetachedOrgKeepsItsKindAfterTheLauncherExits$")
	launcher.Env = append(os.Environ(), helperEnv+"="+root, monomind.EnvOverride+"="+bin,
		testhome.KeepHomeEnv+"=1", "USERPROFILE="+home, "TMPDIR="+tmp)
	if out, err := launcher.CombinedOutput(); err != nil {
		t.Fatalf("launcher: %v: %s", err, out)
	}
	if left, _ := filepath.Glob(filepath.Join(tmp, "monoagent-testhome-*")); len(left) > 0 {
		t.Fatalf("the launcher left a test home behind: %v", left)
	}
	var run struct {
		PID int `json:"pid"`
	}
	raw, _ := os.ReadFile(rt)
	if err := json.Unmarshal(raw, &run); err != nil || run.PID <= 0 {
		t.Fatalf("runtime.json %q: %v", raw, err)
	}
	org, err := os.FindProcess(run.PID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = org.Kill() })
	token, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}

	// A role shell of that org.
	t.Setenv(browserctl.EnvAgentKind, browserctl.KindOrg)
	t.Setenv(browserctl.EnvAgentKindToken, strings.TrimSpace(string(token)))
	t.Setenv("MONOMIND_ORG_ROLE", "dev")
	if c := browserctl.AgentCaller(); c != (browserctl.Caller{Kind: browserctl.KindOrg}) {
		t.Fatalf("with the launcher gone and the org running, the role is %+v; want org", c)
	}
	if err := org.Kill(); err != nil {
		t.Fatal(err)
	}
	want := browserctl.Caller{Kind: browserctl.KindOther, Declared: browserctl.KindOrg}
	for deadline := time.Now().Add(10 * time.Second); browserctl.AgentCaller() != want; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("with the org gone, the role is %+v; want %+v", browserctl.AgentCaller(), want)
		}
	}
}
