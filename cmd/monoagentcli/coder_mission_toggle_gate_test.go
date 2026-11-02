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

// `coder sessions settings set CONV --mission on|off` goes through the production gate: a model (an agent
// process, a workflow run, a chat, a mission or session token) can never turn mission mode on or off. Every
// such context is refused with exit 3 / operator_only before the database is opened.
func TestMissionToggleIsRefusedByTheProductionGate(t *testing.T) {
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
		for _, state := range []string{"on", "off"} {
			t.Run(ctxName+"/"+state, func(t *testing.T) {
				cfg := sendEnv(t)
				cfg.DBPath = filepath.Join(t.TempDir(), "never-opened.db")
				setup(t)
				_, err := runSendCLI(t, cfg, newCoderCmd(cfg), "sessions", "settings", "set", "conv", "--mission", state)
				var coded interface{ JSONErrorFields() map[string]any }
				if clicore.ExitCodeFor(err) != 3 || !errors.As(err, &coded) || coded.JSONErrorFields()["code"] != "operator_only" {
					t.Fatalf("%s --mission %s was not refused as operator_only: %v", ctxName, state, err)
				}
				if _, statErr := os.Stat(cfg.DBPath); statErr == nil {
					t.Errorf("the database was opened by a refused %s", ctxName)
				}
			})
		}
	}
}
