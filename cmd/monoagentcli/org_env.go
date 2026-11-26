package main

import (
	"context"
	"sync"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/org"
	"github.com/monoes/mono-agent/internal/cli/orgenv"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/storage"
)

// The org environment and the org save helpers live in internal/cli/orgenv;
// these are the CLI's names for them.

// orgEnv is orgenv.Env on the CLI's config: initDB opens the database and
// resolves --profile.
type orgEnv struct {
	cfg         *globalConfig
	projectFlag string

	once sync.Once
	e    *orgenv.Env
}

func (e *orgEnv) env() *orgenv.Env {
	e.once.Do(func() {
		e.e = orgenv.New(orgenv.Deps{
			OpenDB:            func() (*storage.Database, error) { return initDB(e.cfg) },
			ProfileID:         func() string { return e.cfg.ProfileID },
			OpenWorkflowStore: openWorkflowStore,
			Project:           func() string { return e.projectFlag },
		})
	})
	return e.e
}

// newOrgCmd is `org` (internal/cli/org) on the CLI's config.
func newOrgCmd(cfg *globalConfig) *cobra.Command { return org.NewCommand(orgDeps(cfg)) }

// orgDeps is org.Deps on cfg.
func orgDeps(cfg *globalConfig) org.Deps {
	return org.Deps{
		OpenDB:            func() (*storage.Database, error) { return initDB(cfg) },
		ProfileID:         func() string { return cfg.ProfileID },
		OpenWorkflowStore: openWorkflowStore,
		JSONOutput:        func() bool { return cfg.JSONOutput },
		NotFound:          errNotFound,
		InvalidInput:      errInvalidInput,
		Reported:          func(err error) error { return reportedError{Err: err} },
		StdinIsTerminal:   stdinIsTerminal,
		Serving:           servingCommand,
		CapabilityProbe:   func(cmd *cobra.Command) (*monomind.CapabilitySet, error) { return capabilityProbe(cmd) },
		RequireOperator:   cfg.requireOperator,
	}
}

func (e *orgEnv) RootErr() (string, error) { return e.env().RootErr() }

func (e *orgEnv) Profile() (*storage.Database, string, string, error) { return e.env().Profile() }

func (e *orgEnv) Close() { e.env().Close() }

func selfExecutable() string { return orgenv.SelfExecutable() }

func orgAPIAddr(db *storage.Database) string { return orgenv.APIAddr(db) }

// The org helpers the library, the daemon and `status` use; the commands
// themselves are in internal/cli/org.

func statusDaemons(root string) map[string]interface{} { return org.StatusDaemons(root) }

func saveOrgDoc(ctx context.Context, root string, doc *orgdesign.Doc) (string, error) {
	return orgenv.SaveDoc(ctx, root, doc)
}
