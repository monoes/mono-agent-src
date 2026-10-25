//go:build !windows

package main

import (
	"strings"
	"testing"
)

// The app reveals a secret through the CLI as the user, with this argv.
// cmd/monoagentcli's TestCommandsThatPrintSecretsAreTheOperatorsOnly runs
// the same argv from an unmarked process and gets the value, and the agent's
// markers refused: keep the two in step.
func TestGetSecretFieldsArgv(t *testing.T) {
	argsLog, _ := connFakeCLI(t, `case "$*" in
  *"secret reveal"*) echo '{"fields":{"secret":"vsecret-777"},"notes":""}' ;;
esac
`)
	a := newConnTestApp(t)
	got, err := a.GetSecretFields("vs")
	if err != nil || got.Fields["secret"] != "vsecret-777" {
		t.Fatalf("GetSecretFields = %+v, %v", got, err)
	}
	if args := strings.TrimSpace(readLog(t, argsLog)); args != "--profile work --json secret reveal vs --reveal" {
		t.Fatalf("argv = %q", args)
	}
}

func TestVaultAgentAccessArgv(t *testing.T) {
	argsLog, _ := connFakeCLI(t, `echo '{"profile_id":"work","level":"strict","agent_access":false}'`)
	a := newConnTestApp(t)
	got, err := a.GetVaultAgentAccess()
	if err != nil || got["level"] != "strict" {
		t.Fatalf("GetVaultAgentAccess = %+v, %v", got, err)
	}
	if args := strings.TrimSpace(readLog(t, argsLog)); args != "--profile work --json secret agent-access" {
		t.Fatalf("argv = %q", args)
	}
}

func TestSetVaultAgentAccessArgv(t *testing.T) {
	for _, level := range []string{"strict", "use", "reveal"} {
		t.Run(level, func(t *testing.T) {
			argsLog, _ := connFakeCLI(t, `echo '{"profile_id":"work","level":"`+level+`"}'`)
			a := newConnTestApp(t)
			got, err := a.SetVaultAgentAccess(level)
			if err != nil || got["level"] != level {
				t.Fatalf("SetVaultAgentAccess = %+v, %v", got, err)
			}
			want := "--profile work --json secret agent-access " + level
			if level != "strict" {
				want += " --yes"
			}
			if args := strings.TrimSpace(readLog(t, argsLog)); args != want {
				t.Fatalf("argv = %q, want %q", args, want)
			}
		})
	}
}

func TestSetVaultAgentAccessRejectsUnknownLevel(t *testing.T) {
	a := newConnTestApp(t)
	if _, err := a.SetVaultAgentAccess("open-everything"); err == nil {
		t.Fatal("unknown access level accepted")
	}
}
