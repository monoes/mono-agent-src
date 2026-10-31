package main

import (
	"errors"
	"os"
	"path/filepath"
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
		for verb, args := range personVerbs() {
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
