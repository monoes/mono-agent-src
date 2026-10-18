// Package jevcli is `monoagentcli jev`: TypeSafe Jev's key, per-surface opt-in, usage,
// raw asks and models, and the retry triage it installs as workflow.RetryClassifier
// (retry.go; importing the package installs it). Everything the commands need from the
// CLI around them comes in through Deps, so they can be tested in isolation.
package jevcli

import (
	"database/sql"

	"github.com/monoes/mono-agent/internal/storage"
)

// Deps is what the commands take from the CLI around them.
type Deps struct {
	// JSONOutput reports the global --json flag; it is read when a command runs.
	JSONOutput func() bool
	// ProfileID is the profile in effect; read after OpenDB, which resolves --profile.
	ProfileID func() string
	// OpenDB opens (and migrates) the database and resolves the profile (the CLI's initDB).
	OpenDB func() (*storage.Database, error)
	// ResolveProfile turns a profile id or name into the id (the CLI's --profile resolution).
	ResolveProfile func(db *sql.DB, idOrName string) (string, error)
	// StdinIsTerminal decides whether `jev enable` may ask instead of requiring --yes.
	StdinIsTerminal func() bool
	// Refusal says why this process may not switch a surface on or set the key
	// (operator.Refusal): an agent, the chat or a workflow runs it. "" for the user.
	Refusal func() string
}
