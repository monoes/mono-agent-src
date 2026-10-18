package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/cli/workflowrun"
	"github.com/monoes/mono-agent/internal/workflow"
)

// Every command that prints a decrypted secret is the user's own: an agent's
// shell, the chat and a workflow are refused (exit 3) with nothing printed,
// while names, masked values and metadata stay open to them. The user's
// terminal and the desktop app (its exact argv) print them as before.
func TestCommandsThatPrintSecretsAreTheOperatorsOnly(t *testing.T) {
	account.SetEnforceFromForTest(t, time.Time{})
	keyring.MockInit()
	asOperatorProcess(t)
	home := t.TempDir()
	// What a command prints, to its own writers or straight to stdout.
	run := func(stdin string, args ...string) (string, error) {
		t.Helper()
		var out, errOut string
		var err error
		printed := captureStdout(t, func() { out, errOut, err = runCLIIn(t, home, stdin, args...) })
		return printed + out + errOut, err
	}
	for _, seed := range [][]string{
		{"--json", "db", "migrate"},
		{"--json", "secret", "add", "--kind", "secret", "--name", "vs", "--value", "vsecret-777"},
		{"--json", "profile", "info", "set", "passport_number", "P1234567"},
	} {
		if out, err := run("", seed...); err != nil {
			t.Fatalf("%v: %v\n%s", seed, err, out)
		}
	}
	if out, err := run("client-s3cret\n", "--json", "connect", "set-oauth-client", "google_sheets", "--client-id", "cid-1", "--client-secret-stdin"); err != nil {
		t.Fatalf("set-oauth-client: %v\n%s", err, out)
	}
	exportTo := filepath.Join(t.TempDir(), "vault.enc")

	gated := [][]string{
		{"--json", "secret", "reveal", "vs", "--reveal"},
		{"--json", "secret", "export", "--output", exportTo},
		{"--json", "connect", "get-oauth-client", "google_sheets", "--reveal"},
		{"--json", "profile", "info", "get", "--reveal"},
		{"--json", "node", "run", "vault.secret_get", "--config", `{"name":"vs"}`, "--output", "json"},
		{"--json", "workflow", "run", "wf-any", "--full-outputs"},
	}
	open := map[string][]string{
		"secret list":      {"--json", "secret", "list"},
		"secret get":       {"--json", "secret", "get", "vs"},
		"get-oauth-client": {"--json", "connect", "get-oauth-client", "google_sheets"},
		"profile info get": {"--json", "profile", "info", "get"},
	}
	for _, marker := range []string{"CLAUDECODE", browserctl.EnvAICaller, workflow.EnvWorkflowRun} {
		t.Setenv(marker, "1")
		for _, args := range gated {
			out, err := run("", args...)
			if exitCodeFor(err) != 3 || jsonCode(err) != "operator_only" || !strings.Contains(err.Error(), "prints decrypted secrets") {
				t.Errorf("%s set: %v: exit %d, code %q, %v", marker, args[1:], exitCodeFor(err), jsonCode(err), err)
			}
			for _, secret := range []string{"vsecret-777", "client-s3cret", "P1234567"} {
				if strings.Contains(out, secret) {
					t.Errorf("%s set: %v printed %s", marker, args[1:], secret)
				}
			}
		}
		if _, err := os.Stat(exportTo); err == nil {
			t.Errorf("%s set: the vault was exported", marker)
		}
		for name, args := range open {
			out, err := run("", args...)
			if err != nil {
				t.Errorf("%s set: %s: %v", marker, name, err)
			}
			if strings.Contains(out, "vsecret-777") || strings.Contains(out, "client-s3cret") || strings.Contains(out, "P1234567") {
				t.Errorf("%s set: %s printed a secret: %s", marker, name, out)
			}
		}
		t.Setenv(marker, "")
	}

	// The user's terminal, and the desktop app's own argv for revealing a
	// secret (wails-app GetSecretFields: --profile <id> --json secret
	// reveal <name> --reveal), print them.
	for want, args := range map[string][]string{
		"vsecret-777":   {"--profile", "default", "--json", "secret", "reveal", "vs", "--reveal"},
		"client-s3cret": {"--json", "connect", "get-oauth-client", "google_sheets", "--reveal"},
		"P1234567":      {"--json", "profile", "info", "get", "--reveal"},
	} {
		if out, err := run("", args...); err != nil || !strings.Contains(out, want) {
			t.Errorf("the operator's %v: %v\n%s", args, err, out)
		}
	}
	if out, err := run("", "--json", "node", "run", "vault.secret_get", "--config", `{"name":"vs"}`, "--output", "json"); err != nil || !strings.Contains(out, "vsecret-777") {
		t.Errorf("the operator's node run vault.secret_get: %v\n%s", err, out)
	}
	if out, err := run("", "--json", "secret", "export", "--output", exportTo); err != nil {
		t.Errorf("the operator's secret export: %v\n%s", err, out)
	} else if _, err := os.Stat(exportTo); err != nil {
		t.Errorf("the operator's export wrote nothing: %v", err)
	}
}

