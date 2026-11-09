package main

import (
	"encoding/json"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/monomind"
)

// `agent test` runs its turn sandboxed. The fake monomind and its helpers are copies of
// internal/cli/chat's (chat_sandbox_test.go), where the chat turns are tested.

// writeSandboxMonomind writes a fake monomind of version that advertises
// (or not) agent-exec-sandbox on top of caps, logs every agent exec argv,
// and streams transcript for it.
func writeSandboxMonomind(t *testing.T, version string, advertise bool, transcript string, caps ...string) (bin, argsLog string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake monomind is a shell script")
	}
	all := append([]string{"agent-exec", "agent-scan", "org-json-v1"}, caps...)
	if advertise {
		all = append(all, monomind.CapAgentExecSandbox)
	}
	capsJSON, _ := json.Marshal(all)
	dir := t.TempDir()
	bin = filepath.Join(dir, "monomind")
	argsLog = filepath.Join(dir, "exec-args.log")
	script := "#!/bin/sh\n" +
		`if [ "$1" = "--version" ]; then echo '{"v":1,"version":"` + version + `","min_caller":"1.0.0","capabilities":` + string(capsJSON) + `}'; exit 0; fi` + "\n" +
		// agent scan answers like monomind 2.19.0: only codex and grok
		// have a sandbox of their own; every other runtime lists "full".
		`if [ "$1" = "agent" ] && [ "$2" = "scan" ]; then echo '{"v":1,"agents":[` +
		`{"id":"codex","installed":true,"sandbox_modes":["read-only","workspace-write","full"]},` +
		`{"id":"grok","installed":true,"sandbox_modes":["read-only","workspace-write","full"]},` +
		`{"id":"claude","installed":true,"sandbox_modes":["full"]},` +
		`{"id":"copilot","installed":true,"sandbox_modes":["full"]},` +
		`{"id":"qwen","installed":true,"sandbox_modes":["full"]}]}'; exit 0; fi` + "\n" +
		`if [ "$1" = "agent" ] && [ "$2" = "exec" ]; then` + "\n" +
		`  echo "$*" >> '` + argsLog + "'\n" + transcript + "\n  exit 0\nfi\n" +
		`echo "unsupported: $*" >&2` + "\nexit 2\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(monomind.EnvOverride, bin)
	return bin, argsLog
}

func sandboxTranscript(startExtra string) string {
	return `  echo '{"v":1,"type":"start","runtime":"codex","pid":1` + startExtra + `}'
  echo '{"v":1,"type":"assistant","text":"hi"}'
  echo '{"v":1,"type":"result","subtype":"success","stop_reason":"end_turn","text":"hi"}'
  echo '{"v":1,"type":"done","exit_code":0}'`
}

func lastExecLine(t *testing.T, argsLog string) string {
	t.Helper()
	logged, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatalf("no agent exec ran: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(logged)), "\n")
	return lines[len(lines)-1]
}

func TestAgentTestIsSandboxed(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	_, argsLog := writeSandboxMonomind(t, "2.18.5", false, sandboxTranscript(""))
	cmd := newAgentTestCmd(&globalConfig{})
	cmd.SetArgs([]string{"codex"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	line := lastExecLine(t, argsLog)
	want := "--cwd " + filepath.Join(os.Getenv("HOME"), ".monoagent", "workspaces", monomind.WorkspaceAgentTest)
	if !strings.Contains(line, "--env MONOMIND_GIT_LEVEL=read") || !strings.Contains(line, want) {
		t.Errorf("agent test argv: %s", line)
	}
}
