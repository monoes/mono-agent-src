package main

import (
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/workflow"
)

// What widens what the OpenAI-compatible API reaches, or where Jev sends a
// profile's data, behind no more than a --yes is the user's own: `api config
// set|unset`, `jev enable` and `jev key set|adopt|remove` refuse an agent's
// shell, the chat and a workflow (exit 3, operator_only), and the user's key
// stays; --dry-run stays open (#845 security review).
func TestAPISettingsAndJevAreTheOperatorsOnly(t *testing.T) {
	account.SetEnforceFromForTest(t, time.Time{})
	asOperatorProcess(t)
	home := t.TempDir()
	if _, _, err := runCLIIn(t, home, "", "--json", "db", "migrate"); err != nil {
		t.Fatal(err)
	}
	if out, _, err := runCLIIn(t, home, "user-key\n", "--json", "jev", "key", "set"); err != nil {
		t.Fatalf("the user's jev key set: %v\n%s", err, out)
	}
	for _, marker := range []string{"CLAUDECODE", browserctl.EnvAICaller, workflow.EnvWorkflowRun} {
		t.Setenv(marker, "1")
		for _, c := range []struct {
			stdin string
			args  []string
		}{
			{"", []string{"api", "config", "set", "--confinement", "any", "--yes"}},
			{"", []string{"api", "config", "unset", "--all", "--yes"}},
			{"", []string{"--profile", "default", "jev", "enable", "api_auto", "--yes"}},
			{"agent-key\n", []string{"jev", "key", "set"}},
			{"", []string{"jev", "key", "adopt"}},
			{"", []string{"jev", "key", "remove"}},
		} {
			out, _, err := runCLIIn(t, home, c.stdin, append([]string{"--json"}, c.args...)...)
			if exitCodeFor(err) != 3 || jsonCode(err) != "operator_only" {
				t.Errorf("%s set: %v: exit %d, code %q, %v\n%s", marker, c.args, exitCodeFor(err), jsonCode(err), err, out)
			}
		}
		if out, _, err := runCLIIn(t, home, "", "--json", "api", "config", "set", "--confinement", "any", "--dry-run"); err != nil {
			t.Errorf("%s set: the dry run: %v\n%s", marker, err, out)
		}
		t.Setenv(marker, "")
	}
	show, _, err := runCLIIn(t, home, "", "--json", "api", "config", "show")
	if err != nil || strings.Contains(show, `"saved": "any"`) {
		t.Fatalf("the saved settings after the refusals: %v\n%s", err, show)
	}
	if out, _, err := runCLIIn(t, home, "", "--json", "api", "config", "set", "--confinement", "any", "--yes"); err != nil {
		t.Errorf("the user's api config set: %v\n%s", err, out)
	}
	// The user's key outlived the refusals: their own remove finds it.
	if out, _, err := runCLIIn(t, home, "", "--json", "jev", "key", "remove"); err != nil {
		t.Errorf("the user's jev key remove: %v\n%s", err, out)
	}
}
