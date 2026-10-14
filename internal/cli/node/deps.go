// Package node is `monoagentcli node`: list the node types, print a type's schema, run one node
// on its own, and turn a curl command into an http.request config. Everything the commands need
// from the CLI around them comes in through Deps, so they can be tested in isolation. `node
// palette` stays in the CLI, which adds it to the group NewCommand returns.
package node

import (
	"context"
	"database/sql"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"

	browserpkg "github.com/monoes/mono-agent/internal/browser"
	"github.com/monoes/mono-agent/internal/connections"
	"github.com/monoes/mono-agent/internal/nodes"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// Deps is what the commands take from the CLI around them.
type Deps struct {
	// JSONOutput and Verbose report the global --json and --verbose flags; they are read when
	// a command runs.
	JSONOutput func() bool
	Verbose    func() bool
	// ProfileID is the profile --profile names, resolved to its id once OpenDB has run; it is
	// read when a command runs.
	ProfileID func() string
	// OpenDB opens (and migrates) the database the command is pointed at, and resolves the
	// profile ProfileID reports.
	OpenDB func() (*storage.Database, error)
	// BuildRegistry builds the registry of every node type; db (nil when the database did not
	// open) adds the AI nodes.
	BuildRegistry func(db *sql.DB) *workflow.NodeTypeRegistry
	// ResolveCredential returns the credential data of a connection, by id or platform name,
	// in profileID, refreshing an OAuth token that is about to expire.
	ResolveCredential func(ctx context.Context, store *connections.Store, credentialOrPlatform, profileID string) (map[string]interface{}, error)
	// ConnectBridge returns the extension bridge narrowed to profileID's browser once it is
	// connected (launching the user's browser and waiting when it is not), or why it is not.
	ConnectBridge func(profileID string, logger zerolog.Logger) (browserpkg.ExtensionBridge, error)
	// BotRegistry hands a browser node the platform bots compiled into the CLI.
	BotRegistry nodes.BotRegistry
	// ConfigDir is the directory of the selector configs (~/.monoagent/configs).
	ConfigDir func() string
	// NotFound and InvalidInput build the CLI's errors (exit codes 2 and 3).
	NotFound     func(format string, a ...interface{}) error
	InvalidInput func(format string, a ...interface{}) error
}

// NewRunCommand returns `node run` on its own.
func NewRunCommand(d Deps) *cobra.Command { return d.newNodeRunCmd() }

// NeedsBrowserSession reports whether a node type drives the browser and so needs the
// extension session provider.
func NeedsBrowserSession(nodeType string, factory workflow.NodeFactory) bool {
	return needsBrowserSession(nodeType, factory)
}

// TypeCategory is the category `node list` shows for a node type: the prefix before the first
// dot, else Category's guess.
func TypeCategory(t string) string { return nodeTypeCategory(t) }

// Category infers a display category from a node type string.
func Category(t string) string { return nodeCategory(t) }
