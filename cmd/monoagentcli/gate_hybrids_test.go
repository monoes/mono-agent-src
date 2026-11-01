package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/coderhost"
)

// The three hybrid verbs (session approve, approve, board) keep an agent path, so they are not
// wrapped whole in the person gate: only their operator branch is. Decided by the user: Q1 both
// (the coder-session token marks an agent in callerFor, and each operator branch is gated), Q2 yes
// (a real operator inside a coder turn is refused too).
func gateHybridVerbs() map[string][]string {
	return map[string][]string{
		"session approve": {"session", "approve", "1"},
		"approve":         {"approve", "1"},
		"board":           {"board"},
	}
}

// Every context that is not the operator's own terminal is refused with exit 3 / operator_only. The
// environment contexts that carry no agent identity to look up (workflow, chat) are refused before
// the database is opened, for all three verbs; session approve is refused before it in every context but a mission token (callerFor looks that up).
func TestHybridVerbsRefuseEveryNonOperatorContextOnTheOperatorBranch(t *testing.T) {
	for ctxName, setup := range gateOSMenuContexts() {
		for verb, args := range gateHybridVerbs() {
			t.Run(ctxName+"/"+verb, func(t *testing.T) {
				cfg := sendEnv(t)
				cfg.DBPath = filepath.Join(t.TempDir(), "never-opened.db")
				setup(t)
				_, err := runSendCLI(t, cfg, newTaskCmd(cfg), args...)
				requireOperatorOnly(t, err)
				mustNotOpen := ctxName == "workflow" || ctxName == "chat" || (verb == "session approve" && ctxName != "mission-token")
				if _, statErr := os.Stat(cfg.DBPath); statErr == nil && mustNotOpen {
					t.Errorf("the database was opened by a refused %s %s", ctxName, verb)
				}
			})
		}
	}
}

// The coder-session token alone (no agent marker, no --as, no actor) is enough to be refused: it is
// what a lead's process holds, and a lead is an agent that names itself.
func TestHybridVerbsRefuseAProcessHoldingOnlyTheSessionToken(t *testing.T) {
	for verb, args := range gateHybridVerbs() {
		t.Run(verb, func(t *testing.T) {
			cfg := sendEnv(t)
			t.Setenv(coderhost.SessionTokenEnv, "session-token")
			_, err := runSendCLI(t, cfg, newTaskCmd(cfg), args...)
			requireOperatorOnly(t, err)
			if !strings.Contains(err.Error(), coderhost.SessionTokenEnv) {
				t.Fatalf("the refusal does not name the token variable: %v", err)
			}
		})
	}
}

// A real operator in their own terminal passes the gate: the verb answers on its merits, never with
// operator_only.
func TestHybridVerbsPassTheGateForTheOperator(t *testing.T) {
	cfg := sendEnv(t)
	if out, err := runSendCLI(t, cfg, newTaskCmd(cfg), "board"); err != nil {
		t.Fatalf("the operator's board failed: %v\n%s", err, out)
	}
	for verb, args := range map[string][]string{
		"approve":         {"approve"},
		"session approve": {"session", "approve", "99"},
	} {
		_, err := runSendCLI(t, cfg, newTaskCmd(cfg), args...)
		var coded interface{ JSONErrorFields() map[string]any }
		if err == nil {
			t.Fatalf("%s: expected its own error", verb)
		}
		if errors.As(err, &coded) && coded.JSONErrorFields()["code"] == "operator_only" {
			t.Fatalf("the operator's %s was refused: %v", verb, err)
		}
	}
}

// A real operator INSIDE a coder turn is refused on the operator branch too (Q2); the wording is the
// person gate's, telling them to restart from their own terminal.
func TestHybridVerbsRefuseTheOperatorInsideACoderTurn(t *testing.T) {
	if coderhost.ProcStamp(os.Getpid()) == "" {
		t.Skip("process ancestry cannot be read here")
	}
	for verb, args := range gateHybridVerbs() {
		t.Run(verb, func(t *testing.T) {
			cfg := sendEnv(t)
			markThisProcessAsACoderTurn(t, cfg)
			_, err := runSendCLI(t, cfg, newTaskCmd(cfg), args...)
			requireOperatorOnly(t, err)
			for _, want := range []string{"coder session's turn", "run it in your own terminal"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("not refused by the turn ancestry (missing %q): %v", want, err)
				}
			}
		})
	}
}

// The other caller of the shared caller resolution outside the task package: a process holding only
// the session token is no longer the operator for the sandbox-fallback setter.
func TestRequireOperatorRefusesAProcessHoldingOnlyTheSessionToken(t *testing.T) {
	sendEnv(t)
	if err := requireOperator("do the thing"); err != nil {
		t.Fatalf("the operator was refused: %v", err)
	}
	t.Setenv(coderhost.SessionTokenEnv, "session-token")
	err := requireOperator("do the thing")
	if err == nil || clicore.ExitCodeFor(err) != 3 {
		t.Fatalf("a session-token holder passed requireOperator: %v", err)
	}
}
