// internal/cli/image/sync.go
package image

import (
	"database/sql"
	"fmt"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/imagescan"
	"github.com/monoes/mono-agent/internal/profiledir"
	"github.com/monoes/mono-agent/internal/vault"

	"github.com/spf13/cobra"
)

// newImageSyncCmd reconciles the profile's discovered media (vault_images
// rows with source "discovered") with the image files in its folder, once --
// and its audio and video files too when the profile has switched that on
// (`media scan on`; off by default). The desktop app runs it when its folder
// watcher sees a change. Uploaded, chat and workflow items are never touched.
func (d Deps) newImageSyncCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sync",
		Short: "Register, refresh and remove discovered media to match the profile folder",
		Long: "Tracks the images in the profile's folder in place. Audio and video there are tracked only " +
			"while `media scan` is on for the profile (it is off by default); turning it off drops their " +
			"discovered entries on the next sync and leaves the files alone.",
		Args:    cobra.NoArgs,
		Example: `  monoagentcli media sync --json`,
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
			withAV, err := vault.ScanAVEnabled(cmd.Context(), db.DB, d.ProfileID())
			if err != nil {
				return err
			}
			scan := imagescan.Scan
			if withAV {
				scan = imagescan.ScanMedia
			}
			files, err := scan(root)
			if err != nil {
				return fmt.Errorf("scanning %s: %w", root, err)
			}
			found := make([]vault.DiscoveredFile, len(files))
			for i, f := range files {
				found[i] = vault.DiscoveredFile{Path: f.Path, Filename: f.Filename, SizeBytes: f.SizeBytes}
			}
			r := vault.SyncDiscoveredImages(cmd.Context(), db.DB, d.ProfileID(), found)
			return d.PrintFolderSync(cmd, "media files", d.ProfileID(), root, len(files), r)
		},
	}
}

// newMediaScanCmd shows or sets whether `media sync` also tracks the audio and
// video files in the profile's folder. Off unless switched on.
func (d Deps) newMediaScanCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "scan [on|off]",
		Short: "Show or set whether audio and video in the profile folder are tracked (off by default)",
		Long: "Images in the profile's folder are always tracked by `media sync`. Audio and video there are " +
			"tracked only when this is on, because a project folder can hold many large recordings nobody " +
			"meant for the vault. It is a per-profile setting; it also decides whether a workflow node's " +
			"audio or video output is registered (images always are), and it does not affect `media add` or " +
			"files saved from the browser extension. Only the operator can change it. Turning it off drops " +
			"the discovered audio and video entries on the next sync; their files stay where they are.",
		Example: "  monoagentcli media scan\n  monoagentcli media scan on",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 && args[0] != "on" && args[0] != "off" {
				return d.InvalidInput("want on or off, got %q", args[0])
			}
			return d.withImageDB(func(db *sql.DB) error {
				if len(args) == 1 {
					if err := clicore.RequireOperator(d.RequireOperator, "switch audio and video discovery "+args[0]); err != nil {
						return err
					}
					if err := vault.SetScanAV(cmd.Context(), db, d.ProfileID(), args[0] == "on"); err != nil {
						return err
					}
				}
				on, err := vault.ScanAVEnabled(cmd.Context(), db, d.ProfileID())
				if err != nil {
					return err
				}
				if d.JSONOutput() {
					return d.WriteJSON(cmd.OutOrStdout(), map[string]bool{"scan_audio_video": on})
				}
				if on {
					fmt.Fprintln(cmd.OutOrStdout(), "Audio and video in the profile folder are tracked by `media sync`.")
				} else {
					fmt.Fprintln(cmd.OutOrStdout(), "Audio and video in the profile folder are not tracked (images are).")
				}
				return nil
			})
		},
	}
}
