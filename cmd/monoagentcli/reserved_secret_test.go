package main

import (
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/workflow"
)

// httpapi-token is the HTTP API's bearer, and whoever writes it chooses which
// bearer the API accepts: the user's own `secret add|update|rm` write it, and
// an agent's shell, the chat and a workflow are refused (exit 3,
// operator_only). Their writes of any other entry go through (#845 security
// review).
func TestOnlyTheUserWritesTheHTTPAPIToken(t *testing.T) {
	account.SetEnforceFromForTest(t, time.Time{})
	keyring.MockInit()
	asOperatorProcess(t)
	home := t.TempDir()
	run := func(args ...string) (string, error) {
		t.Helper()
		out, _, err := runCLIIn(t, home, "", append([]string{"--profile", "default", "--json"}, args...)...)
		return out, err
	}
	if _, _, err := runCLIIn(t, home, "", "--json", "db", "migrate"); err != nil {
		t.Fatal(err)
	}
	if out, err := run("secret", "add", "--kind", "secret", "--name", "httpapi-token", "--field", "token=user-made"); err != nil {
		t.Fatalf("the user's secret add: %v\n%s", err, out)
	}
	for _, marker := range []string{"CLAUDECODE", browserctl.EnvAICaller, workflow.EnvWorkflowRun} {
		t.Setenv(marker, "1")
		other := "agent-" + strings.ToLower(marker)
		if out, err := run("secret", "add", "--kind", "secret", "--name", other, "--field", "secret=x"); err != nil {
			t.Errorf("%s set: another entry: %v\n%s", marker, err, out)
		}
		for _, args := range [][]string{
			{"secret", "add", "--kind", "secret", "--name", "httpapi-token", "--field", "token=agent-made"},
			{"secret", "update", "httpapi-token", "--field", "token=agent-made"},
			{"secret", "update", other, "--name", "httpapi-token"},
			{"secret", "rm", "httpapi-token"},
		} {
			out, err := run(args...)
			if exitCodeFor(err) != 3 || jsonCode(err) != "operator_only" {
				t.Errorf("%s set: %v: exit %d, code %q, %v\n%s", marker, args, exitCodeFor(err), jsonCode(err), err, out)
			}
		}
		t.Setenv(marker, "")
	}
	if out, err := run("secret", "update", "httpapi-token", "--field", "token=user-2"); err != nil {
		t.Errorf("the user's secret update: %v\n%s", err, out)
	}
	if out, err := run("secret", "rm", "httpapi-token"); err != nil {
		t.Errorf("the user's secret rm: %v\n%s", err, out)
	}
}
