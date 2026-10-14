// Package sociallist is `monoagentcli list`: create, show, fill and delete the active profile's
// social lists. Everything the commands need from the CLI around them comes in through Deps, so
// they can be tested in isolation.
package sociallist

import "github.com/monoes/mono-agent/internal/storage"

// Deps is what the commands take from the CLI around them.
type Deps struct {
	// JSONOutput reports the global --json flag; it is read when a command runs.
	JSONOutput func() bool
	// ProfileID is the profile the command runs in once OpenDB has resolved it; it is read when
	// a command runs.
	ProfileID func() string
	// OpenDB opens (and migrates) the database the command is pointed at.
	OpenDB func() (*storage.Database, error)
}