// `api key create` makes a key that signs in as the profile, `api key
// update` changes what it reaches and `api key revoke` cuts off whatever uses
// it: an agent's shell, the chat and a workflow are refused (exit 3,
// operator_only) and no key is made or changed, while the desktop app's own
// argv (wails-app: --profile <id> --json api key create --name=<name>, and
// update and revoke the same way) does it as the user.
func TestAPIKeyCreateUpdateAndRevokeAreTheOperatorsOnly(t *testing.T) {
	account.SetEnforceFromForTest(t, time.Time{})
	asOperatorProcess(t)
	home := t.TempDir()
	if _, _, err := runCLIIn(t, home, "", "--json", "db", "migrate"); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLIIn(t, home, "", "--profile", "default", "--json", "api", "key", "create", "--name=app-key")
	if err != nil || !strings.Contains(out, `"key"`) {
		t.Fatalf("the app's api key create: %v\n%s", err, out)
	}
	for _, marker := range []string{"CLAUDECODE", browserctl.EnvAICaller, workflow.EnvWorkflowRun} {
		t.Setenv(marker, "1")
		for _, args := range [][]string{{"create", "--name=agent-made"}, {"update", "app-key", "--context"}, {"revoke", "app-key", "--yes"}} {
			out, _, err := runCLIIn(t, home, "", append([]string{"--profile", "default", "--json", "api", "key"}, args...)...)
			if exitCodeFor(err) != 3 || jsonCode(err) != "operator_only" || strings.Contains(out, "agent-made") {
				t.Errorf("%s set: api key %v: exit %d, code %q, %v\n%s", marker, args, exitCodeFor(err), jsonCode(err), err, out)
			}
		}
		t.Setenv(marker, "")
	}
	list, _, err := runCLIIn(t, home, "", "--profile", "default", "--json", "api", "key", "list", "--include-revoked")
	if err != nil || strings.Contains(list, "agent-made") || !strings.Contains(list, "app-key") || !strings.Contains(list, `"context": false`) || !strings.Contains(list, `"revoked_at": null`) {
		t.Fatalf("keys after the refusals: %v\n%s", err, list)
	}
	for _, args := range [][]string{{"update", "app-key", "--context"}, {"revoke", "app-key", "--yes"}} {
		if out, _, err := runCLIIn(t, home, "", append([]string{"--profile", "default", "--json", "api", "key"}, args...)...); err != nil {
			t.Errorf("the app's api key %v: %v\n%s", args, err, out)
		}
	}
}

// The user's own `workflow run --full-outputs` shows a step's output
// unredacted, which the default --json masks under a credential-like key.
func TestTheOperatorsFullOutputsShowUnredactedOutput(t *testing.T) {
	account.SetEnforceFromForTest(t, time.Time{})
	keyring.MockInit()
	asOperatorProcess(t)
	// --full-outputs binds a package global, which a later test's own
	// newWorkflowCmd would otherwise inherit.
	t.Cleanup(func() { workflowrun.FullOutputs = false })
	home := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		var out, errOut string
		var err error
		printed := captureStdout(t, func() { out, errOut, err = runCLIIn(t, home, "", args...) })
		if err != nil {
			t.Fatalf("%v: %v\n%s%s%s", args, err, printed, out, errOut)
		}
		return printed + out
	}
	run("--json", "db", "migrate")
	var wf workflow.Workflow
	if err := json.Unmarshal([]byte(run("--json", "workflow", "create", "keys")), &wf); err != nil {
		t.Fatal(err)
	}
	node := func(typ, config string) string {
		var n workflow.WorkflowNode
		if err := json.Unmarshal([]byte(run("--json", "workflow", "node", "add", wf.ID, "--type", typ, "--name", typ, "--config", config)), &n); err != nil {
			t.Fatal(err)
		}
		return n.ID
	}
	trig := node("trigger.manual", `{}`)
	set := node("core.set", `{"assignments":[{"field":"api_key","value":"sk-live-123"}]}`)
	run("--json", "workflow", "connect", wf.ID, "--from", trig, "--to", set)
	run("--json", "workflow", "activate", wf.ID)

	if out := run("--json", "workflow", "run", wf.ID, "--timeout", "30s"); strings.Contains(out, "sk-live-123") {
		t.Fatalf("the default --json shows the value:\n%s", out)
	}
	if out := run("--json", "workflow", "run", wf.ID, "--timeout", "30s", "--full-outputs"); !strings.Contains(out, "sk-live-123") {
		t.Fatalf("the user's --full-outputs masks the value:\n%s", out)
	}
}
