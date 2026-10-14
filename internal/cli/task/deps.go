// Package task is `monoagentcli task`: the profile's task board for people and AI agents (the
// store is internal/tasks'). Everything the commands need from the CLI around them comes in
// through Deps, so they can be tested in isolation. `task os` (os.go) is the macOS Services
// menu.
package task

import (
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/storage"
)

// Deps is what the commands take from the CLI around them.
type Deps struct {
	// JSONOutput reports the global --json flag; it is read when a command runs.
	JSONOutput func() bool
	// ProfileID is the profile --profile names (an id or a name), else the active one once
	// OpenDB has resolved it; it is read when a command runs.
	ProfileID func() string
	// OpenDB opens (and migrates) the database the command is pointed at.
	OpenDB func() (*storage.Database, error)
	// NotFound builds the CLI's not-found error (exit code 2).
	NotFound func(format string, a ...interface{}) error
	// InvalidInput builds the CLI's invalid-input error (exit code 3).
	InvalidInput func(format string, a ...interface{}) error
	// WriteJSON writes the --json document.
	WriteJSON func(w io.Writer, v any) error
	// WithJSONErrors makes a command print its error as the CLI's --json error document.
	WithJSONErrors func(cmd *cobra.Command)
	// Getenv reads the environment that says who the caller is: MONOAGENT_ACTOR and the
	// agent-context markers of internal/orgsign.
	Getenv func(key string) string
	// StdinIsTerminal says whether `task agents allow` runs at a terminal, where it may ask.
	StdinIsTerminal func() bool
	// DBPath is --db-path as given: `task os` writes it, made absolute, into a menu.
	DBPath func() string
	// OpenDBActive opens (and migrates) the database without resolving --profile and returns
	// the active profile's id: `task os status` and `uninstall` take their profiles from the
	// menus, and some may be deleted.
	OpenDBActive func() (db *storage.Database, active string, err error)
}

// splitCSV splits a comma-separated list, dropping blank items (the CLI's splitCSV).
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
