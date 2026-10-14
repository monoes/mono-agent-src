// Package jevcli is `monoagentcli jev`: TypeSafe Jev's key, per-surface opt-in, usage,
// raw asks and models, and the retry triage it installs as workflow.RetryClassifier
// (retry.go; importing the package installs it). Everything the commands need from the
// CLI around them comes in through Deps, so they can be tested in isolation.
package jevcli

import "github.com/monoes/mono-agent/internal/storage"

// Deps is what the commands take from the CLI around them.
type Deps struct {
	// JSONOutput reports the global --json flag; it is read when a command runs.
	JSONOutput func() bool
	// ProfileID is the profile in effect; read after OpenDB, which resolves --profile.
	ProfileID func() string
	// OpenDB opens (and migrates) the database and resolves the profile (the CLI's initDB).
	OpenDB func() (*storage.Database, error)
	// StdinIsTerminal decides whether `jev enable` may ask instead of requiring --yes.
	StdinIsTerminal func() bool
}
