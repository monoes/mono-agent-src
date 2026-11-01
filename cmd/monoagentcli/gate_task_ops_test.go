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

// The operator-only task verbs below go through the same gate as the person verbs
// (personGate): every non-operator context is refused with exit 3 / operator_only
// before the database is opened, even with arguments that would fail parsing.
func gateTaskOpsVerbs() map[string][]string {
	return map[string][]string{
		"edit":              {"edit", "1", "--title", "x"},
		"edit-no-arguments": {"edit"},
		"move":              {"move", "1", "ready"},
		"move-no-arguments": {"move"},
		"archive":           {"archive", "1"},
		"archive-no-args":   {"archive"},
		"unarchive":         {"unarchive", "1"},
		"unarchive-no-args": {"unarchive"},
		"add-ready":         {"add", "--ready", "Fix the thing"},
		"add-ready-stdin":   {"add", "--ready", "--stdin"},
	}
}

func gateTaskOpsContexts() map[string]func(*testing.T) {
	return map[string]func(*testing.T){
		"agent":         asAgentProcess,
		"workflow":      func(t *testing.T) { asOperatorProcess(t); t.Setenv(workflow.EnvWorkflowRun, "exec-1") },
		"chat":          func(t *testing.T) { asOperatorProcess(t); t.Setenv(browserctl.EnvAICaller, browserctl.KindChat) },
		"mission-token": func(t *testing.T) { asOperatorProcess(t); t.Setenv(agenttokens.EnvMissionToken, "task-binding") },
		"session-token": func(t *testing.T) { asOperatorProcess(t); t.Setenv(coderhost.SessionTokenEnv, "session-token") },
	}
}

func TestOperatorTaskOpsAreRefusedByTheProductionGate(t *testing.T) {
	for ctxName, setup := range gateTaskOpsContexts() {
		for verb, args := range gateTaskOpsVerbs() {
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

// The operator's own terminal passes the gate: the verb runs, or answers its own argument error.
func TestOperatorTaskOpsPassTheGateForTheOperator(t *testing.T) {
	notGate := func(t *testing.T, err error) {
		t.Helper()
		var coded interface{ JSONErrorFields() map[string]any }
		if errors.As(err, &coded) && coded.JSONErrorFields()["code"] == "operator_only" {
			t.Fatalf("the operator was refused: %v", err)
		}
	}
	for verb, args := range map[string][]string{
		"edit-no-arguments":      {"edit"},
		"move-no-arguments":      {"move"},
		"archive-no-arguments":   {"archive"},
		"unarchive-no-arguments": {"unarchive"},
	} {
		t.Run(verb, func(t *testing.T) {
			cfg := sendEnv(t)
			_, err := runSendCLI(t, cfg, newTaskCmd(cfg), args...)
			if err == nil {
				t.Fatalf("%s without arguments succeeded", verb)
			}
			notGate(t, err)
		})
	}
	t.Run("add-ready", func(t *testing.T) {
		cfg := sendEnv(t)
		if out, err := runSendCLI(t, cfg, newTaskCmd(cfg), "add", "--ready", "Fix the thing"); err != nil {
			t.Fatalf("the operator's add --ready failed: %v\n%s", err, out)
		}
	})
}

// A plain add stays open to an agent: only the --ready branch is gated.
func TestPlainTaskAddIsStillAllowedToAnAgent(t *testing.T) {
	cfg := sendEnv(t)
	asAgentProcess(t)
	if out, err := runSendCLI(t, cfg, newTaskCmd(cfg), "add", "--as", "bot", "Look at this"); err != nil {
		t.Fatalf("an agent's plain add was refused: %v\n%s", err, out)
	}
}
