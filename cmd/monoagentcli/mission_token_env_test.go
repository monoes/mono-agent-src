package main

import (
	"os"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/account/accounttest"
	"github.com/monoes/mono-agent/internal/coderhost"
)

// The coder host's confirmation token must be out of the process environment
// before the first thing that can start a subprocess (the account guard's key
// store, the update and tool probes) runs: a child started there would inherit it.
// The guard is the first such thing run reaches, so the test looks at the
// environment from inside it.
func TestMissionTokenLeavesTheEnvironmentBeforeAnythingCanStartAProcess(t *testing.T) {
	account.InstallForTest(t, nil)
	_ = accounttest.New(t)
	freshHome(t)
	_ = coderhost.TakeMissionToken() // nothing stashed by an earlier test
	t.Setenv(coderhost.MissionTokenEnv, "7.secret-nonce")

	var inEnv, childSees bool
	prev := newDefaultGuard
	newDefaultGuard = func() (*account.Guard, error) {
		_, inEnv = os.LookupEnv(coderhost.MissionTokenEnv)
		for _, kv := range os.Environ() {
			if strings.HasPrefix(kv, coderhost.MissionTokenEnv+"=") {
				childSees = true // what a process started here would be given
			}
		}
		return nil, os.ErrNotExist
	}
	t.Cleanup(func() { newDefaultGuard = prev })

	runMain(t, "version")
	if inEnv || childSees {
		t.Fatalf("the token was still in the environment when the guard ran (LookupEnv %v, Environ %v)", inEnv, childSees)
	}
	if got := coderhost.TakeMissionToken(); got != "7.secret-nonce" {
		t.Fatalf("the chat command would get %q, want the stashed token", got)
	}
}
