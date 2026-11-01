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

// `task os install` and `task os uninstall` go through the same gate as the person verbs
// (personGate): every non-operator context is refused with exit 3 / operator_only, on every
// platform and before the database is opened, even with arguments that would fail parsing.
func gateOSMenuVerbs() map[string][]string {
	return map[string][]string{
		"install":              {"os", "install"},
		"install-bad-argument": {"os", "install", "junk"},
		"uninstall":            {"os", "uninstall"},
		"uninstall-bad-arg":    {"os", "uninstall", "junk"},
	}
}

func gateOSMenuContexts() map[string]func(*testing.T) {
	return map[string]func(*testing.T){
		"agent":         asAgentProcess,
		"workflow":      func(t *testing.T) { asOperatorProcess(t); t.Setenv(workflow.EnvWorkflowRun, "exec-1") },
		"chat":          func(t *testing.T) { asOperatorProcess(t); t.Setenv(browserctl.EnvAICaller, browserctl.KindChat) },
		"mission-token": func(t *testing.T) { asOperatorProcess(t); t.Setenv(agenttokens.EnvMissionToken, "task-binding") },
		"session-token": func(t *testing.T) { asOperatorProcess(t); t.Setenv(coderhost.SessionTokenEnv, "session-token") },
	}
}

func TestOSMenuInstallAndUninstallAreRefusedByTheProductionGate(t *testing.T) {
	for ctxName, setup := range gateOSMenuContexts() {
		for verb, args := range gateOSMenuVerbs() {
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

// The operator passes the gate: with an argument the verb takes none of, it answers its own
// argument error (invalid_input), and nothing is installed or removed.
func TestOSMenuVerbsPassTheGateForTheOperator(t *testing.T) {
	for _, sub := range []string{"install", "uninstall"} {
		t.Run(sub, func(t *testing.T) {
			cfg := sendEnv(t)
			_, err := runSendCLI(t, cfg, newTaskCmd(cfg), "os", sub, "junk")
			if err == nil || !strings.Contains(err.Error(), "takes no arguments") {
				t.Fatalf("the operator's os %s did not reach its argument check: %v", sub, err)
			}
		})
	}
}

// A process inside a coder turn's process tree never acts as the person, even with a clean
// environment and the operator's identity: the ancestry layer of the shared gate covers os install and os uninstall.
// A courtesy layer (SECURITY.md). The turn is this test process itself, recorded in the database
// the verb opens.
func TestOSMenuVerbsRefuseAProcessInsideACoderTurn(t *testing.T) {
	if coderhost.ProcStamp(os.Getpid()) == "" {
		t.Skip("process ancestry cannot be read here")
	}
	for verb, args := range gateOSMenuVerbs() {
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
