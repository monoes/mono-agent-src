// Package action is `monoagentcli action`: export and import single actions as
// minimal packages, and the action template commands (capture, install, list).
// The install pipeline it shares with `automation` is internal/cli/autoinstall;
// everything else these commands need from the CLI comes in through Deps.
package action

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/autoinstall"
)

// Deps is what the commands take from the CLI around them.
type Deps struct {
	// Install is the CLI's install pipeline: --json, the registry, the prompt and
	// the install result printers.
	Install autoinstall.Deps
	// WithJSONErrors makes a command print its error as the CLI's --json error document.
	WithJSONErrors func(cmd *cobra.Command)
	// RenderHTML opens rawURL in the user's browser through the extension, waits for
	// the page to render and returns its HTML.
	RenderHTML func(ctx context.Context, rawURL string, wait time.Duration) (string, error)
}

// NewCommand builds `monoagentcli action`.
func NewCommand(d Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "action",
		Short: "Export, import and template single actions",
		Long:  "Move single actions between automations and machines, and capture action templates for new sites.",
	}

	cmd.AddCommand(
		d.newActionTemplateCmd(),
		d.newActionExportCmd(),
		d.newActionImportCmd(),
	)
	for _, sub := range cmd.Commands() {
		d.WithJSONErrors(sub)
	}

	return cmd
}

// writeJSONTo writes v as indented JSON to w (a copy of the CLI's).
func writeJSONTo(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
