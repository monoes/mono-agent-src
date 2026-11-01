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
	"github.com/monoes/mono-agent/internal/storage"
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

// A process inside a coder turn's process tree never acts as the person, even with a clean
// environment and the operator's identity: the ancestry layer of the shared gate covers undepend.
// A courtesy layer (SECURITY.md). The turn is this test process itself, recorded in the database
// the verb opens.
func TestUndependRefusesAProcessInsideACoderTurn(t *testing.T) {
	if coderhost.ProcStamp(os.Getpid()) == "" {
		t.Skip("process ancestry cannot be read here")
	}
	for verb, args := range gateUndependVerbs() {
		t.Run(verb, func(t *testing.T) {
			cfg := sendEnv(t)
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
			_, err = runSendCLI(t, cfg, newTaskCmd(cfg), args...)
			var coded interface{ JSONErrorFields() map[string]any }
			if clicore.ExitCodeFor(err) != 3 || !errors.As(err, &coded) || coded.JSONErrorFields()["code"] != "operator_only" {
				t.Fatalf("%s inside a coder turn was not refused as operator_only: %v", verb, err)
			}
			if !strings.Contains(err.Error(), "coder session's turn") {
				t.Fatalf("%s was refused by another layer, not the turn ancestry: %v", verb, err)
			}
		})
	}
}
