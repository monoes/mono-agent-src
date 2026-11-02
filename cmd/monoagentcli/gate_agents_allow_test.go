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
	"github.com/monoes/mono-agent/internal/storage"
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

func atATerminal(t *testing.T) {
	t.Helper()
	old := agentsAllowStdinIsTerminal
	t.Cleanup(func() { agentsAllowStdinIsTerminal = old })
	agentsAllowStdinIsTerminal = func() bool { return true }
}

// With a terminal on standard input the old terminal rule would let these callers through, so only
// the gate can refuse them: a revert of the routing makes this fail.
func TestAgentsAllowIsRefusedByTheGateEvenAtATerminal(t *testing.T) {
	for _, ctxName := range []string{"agent", "session-token", "mission-token"} {
		t.Run(ctxName, func(t *testing.T) {
			cfg := sendEnv(t)
			cfg.DBPath = filepath.Join(t.TempDir(), "never-opened.db")
			gateAgentsAllowContexts()[ctxName](t)
			atATerminal(t)
			_, err := runSendCLI(t, cfg, newTaskCmd(cfg), "agents", "allow", "--view")
			var coded interface{ JSONErrorFields() map[string]any }
			if clicore.ExitCodeFor(err) != 3 || !errors.As(err, &coded) || coded.JSONErrorFields()["code"] != "operator_only" {
				t.Fatalf("%s was not refused as operator_only: %v", ctxName, err)
			}
			if _, statErr := os.Stat(cfg.DBPath); statErr == nil {
				t.Errorf("the database was opened by a refused %s", ctxName)
			}
		})
	}
}

// A process inside a coder turn's process tree is refused by allow (at a terminal) and by deny.
func TestAgentsAllowAndDenyRefuseAProcessInsideACoderTurn(t *testing.T) {
	if coderhost.ProcStamp(os.Getpid()) == "" {
		t.Skip("process ancestry cannot be read here")
	}
	cfg := sendEnv(t)
	atATerminal(t)
	d, err := storage.NewDatabase(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coderhost.NewStore(d.DB).EnsureSession("default", "conv-lead"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`UPDATE coder_sessions SET turn_pid = ?, turn_stamp = ? WHERE conversation_id = 'conv-lead'`, os.Getpid(), coderhost.ProcStamp(os.Getpid())); err != nil {
		t.Fatal(err)
	}
	d.Close()
	for _, args := range [][]string{{"agents", "allow", "--view"}, {"agents", "deny", "--view"}} {
		_, err := runSendCLI(t, cfg, newTaskCmd(cfg), args...)
		var coded interface{ JSONErrorFields() map[string]any }
		if clicore.ExitCodeFor(err) != 3 || !errors.As(err, &coded) || coded.JSONErrorFields()["code"] != "operator_only" {
			t.Errorf("%v inside a turn was not refused as operator_only: %v", args, err)
		}
	}
}

// The operator passes the gate: allow gets as far as its own terminal check (the test's standard
// input is not a terminal; exit 3 / operator_only from the terminal rule, not the gate), and deny runs.
func TestAgentsAllowAndDenyPassTheGateForTheOperator(t *testing.T) {
	cfg := sendEnv(t)
	_, err := runSendCLI(t, cfg, newTaskCmd(cfg), "agents", "allow", "--view")
	if clicore.ExitCodeFor(err) != 3 {
		t.Fatalf("the operator's allow did not reach its own terminal check: %v", err)
	}
	cfg = sendEnv(t)
	atATerminal(t)
	if out, err := runSendCLI(t, cfg, newTaskCmd(cfg), "agents", "allow", "--view"); err != nil {
		t.Fatalf("the operator's allow at a terminal failed: %v\n%s", err, out)
	}
	cfg = sendEnv(t)
	if out, err := runSendCLI(t, cfg, newTaskCmd(cfg), "agents", "deny", "--view"); err != nil {
		t.Fatalf("the operator's deny failed: %v\n%s", err, out)
	}
}
