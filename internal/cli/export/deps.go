// Package export is `monoagentcli export`: the profile's people written to JSON files.
// Everything the command needs from the CLI around it comes in through Deps, so it can be
// tested in isolation.
package export

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/storage"
)

// Deps is what the command takes from the CLI around it.
type Deps struct {
	// JSONOutput reports the global --json flag; it is read when the command runs.
	JSONOutput func() bool
	// ProfileID is the profile the command runs in once OpenDB has resolved it ("" means
	// "default"); it is read when the command runs.
	ProfileID func() string
	// OutputDir is the global --output-dir flag, the default for the command's own.
	OutputDir func() string
	// OpenDB opens (and migrates) the database the command is pointed at.
	OpenDB func() (*storage.Database, error)
}

// NewCommand returns the `export` command.
func NewCommand(d Deps) *cobra.Command { return d.newExportCmd() }

// ExportPeople writes the profile's people to people_export.json in outputDir and returns how
// many it wrote (`people` tests share it).
var ExportPeople = exportPeopleData

// expandPath expands a leading "~/" to the home directory (the CLI's expandPath).
func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

// ensureDir creates path, "~/" expanded, with its parents (the CLI's ensureDir).
func ensureDir(path string) error {
	return os.MkdirAll(expandPath(path), 0755)
}
