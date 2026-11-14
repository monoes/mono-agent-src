package daemon

import (
	"context"
	"database/sql"
	"io"

	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/internal/autostart"
	"github.com/monoes/mono-agent/internal/cli/apiruntime"
	"github.com/monoes/mono-agent/internal/cli/capturehooks"
	"github.com/monoes/mono-agent/internal/extension"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// Deps are the CLI's pieces the daemon runs on.
type Deps struct {
	JSONOutput func() bool
	OpenDB     func() (*storage.Database, error)
	// BuildEngine builds the workflow engine for every profile (buildEngine(cfg, true)).
	BuildEngine       func() (*workflow.WorkflowEngine, func(), error)
	OpenWorkflowStore func(*storage.Database) *workflow.HybridWorkflowStore
	// API is what the OpenAI-compatible API runtime needs; its Version,
	// InvalidInput and AsCLIError serve the daemon too.
	API apiruntime.Deps

	// NewExtensionServer builds the bridge the daemon holds open, ProbeAddrs
	// lists where an already-running one may answer, and InstallCaptureHooks
	// wires the capture summaries into it.
	NewExtensionServer  func(zerolog.Logger) *extension.Server
	ProbeAddrs          func() []string
	InstallCaptureHooks func(*extension.Server, func(string, ...any)) *capturehooks.Hooks

	// StartAutoRevalidation starts the roster's automatic re-validation (off
	// unless turned on); the returned wait blocks until a run in flight ends.
	StartAutoRevalidation func(ctx context.Context, db *sql.DB, logf func(string, ...interface{})) (wait func())
	// NewInstaller is the OS service manager `daemon restart` goes through.
	NewInstaller func() autostart.Installer
	// Refusal says why this process is not the operator's own terminal ("" when it is):
	// the CLI's opgate.Refusal. Nil refuses (clicore.OperatorRefusal).
	Refusal   func() string
	WriteJSON func(io.Writer, any) error
}
