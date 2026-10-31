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

// `task undepend` goes through the same gate as the person verbs (personGate): every
// non-operator context is refused with exit 3 / operator_only before the database is opened,
// even with arguments that would fail parsing.
func gateUndependVerbs() map[string][]string {
	return map[string][]string{
		"undepend":              {"undepend", "2", "1"},
		"undepend-no-arguments": {"undepend"},
	}
}

func gateUndependContexts() map[string]func(*testing.T) {
	return map[string]func(*testing.T){
		"agent":         asAgentProcess,
		"workflow":      func(t *testing.T) { asOperatorProcess(t); t.Setenv(workflow.EnvWorkflowRun, "exec-1") },
		"chat":          func(t *testing.T) { asOperatorProcess(t); t.Setenv(browserctl.EnvAICaller, browserctl.KindChat) },
		"mission-token": func(t *testing.T) { asOperatorProcess(t); t.Setenv(agenttokens.EnvMissionToken, "task-binding") },
		"session-token": func(t *testing.T) { asOperatorProcess(t); t.Setenv(coderhost.SessionTokenEnv, "session-token") },
	}
}

func TestUndependIsRefusedByTheProductionGate(t *testing.T) {
	for ctxName, setup := range gateUndependContexts() {
		for verb, args := range gateUndependVerbs() {
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

// The operator passes the gate: with too few arguments undepend answers its own argument error.
func TestUndependPassesTheGateForTheOperator(t *testing.T) {
	cfg := sendEnv(t)
	_, err := runSendCLI(t, cfg, newTaskCmd(cfg), "undepend", "2")
	if err == nil || !strings.Contains(err.Error(), "task undepend takes a task id") {
		t.Fatalf("the operator's undepend did not reach its argument check: %v", err)
	}
}
