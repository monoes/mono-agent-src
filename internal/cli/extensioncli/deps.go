// Package extensioncli is `monoagentcli extension` (alias `bridge`): running the MonoAgent
// Bridge in the foreground, its status, pairing, the browsers it sees and their profile
// bindings, installing the extension, plus the bridge helpers every process that owns or
// relays through a bridge shares (the server as it is wired, finding a running bridge, the
// capture summaries). Everything the commands need from the CLI around them comes in through
// Deps.
package extensioncli

import (
	"context"
	"database/sql"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/extension"
	"github.com/monoes/mono-agent/internal/storage"
)

// Deps is what the commands take from the CLI around them.
type Deps struct {
	// JSONOutput reports the global --json flag; it is read when a command runs.
	JSONOutput func() bool
	// OpenDB opens (and migrates) the database the command is pointed at.
	OpenDB func() (*storage.Database, error)
	// ResolveProfileID turns a profile id or name into its id (exit 3 when there is none).
	ResolveProfileID func(db *sql.DB, idOrName string) (string, error)
	// Version is this build, reported by the bridge and used to pick the extension release.
	Version func() string
	// ServingCommand marks a long-running command that serves (the account's serving guard).
	ServingCommand func(c *cobra.Command) *cobra.Command
	// Refusal says why this process is not the operator's own terminal (""
	// when it is; operator.Refusal). What changes which browser or profile
	// the extension acts as, which bridge it pairs with, or what code it
	// runs is the user's: bind, unbind, assign, reset, pair --open and install
	// --zip. Nil refuses (clicore.OperatorRefusal).
	Refusal func() string
	// StdinIsTerminal says whether a person can type at the command (nil:
	// clicore.StdinIsTerminal). `extension approve` asks for its code there.
	StdinIsTerminal func() bool
}

// operatorOnly refuses what only the user does, naming what and why.
func (cfg *Deps) operatorOnly(what string) error {
	if why := clicore.OperatorRefusal(cfg.Refusal); why != "" {
		return clicore.OperatorOnly("%s, which only the user does: %s; run it yourself in your terminal, or use the app or the extension's side panel", what, why)
	}
	return nil
}

// connChecker is the minimal slice of browser.ExtensionBridge the hints need.
type connChecker interface {
	IsConnected() bool
}

// addrProvider is implemented by the bridge that owns the extension server.
type addrProvider interface {
	Addr() (string, bool)
}

// NewCommand is `monoagentcli extension`.
func NewCommand(d Deps) *cobra.Command { return newExtensionCmd(&d) }

// BridgeAdvice says how to keep a bridge running (appended to "not connected" errors).
const BridgeAdvice = bridgeAdvice

// BridgeLifetimeHint is BridgeAdvice, but only for a bridge this process owns.
func BridgeLifetimeHint(bridge interface{ IsConnected() bool }) string {
	return bridgeLifetimeHint(bridge)
}

// NewServer builds the extension server every process that owns one runs, reporting version.
func NewServer(logger zerolog.Logger, version string) *extension.Server {
	return newExtensionServer(logger, version)
}

// FindRunningBridge is the status and base URL of a bridge that is already running, if any.
func FindRunningBridge(ctx context.Context) (extension.Status, string, bool) {
	return findRunningBridgeCtx(ctx)
}

// BridgeAddr is the address a running bridge answers on.
func BridgeAddr(st extension.Status, base string) string { return bridgeAddr(st, base) }

// CaptureHooks are the capture summaries and indexing installed into a server.
type CaptureHooks = captureHooks

// InstallCaptureSummaries wires the capture summaries and indexing into srv.
func InstallCaptureSummaries(srv *extension.Server, logf func(string, ...any)) *CaptureHooks {
	return installCaptureSummaries(srv, logf)
}

// CheckSummaryChoice validates a capture's summary mode, runtime and model.
func CheckSummaryChoice(mode, runtime, model string) error {
	return checkSummaryChoice(mode, runtime, model)
}
