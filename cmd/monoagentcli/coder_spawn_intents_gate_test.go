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

// `coder sessions spawn-intents list|resolve` go through the production gate (requireOperator behind
// requireMissionOperator), not a replacement: every non-operator context is refused with exit 3 /
// operator_only before the database is opened, and a plain operator terminal is not refused.
func spawnIntentsVerbs() map[string][]string {
	base := []string{"sessions", "spawn-intents"}
	return map[string][]string{
		"list":    append(append([]string{}, base...), "list"),
		"resolve": append(append([]string{}, base...), "resolve", "sp", "--keep", "--generation", "7", "--confirm", "resolve spawn intent"),
	}
}

func TestSpawnIntentsVerbsAreRefusedByTheProductionGate(t *testing.T) {
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
		for verb, args := range spawnIntentsVerbs() {
			t.Run(ctxName+"/"+verb, func(t *testing.T) {
				cfg := sendEnv(t)
				cfg.DBPath = filepath.Join(t.TempDir(), "never-opened.db")
				setup(t)
				_, err := runSendCLI(t, cfg, newCoderCmd(cfg), args...)
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

func TestSpawnIntentsListIsNotRefusedToAnOperator(t *testing.T) {
	cfg := sendEnv(t)
	out, err := runSendCLI(t, cfg, newCoderCmd(cfg), spawnIntentsVerbs()["list"]...)
	if err != nil {
		t.Fatalf("an operator's list was refused: %v\n%s", err, out)
	}
}
