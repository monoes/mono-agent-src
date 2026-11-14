package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/release"
)

// Class C of issue 1475, as the owner decided it: the verbs of operatorOnlyRowsPR6 are refused in every
// context that is not the operator's own terminal, and the verbs left open stay open.

// Each flipped verb is refused in each agent context, before the database is read.
func TestClassCFlippedVerbsAreRefusedInEveryAgentContext(t *testing.T) {
	for ctxName, setup := range gateOSMenuContexts() {
		for _, r := range operatorOnlyRowsPR6() {
			t.Run(ctxName+"/"+r.path, func(t *testing.T) {
				cfg := opEnv(t)
				cfg.DBPath = filepath.Join(t.TempDir(), "never.db")
				setup(t)
				requireOperatorOnly(t, r.run(t, cfg))
			})
		}
	}
}

// `update --check` only reports: it is open to an agent. It must not answer operator_only in any context;
// the endpoint is a closed loopback port, so the answer is a network error.
func TestClassCUpdateCheckStaysOpenToAgents(t *testing.T) {
	was, wasClient := latestReleaseURL, releaseClient
	latestReleaseURL = "http://127.0.0.1:9/latest"
	releaseClient = func() *release.Client { return &release.Client{} }
	t.Cleanup(func() { latestReleaseURL, releaseClient = was, wasClient })
	for ctxName, setup := range gateOSMenuContexts() {
		t.Run(ctxName, func(t *testing.T) {
			cfg := opEnv(t)
			setup(t)
			_, err := runSendCLI(t, cfg, newUpdateCmd(cfg), "--check")
			if isOperatorOnly(err) {
				t.Fatalf("update --check was refused in the %s context: %v", ctxName, err)
			}
		})
	}
}

// The verbs the owner decided to leave open do not carry the operator-only help text, and the families
// with a flipped verb say so in the guard table. A leave-open verb that is flipped later must change this
// list on purpose.
func TestClassCOpenVerbsStayOpen(t *testing.T) {
	open := []string{
		"db migrate", "init", "httpapi", "mcp", "daemon", "release verify", "account status",
		"account login", "library install", "library update", "integration import-openapi",
	}
	byKey := map[string]string{}
	for _, c := range allCommands(newRootCmd()) {
		byKey[commandKey(c)] = c.Long
	}
	for _, k := range open {
		long, ok := byKey[k]
		if !ok {
			t.Errorf("%s: not a command of the tree", k)
			continue
		}
		if strings.Contains(long, clicore.OperatorOnlyHelp) {
			t.Errorf("%s carries the operator-only help text, but class C left it open (issue 1475)", k)
		}
		for _, r := range operatorOnlyRows() {
			if r.path == k {
				t.Errorf("%s is a row of operatorOnlyRows, but class C left it open (issue 1475)", k)
			}
		}
	}
}
