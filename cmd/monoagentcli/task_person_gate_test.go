package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/workflow"
)

// `task session person inbox|approve|deny|ask` go through the production gate (taskDeps behind
// personGate: the token variables, callerFor(...).operator, the turn ancestry), not a replacement: every non-operator context is
// refused with exit 3 / operator_only before the database is opened, even with arguments that would
// fail parsing, and a plain operator terminal is not refused.
func personVerbs() map[string][]string {
	base := []string{"session", "person"}
	with := func(rest ...string) []string { return append(append([]string{}, base...), rest...) }
	return map[string][]string{
		"inbox":                     with("inbox"),
		"full":                      with("full", "1"),
		"full-without-arguments":    with("full"),
		"approve":                   with("approve", "1", "--rev", "1"),
		"deny":                      with("deny", "1", "--note", "x"),
		"ask":                       with("ask", "1", "--text", "x"),
		"answer-ask":                with("answer-ask", "--ask", "1", "--text", "x"),
		"approve-without-arguments": with("approve"),
	}
}

// operatorOnlyVerbs is every person verb and retire-worker: what scripts/tasks-mcp-smoke.sh asserts an agent is refused
// (exit 3, code operator_only), here in process through the real command tree.
func operatorOnlyVerbs() map[string][]string {
	m := personVerbs()
	m["retire-worker"] = []string{"session", "retire-worker", "1", "1"}
	m["retire-worker-without-arguments"] = []string{"session", "retire-worker"}
	return m
}

func TestPersonVerbsAreRefusedByTheProductionGate(t *testing.T) {
	contexts := map[string]func(*testing.T){
		"agent":    asAgentProcess,
		"workflow": func(t *testing.T) { asOperatorProcess(t); t.Setenv(workflow.EnvWorkflowRun, "exec-1") },
		"chat":     func(t *testing.T) { asOperatorProcess(t); t.Setenv(browserctl.EnvAICaller, browserctl.KindChat) },
		"mission-token": func(t *testing.T) {
			asOperatorProcess(t)
			t.Setenv(agenttokens.EnvMissionToken, "task-binding")
		},
		"session-token": func(t *testing.T) {
			asOperatorProcess(t)
			t.Setenv(coderhost.SessionTokenEnv, "session-token")
		},
	}
	for ctxName, setup := range contexts {
		for verb, args := range operatorOnlyVerbs() {
			t.Run(ctxName+"/"+verb, func(t *testing.T) {
				cfg := sendEnv(t)
				cfg.DBPath = filepath.Join(t.TempDir(), "never-opened.db")
				setup(t)
				_, err := runSendCLI(t, cfg, newTaskCmd(cfg), args...)
				var coded interface{ JSONErrorFields() map[string]any }
				if clicore.ExitCodeFor(err) != 3 || !errors.As(err, &coded) || coded.JSONErrorFields()["code"] != "operator_only" {
					t.Fatalf("%s %s was not refused as operator_only: %v", ctxName, verb, err)
				}
				if _, statErr := os.Stat(cfg.DBPath); statErr == nil {
					t.Errorf("the database was opened by a refused %s", ctxName)
				}
			})
		}
	}
}

func TestPersonInboxIsNotRefusedToAnOperator(t *testing.T) {
	cfg := sendEnv(t)
	out, err := runSendCLI(t, cfg, newTaskCmd(cfg), personVerbs()["inbox"]...)
	if err != nil {
		t.Fatalf("an operator's inbox was refused: %v\n%s", err, out)
	}
}

func TestRetireWorkerIsNotRefusedToAnOperator(t *testing.T) {
	cfg := sendEnv(t)
	_, err := runSendCLI(t, cfg, newTaskCmd(cfg), "session", "retire-worker", "1")
	var coded interface{ JSONErrorFields() map[string]any }
	if err == nil || (errors.As(err, &coded) && coded.JSONErrorFields()["code"] == "operator_only") {
		t.Fatalf("an operator should reach the argument check, not the operator gate: %v", err)
	}
}

// The smoke script's agent-shaped direction (--as bot, below a session, the agent working in no session) is refused with
// exit 3 and operator_only, before any session is made.
func TestAgentDirectionBelowASessionItDoesNotWorkInIsRefused(t *testing.T) {
	cfg := sendEnv(t)
	cfg.DBPath = filepath.Join(t.TempDir(), "never-opened.db")
	_, err := runSendCLI(t, cfg, newTaskCmd(cfg), "session", "new", "d", "--parent", "1", "--kind", "direction", "--as", "bot")
	var coded interface{ JSONErrorFields() map[string]any }
	if clicore.ExitCodeFor(err) != 3 || !errors.As(err, &coded) || coded.JSONErrorFields()["code"] != "operator_only" ||
		!strings.Contains(err.Error(), "works in no session") {
		t.Fatalf("an agent's direction was not refused as operator_only (works in no session): %v", err)
	}
}
