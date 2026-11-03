package main

import (
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/workflowrun"
)

// opRow is one operator-only verb that asks the shared gate (opgate). path is its place in the command
// tree (the key client_mode_test.go uses); build and args reach it from a test. recovery marks a verb that
// repairs an install: it must not lock the operator out when the database cannot be read. The guards in
// gate_operator_only_guard_test.go compare this table with the command tree and with the sources.
type opRow struct {
	path     string
	build    func(*globalConfig) *cobra.Command
	args     []string
	recovery bool
}

// operatorOnlyRows is the table of the verbs converted to the turn-aware gate so far (issue 1333). A
// later change that converts more verbs adds rows here and removes them from the pending lists of the
// guards.
func operatorOnlyRows() []opRow {
	return []opRow{
		{path: "api key create", build: newAPICmd, args: []string{"key", "create", "--name", "x"}},
		{path: "api key update", build: newAPICmd, args: []string{"key", "update", "k1", "--name", "y"}},
		{path: "api key revoke", build: newAPICmd, args: []string{"key", "revoke", "k1", "--yes"}},
		{path: "api config set", build: newAPICmd, args: []string{"config", "set", "--max-concurrent", "2", "--yes"}},
		{path: "api config unset", build: newAPICmd, args: []string{"config", "unset", "--all", "--yes"}},
		{path: "secret agent-access", build: newSecretCmd, args: []string{"agent-access", "strict"}},
		{path: "secret agent-access audit", build: newSecretCmd, args: []string{"agent-access", "audit", "--clear"}},
		{path: "secret rebind", build: newSecretCmd, args: []string{"rebind"}, recovery: true},
		{path: "secret export", build: newSecretCmd, args: []string{"export"}},
		{path: "connect get-oauth-client", build: newConnectCmd, args: []string{"get-oauth-client", "github", "--reveal"}},
		{path: "profile info get", build: newProfileCmd, args: []string{"info", "get", "--reveal"}},
		{path: "workflow run", build: newWorkflowRunWithFullOutputs, args: []string{"no-such-workflow", "--full-outputs"}},
	}
}

// newWorkflowRunWithFullOutputs is `workflow run` with the flag the real tree binds to workflowrun.FullOutputs
// (root.go): the run command alone does not carry it.
func newWorkflowRunWithFullOutputs(cfg *globalConfig) *cobra.Command {
	c := newWorkflowRunCmd(cfg)
	c.Flags().BoolVar(&workflowrun.FullOutputs, "full-outputs", false, "")
	return c
}
