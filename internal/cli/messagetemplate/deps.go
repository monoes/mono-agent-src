// Package messagetemplate is `monoagentcli message-template` (alias `template`): list, show,
// create and delete the message templates used for bulk messaging. Everything the commands
// need from the CLI around them comes in through Deps, so they can be tested in isolation.
package messagetemplate

import "github.com/monoes/mono-agent/internal/storage"

// Deps is what the commands take from the CLI around them.
type Deps struct {
	// JSONOutput reports the global --json flag; it is read when a command runs.
	JSONOutput func() bool
	// OpenDB opens (and migrates) the database the command is pointed at.
	OpenDB func() (*storage.Database, error)
}
