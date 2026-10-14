// Package connect is `monoagentcli connect`: saved connections to external platforms (the store
// is internal/connections'). Everything the commands need from the CLI around them comes in
// through Deps, so they can be tested in isolation.
package connect

import (
	"io"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/connections"
	"github.com/monoes/mono-agent/internal/resources"
	"github.com/monoes/mono-agent/internal/storage"
)

// Deps is what the commands take from the CLI around them.
type Deps struct {
	// JSONOutput reports the global --json flag; it is read when a command runs.
	JSONOutput func() bool
	// ProfileID is the profile the command runs in once OpenDB has resolved it ("" means
	// "default"); it is read when a command runs.
	ProfileID func() string
	// OpenDB opens (and migrates) the database the command is pointed at.
	OpenDB func() (*storage.Database, error)
	// NotFound builds the CLI's not-found error (exit code 2).
	NotFound func(format string, a ...interface{}) error
	// InvalidInput builds the CLI's invalid-input error (exit code 3).
	InvalidInput func(format string, a ...interface{}) error
	// AuthFailed builds the CLI's auth/connection error (exit code 4).
	AuthFailed func(format string, a ...interface{}) error
	// WriteJSON writes the --json document.
	WriteJSON func(w io.Writer, v any) error
	// ReadSecretValue reads a secret from stdin up to the first newline (the CLI's
	// readSecretValue).
	ReadSecretValue func(r io.Reader) (string, error)
	// ScrubSecrets returns err with every secret value replaced (the CLI's scrubSecrets).
	ScrubSecrets func(err error, secrets ...string) error
	// ConnectionSecrets lists every string value in a connection's data (the CLI's
	// connectionSecrets).
	ConnectionSecrets func(c *connections.Connection) []string
	// ResourcesClient builds the provider client `connect resources` uses.
	ResourcesClient func() *resources.Client
}

// NewCommand returns the `connect` command group.
func NewCommand(d Deps) *cobra.Command { return d.newConnectCmd() }

// profileOrDefault is the profile the command runs in, "default" when none is set.
func (d Deps) profileOrDefault() string {
	if p := d.ProfileID(); p != "" {
		return p
	}
	return "default"
}
