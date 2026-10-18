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
