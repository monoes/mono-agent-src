// cmd/monoagentcli/application.go
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	browserpkg "github.com/monoes/mono-agent/internal/browser"
	applicationcli "github.com/monoes/mono-agent/internal/cli/application"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/storage"
)

// `application` lives in internal/cli/application; this is its adapter: the CLI's globals and
// helpers handed over as applicationcli.Deps.

// newApplicationCmd returns the `application` command group: tracks job and
// tender applications through a shared pending/applied/rejected/cancelled
// pipeline. See docs/mastermind/specs/2026-09-05-applications-foundation-design.md.
func newApplicationCmd(cfg *globalConfig) *cobra.Command {
	return applicationcli.NewCommand(applicationcli.Deps{
		JSONOutput:   func() bool { return cfg.JSONOutput },
		ProfileID:    func() string { return cfg.ProfileID },
		OpenDB:       func() (*storage.Database, error) { return initDB(cfg) },
		NotFound:     errNotFound,
		InvalidInput: errInvalidInput,
		NewTable: func(w io.Writer, headers []string) *tablewriter.Table {
			return newPlainTable(w, headers, nil)
		},
		Bridge:  applicationApplyBridge,
		Confirm: confirmPrompt,
	})
}

// applicationApplyBridge obtains a connected MonoAgent extension bridge
// for `application apply` to open the job posting through.
func applicationApplyBridge(profileID string) (browserpkg.ExtensionBridge, error) {
	bridge := setupProfileBridge(profileID, newExtensionBridgeLogger(), 3*time.Second)
	if err := ensureExtensionConnected(bridge, 30*time.Second); err != nil {
		return nil, err
	}
	return bridge, nil
}

// confirmPrompt asks the user a y/N question on stdin/stdout. Returns true only on an
// explicit "y"/"yes" (case-insensitive) — any other input, including a read error or EOF,
// is treated as "no" (the safe default for an action that opens a real
// browser and starts an application flow).
func confirmPrompt(prompt string) bool {
	fmt.Fprintf(os.Stderr, "%s [y/N]: ", prompt)
	secrets.MarkStdinConsumed()
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}
