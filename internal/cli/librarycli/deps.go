// Package librarycli is `monoagentcli library`: the monoes.me library of workflows,
// orgs, web automations and API packages — log in, browse, install, publish and
// update. Everything the commands need from the CLI around them comes in through
// Deps, so they can be tested in isolation. (The package is not named library:
// it is built on internal/library, the monoes.me client.)
package librarycli

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/cli/workflowbundle"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// Deps is what the commands take from the CLI around them.
type Deps struct {
	// JSONOutput reports the global --json flag; it is read when a command runs.
	JSONOutput func() bool
	// ProfileID is the profile in effect; read after OpenDB, which resolves --profile.
	ProfileID func() string
	// OpenDB opens (and migrates) the database and resolves the profile (the CLI's initDB).
	OpenDB func() (*storage.Database, error)
	// OpenRegistry opens the automation registry.
	OpenRegistry func() (*automation.Registry, error)
	// OpenWorkflowStore opens the workflow store on db.
	OpenWorkflowStore func(db *storage.Database) *workflow.HybridWorkflowStore
	// WithJSONErrors makes a failing command also print {"error": …} with --json.
	WithJSONErrors func(cmd *cobra.Command)
	// AddSignIn adds the flags of `account login` to cmd and returns the sign-in they
	// ask for: the session's status, and sent when it only emailed a code. name is
	// the command, for the hints.
	AddSignIn func(cmd *cobra.Command, name string) func(cmd *cobra.Command) (st account.Status, sent bool, err error)
	// NewWorkflowImportJSONCmd is a fresh `workflow import` that prints --json, run in
	// process to install a library workflow.
	NewWorkflowImportJSONCmd func() *cobra.Command
	// Bundle bundles the automations a published workflow uses.
	Bundle workflowbundle.Deps
	// LoginRequired is the library's "log in to monoes.me first" (exit 4).
	LoginRequired func() error
	// IsLoginRequired reports whether err means "log in to monoes.me first".
	IsLoginRequired func(err error) bool
}

// splitCSV splits a comma-separated flag value, dropping empty items (a package
// copy of the CLI's).
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
