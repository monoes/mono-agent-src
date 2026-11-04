package org

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/monomind"
)

// roleCaps is what the monomind of runOrgRole advertises (withRoleCaps).
var roleCaps []string

// withRoleCaps stands in for a monomind that advertises caps.
func withRoleCaps(t *testing.T, caps ...string) {
	t.Helper()
	roleCaps = caps
	t.Cleanup(func() { roleCaps = nil })
}

func runOrgRole(t *testing.T, root string, args ...string) (string, error) {
	t.Helper()
	d := testDeps(&testConfig{})
	d.CapabilityProbe = func(*cobra.Command) (*monomind.CapabilitySet, error) {
		return monomind.NewCapabilitySet("9.0.0", roleCaps...), nil
	}
	cmd := newEnv(d, nil).newOrgRoleCmd(func() (string, error) { return root, nil })
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(args)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	err := cmd.Execute()
	return out.String(), err
}

func TestOrgRoleSetAccessIsHumanOnly(t *testing.T) {
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "args.log")
	bin := filepath.Join(dir, "monomind")
	os.WriteFile(bin, []byte("#!/bin/sh\n"+
		`if [ "$1" = "--version" ]; then echo '{"v":1,"version":"9.0.0","min_caller":"1.0.0","capabilities":["agent-exec","agent-scan","org-json-v1"]}'; exit 0; fi`+"\n"+
		`echo "$(pwd) $*" >> '`+argsLog+"'\necho 'granted'\n"), 0o755)
	t.Setenv(monomind.EnvOverride, bin)
	for _, k := range agentMarkersForTest {
		t.Setenv(k, "")
	}
	root := t.TempDir()
	logged := func() string { b, _ := os.ReadFile(argsLog); return string(b) }

	withRoleCaps(t, monomind.CapOrgRoleFullAccess)
	for _, marker := range []string{"CLAUDECODE", "MONOMIND_AGENT_EXEC", "MONOMIND_ORG_ROLE", "CODEX_SANDBOX", "OPENCODE", "GEMINI_CLI", "AI_AGENT", "COPILOT_AGENT_SESSION_ID", "PI_CODING_AGENT"} {
		t.Setenv(marker, "1")
		if _, err := runOrgRole(t, root, "set-access", "growth", "builder", "full", "--yes-i-understand"); err == nil || !strings.Contains(err.Error(), marker) {
			t.Errorf("%s set: grant not refused: %v", marker, err)
		}
		t.Setenv(marker, "")
	}
	if logged() != "" {
		t.Fatalf("monomind was called from an agent context:\n%s", logged())
	}

	if _, err := runOrgRole(t, root, "set-access", "growth", "builder", "full"); exitCodeFor(err) != 3 {
		t.Errorf("grant without --yes-i-understand: %v", err)
	}
	withRoleCaps(t)
	if _, err := runOrgRole(t, root, "set-access", "growth", "builder", "full", "--yes-i-understand"); err == nil || !strings.Contains(err.Error(), "org-role-full-access") {
		t.Errorf("old monomind: %v", err)
	}
	withRoleCaps(t, monomind.CapOrgRoleFullAccess)
	if out, err := runOrgRole(t, root, "set-access", "growth", "builder", "full", "--yes-i-understand"); err != nil || !strings.Contains(out, `"access":"full"`) {
		t.Fatalf("grant: %v %s", err, out)
	}
	real, _ := filepath.EvalSymlinks(root)
	if want := real + " org role set-access growth builder full --yes-i-understand"; !strings.Contains(logged(), want) {
		t.Errorf("monomind argv: %q, want %q", logged(), want)
	}

	// A revoke stays allowed anywhere, even from an agent.
	t.Setenv("CLAUDECODE", "1")
	if _, err := runOrgRole(t, root, "set-access", "growth", "builder", "scoped"); err != nil {
		t.Errorf("revoke from an agent context: %v", err)
	}
	if !strings.Contains(logged(), "org role set-access growth builder scoped\n") {
		t.Errorf("revoke argv: %q", logged())
	}
	if _, err := runOrgRole(t, root, "set-access", "growth", "builder", "admin"); exitCodeFor(err) != 3 {
		t.Errorf("bad access value: %v", err)
	}
}

var agentMarkersForTest = []string{
	"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "MONOMIND_ORG_ROLE", "MONOMIND_SDK_AGENT", "MONOMIND_AGENT_EXEC",
	"AI_AGENT", "AGENT",
	"CODEX_SANDBOX", "CODEX_SANDBOX_NETWORK_DISABLED", "CODEX_THREAD_ID", "CODEX_CI",
	"OPENCODE", "OPENCODE_PID", "ANTIGRAVITY_AGENT", "GEMINI_CLI",
	"GROK_SESSION_ID", "GROK_MANAGED_BY_NPM",
	"COPILOT_CLI_BINARY_VERSION", "COPILOT_AGENT_SESSION_ID",
	"CRUSH", "PI_CODING_AGENT", "PI_SESSION_ID", "QWEN_CODE",
	"DSH_SHELL", "DSH_SESSION_ID", "MONOMIND_CLINE_TURN", "MONOMIND_AIDER",
}
