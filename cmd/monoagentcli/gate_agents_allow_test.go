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

// `task agents allow` and `task agents deny` go through the same gate as the person verbs
// (personGate): every non-operator context is refused with exit 3 / operator_only before the
// database is opened, even with arguments that would fail parsing.
func gateAgentsAllowVerbs() map[string][]string {
	return map[string][]string{
		"allow":              {"agents", "allow", "--view"},
		"allow-no-arguments": {"agents", "allow"},
		"deny":               {"agents", "deny", "--view"},
		"deny-no-arguments":  {"agents", "deny"},
	}
}

func gateAgentsAllowContexts() map[string]func(*testing.T) {
	return map[string]func(*testing.T){
		"agent":         asAgentProcess,
		"workflow":      func(t *testing.T) { asOperatorProcess(t); t.Setenv(workflow.EnvWorkflowRun, "exec-1") },
		"chat":          func(t *testing.T) { asOperatorProcess(t); t.Setenv(browserctl.EnvAICaller, browserctl.KindChat) },
		"mission-token": func(t *testing.T) { asOperatorProcess(t); t.Setenv(agenttokens.EnvMissionToken, "task-binding") },
		"session-token": func(t *testing.T) { asOperatorProcess(t); t.Setenv(coderhost.SessionTokenEnv, "session-token") },
	}
}

func TestAgentsAllowAndDenyAreRefusedByTheProductionGate(t *testing.T) {
	for ctxName, setup := range gateAgentsAllowContexts() {
		for verb, args := range gateAgentsAllowVerbs() {
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

// The operator passes the gate: allow gets as far as its own terminal check (the test's standard
// input is not a terminal), and deny runs.
func TestAgentsAllowAndDenyPassTheGateForTheOperator(t *testing.T) {
	cfg := sendEnv(t)
	_, err := runSendCLI(t, cfg, newTaskCmd(cfg), "agents", "allow", "--view")
	if err == nil || !strings.Contains(err.Error(), "needs a terminal on standard input") {
		t.Fatalf("the operator's allow did not reach its own terminal check: %v", err)
	}
	cfg = sendEnv(t)
	if out, err := runSendCLI(t, cfg, newTaskCmd(cfg), "agents", "deny", "--view"); err != nil {
		t.Fatalf("the operator's deny failed: %v\n%s", err, out)
	}
}
