package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/agentroster"
	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/orgsign"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// runOut is runCLIIn with the stdout the commands print to os.Stdout captured.
func runOut(t *testing.T, home string, args ...string) (out string, err error) {
	t.Helper()
	out = captureStdout(t, func() { _, _, err = runCLIIn(t, home, "", args...) })
	return out, err
}

func purposeRows(t *testing.T, home string) []string {
	t.Helper()
	db, err := storage.NewDatabase(filepath.Join(home, ".monoagent", "monoagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.DB.Query(`SELECT name || ' ' || config_data FROM configs WHERE name LIKE 'agent_purposes.%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// Which models a purpose may use decides what runs on the user's accounts: `agent purposes
// set` and `reset` refuse an agent's shell, the chat and a workflow (exit 3, operator_only),
// every marker an agent can carry among them; the saved choice stays; `list` answers anyone;
// and the rows themselves are reserved, so `config set` and `config delete` of one is refused
// too and `config get` does not show it.
func TestAgentPurposesAreTheOperatorsOnly(t *testing.T) {
	account.SetEnforceFromForTest(t, time.Time{})
	asOperatorProcess(t)
	home := t.TempDir()
	if _, _, err := runCLIIn(t, home, "", "--json", "db", "migrate"); err != nil {
		t.Fatal(err)
	}
	db, err := storage.NewDatabase(filepath.Join(home, ".monoagent", "monoagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"opus", "haiku"} {
		if err := agentroster.Save(context.Background(), db.DB, agentroster.Result{Runtime: "claude", Model: m, Status: agentroster.StatusOK, ValidatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	if out, _, err := runCLIIn(t, home, "", "--json", "agent", "purposes", "set", "review", "--pair", "claude:opus"); err != nil {
		t.Fatalf("the user's set: %v\n%s", err, out)
	}
	rows := purposeRows(t, home)
	if len(rows) != 1 || !strings.Contains(rows[0], `"claude"`) {
		t.Fatalf("rows after the user's set: %v", rows)
	}

	markers := append([]string{"CLAUDECODE", browserctl.EnvAICaller, workflow.EnvWorkflowRun, "MONOAGENT_ACTOR"}, orgsign.AgentContextMarkers()...)
	for _, marker := range markers {
		t.Setenv(marker, "1")
		for _, args := range [][]string{
			{"agent", "purposes", "set", "review", "--pair", "claude:haiku"},
			{"agent", "purposes", "set", "coding", "--pair", "claude:haiku"},
			{"agent", "purposes", "reset", "review"},
			{"config", "set", "agent_purposes.default.review", `{"v":1,"pairs":[{"runtime":"claude","model":"haiku"}],"rule":"any","fallback":"any","budgetSharePct":0}`},
			{"config", "delete", "agent_purposes.default.review"},
		} {
			out, _, err := runCLIIn(t, home, "", append([]string{"--json"}, args...)...)
			if exitCodeFor(err) != 3 {
				t.Errorf("%s: %v: exit %d, %v\n%s", marker, args, exitCodeFor(err), err, out)
			}
			if args[1] == "purposes" && jsonCode(err) != "operator_only" {
				t.Errorf("%s: %v: code %q", marker, args, jsonCode(err))
			}
		}
		if out, err := runOut(t, home, "--json", "agent", "purposes", "list"); err != nil || !strings.Contains(out, `"purposes"`) || !strings.Contains(out, `"opus"`) {
			t.Errorf("%s: list: %v\n%s", marker, err, out)
		}
		if out, _ := runOut(t, home, "--json", "config", "list"); strings.Contains(out, "opus") || strings.Contains(out, "claude") {
			t.Errorf("%s: config list shows the values:\n%s", marker, out)
		}
		t.Setenv(marker, "")
	}
	// A master's turn: the chat's marker and the session token.
	t.Setenv(browserctl.EnvAICaller, "chat")
	t.Setenv(coderhost.SessionTokenEnv, "a-session-token")
	for _, args := range [][]string{{"agent", "purposes", "set", "review", "--pair", "claude:haiku"}, {"agent", "purposes", "reset", "review"}} {
		if _, _, err := runCLIIn(t, home, "", append([]string{"--json"}, args...)...); exitCodeFor(err) != 3 || jsonCode(err) != "operator_only" {
			t.Errorf("a master with a session token: %v: exit %d, code %q", args, exitCodeFor(err), jsonCode(err))
		}
	}
	t.Setenv(browserctl.EnvAICaller, "")
	t.Setenv(coderhost.SessionTokenEnv, "")
	if got := purposeRows(t, home); len(got) != 1 || got[0] != rows[0] {
		t.Errorf("the rows changed under the refusals: %v, was %v", got, rows)
	}

	// The user's reset works, and a pair that is not validated is refused as invalid input.
	if _, _, err := runCLIIn(t, home, "", "--json", "agent", "purposes", "set", "review", "--pair", "claude:ghost"); exitCodeFor(err) != 3 || jsonCode(err) == "operator_only" {
		t.Errorf("an unvalidated pair: exit %d, code %q, %v", exitCodeFor(err), jsonCode(err), err)
	}
	out, err := runOut(t, home, "--json", "agent", "purposes", "reset", "review")
	var v struct {
		ID        string `json:"id"`
		IsDefault bool   `json:"isDefault"`
	}
	if err != nil || json.Unmarshal([]byte(out), &v) != nil || v.ID != "review" || !v.IsDefault {
		t.Errorf("the user's reset: %v\n%s", err, out)
	}
}
