// Package configcli is `monoagentcli config`: list, get, set and delete the key-value entries
// of the database's configs table. Everything the commands need from the CLI around them comes
// in through Deps, so they can be tested in isolation.
package configcli

import "github.com/monoes/mono-agent/internal/storage"

// Deps is what the commands take from the CLI around them.
type Deps struct {
	// JSONOutput reports the global --json flag; it is read when a command runs.
	JSONOutput func() bool
	// OpenDB opens (and migrates) the database the command is pointed at.
	OpenDB func() (*storage.Database, error)
}
