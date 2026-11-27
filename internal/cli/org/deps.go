package org

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/orgenv"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/orggrant"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// Deps is what the org commands need from the CLI.
type Deps struct {
	// OpenDB opens the database and resolves the profile (the CLI's
	// initDB); it runs at most once per invocation, on first use.
	OpenDB func() (*storage.Database, error)
	// ProfileID is the profile in effect, read after OpenDB.
	ProfileID func() string
	// OpenWorkflowStore opens the CLI's workflow store.
	OpenWorkflowStore func(*storage.Database) *workflow.HybridWorkflowStore
	// JSONOutput is --json, read when a command runs.
	JSONOutput func() bool
	// NotFound and InvalidInput build the CLI's exit-code errors (2 and 3).
	NotFound     func(format string, a ...interface{}) error
	InvalidInput func(format string, a ...interface{}) error
	// Reported marks an error whose details the command already printed:
	// it sets the exit code, and --json adds no second {"error"} object.
	Reported func(error) error
	// StdinIsTerminal reports whether a person can answer on stdin.
	StdinIsTerminal func() bool
	// Serving marks a command that serves for as long as it runs (the
	// account guard's refresher), when the predicates say it does.
	Serving func(c *cobra.Command, when ...func(*cobra.Command) bool) *cobra.Command
	// CapabilityProbe reads the installed monomind's capabilities.
	CapabilityProbe func(*cobra.Command) (*monomind.CapabilitySet, error)
	// RequireOperator refuses whatever is not the user's own terminal (`org
	// pending --all`); what says what was refused. Unwired, it refuses.
	RequireOperator func(what string) error
}

// orgEnv is the environment every org command runs in: the org root and
// profile (orgenv.Env, shared by all the org commands of one invocation)
// and the CLI's Deps.
type orgEnv struct {
	*orgenv.Env
	d Deps
}

func newEnv(d Deps, e *orgenv.Env) *orgEnv { return &orgEnv{Env: e, d: d} }

// root is the org root for the proxy commands, which take it as a func. It is
// "" when there is none, so it may only be called from a command run through
// requireRoot; code that can run without that guard uses RootErr.
func (e *orgEnv) root() string {
	r, _ := e.RootErr()
	return r
}

// genOptions returns what reconcile needs to write generated org JSON
// blocks for this process.
func (e *orgEnv) genOptions(profileID string) orggrant.GenOptions { return e.GenOptions(profileID) }

// reconciler reconciles the org files of a profile with db's rows, as the
// daemon does at startup (no daemon watcher to tell about the saves).
func (e *orgEnv) reconciler(db *storage.Database) *orgenv.Reconciler {
	return &orgenv.Reconciler{DB: db, OpenWorkflowStore: e.OpenWorkflowStore, Logf: func(string, ...interface{}) {}}
}

// printJSONValue marshals v and prints it as one line.
func printJSONValue(v interface{}) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return printOrgJSON(b)
}

// expandPath expands ~ to the user's home directory (the CLI's expandPath).
func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

// orgenvFor is the orgenv.Env of d, with --project read through project.
func orgenvFor(d Deps, project func() string) *orgenv.Env {
	return orgenv.New(orgenv.Deps{
		OpenDB:            d.OpenDB,
		ProfileID:         d.ProfileID,
		OpenWorkflowStore: d.OpenWorkflowStore,
		Project:           project,
	})
}
