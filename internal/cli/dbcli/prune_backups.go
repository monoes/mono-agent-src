package dbcli

import (
	"fmt"
	"time"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/spf13/cobra"
)

// PruneResult is `db prune-backups --json`.
type PruneResult struct {
	Removed    int   `json:"removed"`
	FreedBytes int64 `json:"freed_bytes"`
	Kept       int   `json:"kept"`
}

// `db prune-backups` removes the old pre-delete-profile snapshots by the
// rule `profile delete` applies itself: the newest few and anything younger
// than a month stay. It is the user's: the snapshots hold every profile's data.
func (d Deps) newPruneBackupsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "prune-backups",
		Short: "Remove old pre-delete-profile snapshots (keeps the newest 3 and any under 30 days old)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if why := opgate.Refusal(); why != "" {
				return clicore.OperatorOnly("only the user prunes the profile snapshots (they hold every profile's data); the user runs this in their own terminal: %s", why)
			}
			db, err := d.OpenDB()
			if err != nil {
				return err
			}
			defer db.Close()
			paths, err := db.ProfileDeleteBackups()
			if err != nil {
				return err
			}
			removed, freed := storage.PruneProfileDeleteBackupFiles(paths, "", time.Now())
			res := PruneResult{Removed: removed, FreedBytes: freed, Kept: len(paths) - removed}
			if d.JSONOutput() {
				return clicore.WriteJSONTo(cmd.OutOrStdout(), res)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed %d snapshot(s), freed %.1f MB, kept %d\n", res.Removed, float64(res.FreedBytes)/(1<<20), res.Kept)
			return nil
		},
	}
}
