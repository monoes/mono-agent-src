package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	seedHomeDB(t, home)
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

	// The user's export, imported into a vault without these entries: the
	// user's own import writes the token, an agent's holds it back with a
	// note, imports the rest and counts it as skipped.
	exportTo := filepath.Join(t.TempDir(), "vault.enc")
	out, err := run("secret", "export", "--output", exportTo)
	var export struct{ Passphrase string }
	if err != nil || json.Unmarshal([]byte(out), &export) != nil || export.Passphrase == "" {
		t.Fatalf("the user's secret export: %v\n%s", err, out)
	}
	entries := []string{"httpapi-token", "agent-claudecode", "agent-monoagent_ai_caller", "agent-monoagent_workflow_run"}
	clear := func() {
		t.Helper()
		for _, name := range entries {
			if out, err := run("secret", "rm", name); err != nil {
				t.Fatalf("the user's secret rm %s: %v\n%s", name, err, out)
			}
		}
	}
	importAs := func(marker string) (counts struct{ Imported, Skipped int }, stderr string) {
		t.Helper()
		if marker != "" {
			t.Setenv(marker, "1")
			defer t.Setenv(marker, "")
		}
		out, stderr, err := importExport(t, home, exportTo, export.Passphrase)
		if err != nil || json.Unmarshal([]byte(out), &counts) != nil {
			t.Fatalf("secret import (%q set): %v\n%s", marker, err, out)
		}
		return counts, stderr
	}

	clear()
	if counts, _ := importAs(""); counts.Imported != 4 || counts.Skipped != 0 {
		t.Errorf("the user's import: %+v, want all four, the token too", counts)
	}
	if list, err := run("secret", "list"); err != nil || !strings.Contains(list, "httpapi-token") {
		t.Errorf("the vault after the user's import: %v\n%s", err, list)
	}
	clear()
	counts, stderr := importAs("CLAUDECODE")
	// The note starts a line of its own: the passphrase prompt's line ended though stdin echoed nothing.
	if counts.Imported != 3 || counts.Skipped != 1 || !strings.Contains(stderr, "Passphrase: \nnote: \"httpapi-token\" was not imported") {
		t.Errorf("an agent's import: %+v\n%s", counts, stderr)
	}
	if list, err := run("secret", "list"); err != nil || strings.Contains(list, "httpapi-token") {
		t.Errorf("the vault after the agent's import: %v\n%s", err, list)
	}

	// The user's own write without the field the server reads is warned of.
	_, stderr, err = runCLIIn(t, home, "", "--profile", "default", "--json", "secret", "add", "--kind", "secret", "--name", "httpapi-token", "--value", "x")
	if err != nil || !strings.Contains(stderr, "--field token=<value>") {
		t.Errorf("a token without its field: %v\n%s", err, stderr)
	}
}

// importExport runs `secret import file` with passphrase on the process's
// stdin, which the command reads, and returns its stdout and stderr.
func importExport(t *testing.T, home, file, passphrase string) (string, string, error) {
	t.Helper()
	in, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(inW, passphrase)
	inW.Close()
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prevIn, prevErr := os.Stdin, os.Stderr
	os.Stdin, os.Stderr = in, errW
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(errR)
		done <- string(b)
	}()
	out, cmdErr, runErr := runCLIIn(t, home, "", "--profile", "default", "--json", "secret", "import", file)
	os.Stdin, os.Stderr = prevIn, prevErr
	errW.Close()
	in.Close()
	return out, cmdErr + <-done, runErr
}
