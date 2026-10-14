// internal/cli/profile/documents_sync.go
package profile

import (
	"encoding/json"
	"fmt"

	"github.com/monoes/mono-agent/internal/docscan"
	"github.com/monoes/mono-agent/internal/profiledir"
	"github.com/monoes/mono-agent/internal/vault"

	"github.com/spf13/cobra"
)

// folderSyncResult is the --json shape of `profile documents sync` and
// `image sync`: what one reconcile of the profile folder changed.
type folderSyncResult struct {
	ProfileID string   `json:"profile_id"`
	Root      string   `json:"root"`
	Scanned   int      `json:"scanned"`
	Added     int      `json:"added"`
	Updated   int      `json:"updated"`
	Removed   int      `json:"removed"`
	Changed   bool     `json:"changed"`
	Errors    []string `json:"errors"`
}

func newFolderSyncResult(profileID, root string, scanned int, r vault.ReconcileResult) folderSyncResult {
	out := folderSyncResult{ProfileID: profileID, Root: root, Scanned: scanned,
		Added: r.Added, Updated: r.Updated, Removed: r.Removed, Changed: r.Changed(), Errors: []string{}}
	for _, e := range r.Errs {
		out.Errors = append(out.Errors, e.Error())
	}
	return out
}

// PrintFolderSync writes res as JSON or one summary line, with per-file
// errors as stderr warnings in text mode. Per-file errors never fail the
// command: the rest of the folder was still reconciled.
func PrintFolderSync(cmd *cobra.Command, jsonOut bool, noun string, res folderSyncResult) error {
	if jsonOut {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}
	for _, e := range res.Errors {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", e)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Scanned %d %s in %s: %d added, %d updated, %d removed.\n",
		res.Scanned, noun, res.Root, res.Added, res.Updated, res.Removed)
	return nil
}

// newProfileDocumentsSyncCmd reconciles vault_documents with the documents
// on disk in the active profile's folder, once. The desktop app runs it
// when its folder watcher sees a change.
func (d Deps) newProfileDocumentsSyncCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "sync",
		Short:   "Register, refresh and remove discovered documents to match the profile folder",
		Args:    cobra.NoArgs,
		Example: `  monoagentcli profile documents sync --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()

			// Root unmodified: see docscan.Scan for why it must match
			// profiledir.Root byte-for-byte.
			root := profiledir.Root(db.DB, d.ProfileID())
			if root == "" {
				return d.InvalidInput("invalid profile id %q", d.ProfileID())
			}
			files, err := docscan.Scan(root)
			if err != nil {
				return fmt.Errorf("scanning %s: %w", root, err)
			}
			found := make([]vault.DiscoveredFile, len(files))
			for i, f := range files {
				found[i] = vault.DiscoveredFile{Path: f.Path, Filename: f.Filename, SizeBytes: f.SizeBytes}
			}
			r := vault.SyncDiscoveredDocuments(cmd.Context(), db.DB, d.ProfileID(), found)
			return PrintFolderSync(cmd, d.JSONOutput(), "documents", newFolderSyncResult(d.ProfileID(), root, len(files), r))
		},
	}
}
