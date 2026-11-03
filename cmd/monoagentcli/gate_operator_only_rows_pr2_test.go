package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/opgate"
)

// operatorOnlyRowsPR2 are the identity and trust verbs converted in the second change of issue 1333:
// agent purposes, agent-mode, extension (bridge pairing and browser bindings) and jev. The args reach the
// gate of each verb before any other check, and the operator's pass reaches nothing outside the test's
// home (opEnv points the bridge probe at a closed port).
func operatorOnlyRowsPR2() []opRow {
	return []opRow{
		{path: "agent purposes set", build: newAgentCmd, args: []string{"purposes", "set", "review"}},
		{path: "agent purposes reset", build: newAgentCmd, args: []string{"purposes", "reset", "review"}},
		{path: "agent-mode env", build: newAgentModeCmd, args: []string{"env", "--token", "t"}},
		{path: "agent-mode token create", build: newAgentModeCmd, args: []string{"token", "create", "--name", "t", "--scope", "read"}},
		{path: "agent-mode token revoke", build: newAgentModeCmd, args: []string{"token", "revoke", "t"}},
		{path: "extension assign", build: newExtensionCmd, args: []string{"assign", "Work", "Edge"}},
		{path: "extension autopair", build: newExtensionCmd, args: []string{"autopair", "ask"}},
		{path: "extension approve", build: newExtensionCmd, args: []string{"approve", "abcd", "--code", "ABC-123"}},
		{path: "extension deny", build: newExtensionCmd, args: []string{"deny", "abcd"}},
		{path: "extension bind", build: newExtensionCmd, args: []string{"bind", "Edge", "Work"}},
		{path: "extension unbind", build: newExtensionCmd, args: []string{"unbind", "Edge"}},
		{path: "extension browsers allow", build: newExtensionCmd, args: []string{"browsers", "allow", "abcd1234", "--any"}},
		{path: "extension browsers revoke", build: newExtensionCmd, args: []string{"browsers", "revoke", "abcd1234"}},
		{path: "extension reset", build: newExtensionCmd, args: []string{"reset"}, recovery: true},
		{path: "extension pair", build: newExtensionCmd, args: []string{"pair", "--open"}},
		{path: "extension install", build: newExtensionCmd, args: []string{"install", "--zip", "{tmp}"}},
		{path: "jev enable", build: newJevCmd, args: []string{"enable", "hil", "--yes"}},
		{path: "jev key set", build: newJevCmd, args: []string{"key", "set"}},
		{path: "jev key adopt", build: newJevCmd, args: []string{"key", "adopt"}},
		{path: "jev key remove", build: newJevCmd, args: []string{"key", "remove"}},
	}
}

// The recovery verb of this change is `extension reset`: it revokes the pairing token, which is what an
// operator whose install is damaged (or whose token leaked) needs, and it reads no database. Its sibling
// `extension unbind` is not a recovery verb.
func TestExtensionResetIsTheRecoveryVerbOfThisChange(t *testing.T) {
	var recovery []string
	for _, r := range operatorOnlyRowsPR2() {
		if r.recovery {
			recovery = append(recovery, r.path)
		}
	}
	if len(recovery) != 1 || recovery[0] != "extension reset" {
		t.Fatalf("recovery verbs of the identity and trust change: %v, want [extension reset]", recovery)
	}
}

// Operator on a database that cannot be read: reset goes through, unbind (not a recovery verb) is refused
// because the process cannot be placed. A marked turn on a readable database is refused by both.
func TestExtensionResetOnAnUnreadableDatabaseAndInATurn(t *testing.T) {
	reset := opRow{path: "extension reset", build: newExtensionCmd, args: []string{"reset"}, recovery: true}
	unbind := opRow{path: "extension unbind", build: newExtensionCmd, args: []string{"unbind", "Edge"}}

	cfg := opEnv(t)
	cfg.DBPath = filepath.Join(t.TempDir(), "junk.db")
	if err := os.WriteFile(cfg.DBPath, []byte("this is not a database, and it is long enough to be read as a header, or not"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := reset.run(t, cfg); isOperatorOnly(err) {
		t.Fatalf("reset locked the operator out of an unreadable database: %v", err)
	}
	err := unbind.run(t, cfg)
	requireOperatorOnly(t, err)
	if !strings.Contains(err.Error(), "cannot tell whether a coder session's turn runs this command") {
		t.Fatalf("unbind on an unreadable database: %v", err)
	}

	needAncestry(t)
	cfg = opEnv(t)
	markThisProcessAsACoderTurn(t, cfg)
	for _, r := range []opRow{reset, unbind} {
		err := r.run(t, cfg)
		requireOperatorOnly(t, err)
		if !strings.Contains(err.Error(), "coder session's turn") {
			t.Fatalf("%s in a recorded turn: %v", r.path, err)
		}
	}
	if opgate.RecoveryRefusal() == "" {
		t.Fatal("the recovery gate let a process inside a recorded turn through")
	}
}
