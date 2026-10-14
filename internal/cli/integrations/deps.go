// Package integrations is monoagent's integration with the coding-agent runtimes and API
// packages: `init` (monoagent's skills for Claude Code, Codex, Gemini CLI and opencode),
// `mcp` (the stdio MCP server), `integration` (declarative API packages), and the MCP
// registration doctor and setup make with every installed runtime (with --allow-mutations,
// the user's decision). Everything the commands need from the CLI around them comes in
// through Deps, so they can be tested in isolation.
package integrations

import (
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/mcp"
)

// Deps is what the commands take from the CLI around them.
type Deps struct {
	// JSONOutput reports the global --json flag; it is read when a command runs.
	JSONOutput func() bool
	// ProfileID is --profile as given ("" for the active profile); the MCP server resolves it.
	ProfileID func() string
	// DBPath is --db-path.
	DBPath func() string
	// Version is this binary's version.
	Version string
	// Serving marks a command that serves (the account gate lets it start and checks later).
	Serving func(cmd *cobra.Command) *cobra.Command
	// RunMCP serves the MCP server on stdin and stdout.
	RunMCP func(o mcp.Options) error
}
