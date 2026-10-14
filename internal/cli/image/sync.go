// internal/cli/image/sync.go
package image

import (
	"fmt"

	"github.com/monoes/mono-agent/internal/imagescan"
	"github.com/monoes/mono-agent/internal/profiledir"
	"github.com/monoes/mono-agent/internal/vault"

	"github.com/spf13/cobra"
)

// newImageSyncCmd reconciles the profile's discovered images (vault_images
// rows with source "discovered") with the image files in its folder, once.
// The desktop app runs it when its folder watcher sees a change. Uploaded,
// chat and workflow images are never touched.
func (d Deps) newImageSyncCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "sync",
		Short:   "Register, refresh and remove discovered images to match the profile folder",
		Args:    cobra.NoArgs,
		Example: `  monoagentcli image sync --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()

			root := profiledir.Root(db.DB, d.ProfileID())
			if root == "" {
				return d.InvalidInput("invalid profile id %q", d.ProfileID())
			}
			files, err := imagescan.Scan(root)
			if err != nil {
				return fmt.Errorf("scanning %s: %w", root, err)
			}
			found := make([]vault.DiscoveredFile, len(files))
			for i, f := range files {
				found[i] = vault.DiscoveredFile{Path: f.Path, Filename: f.Filename, SizeBytes: f.SizeBytes}
			}
			r := vault.SyncDiscoveredImages(cmd.Context(), db.DB, d.ProfileID(), found)
			return d.PrintFolderSync(cmd, "images", d.ProfileID(), root, len(files), r)
		},
	}
}
