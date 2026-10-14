package image

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strconv"

	"github.com/monoes/mono-agent/internal/vault"
	"github.com/spf13/cobra"
)

// newImageCmd manages the active profile's image vault (vault_images): the
// files workflows generate or the user uploads, addressable as @img-NNN.
// The GUI's Image Vault page goes through these commands.
func (d Deps) newImageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "image",
		Short: "Manage the image vault: list, add, label, export, delete, sync, unignore",
		Long: "Images belong to the active profile and are addressed by id (img-001, …). " +
			"Workflows refer to them as @img-001.",
	}
	cmd.AddCommand(
		d.newImageListCmd(),
		d.newImageGetCmd(),
		d.newImageDataCmd(),
		d.newImageAddCmd(),
		d.newImageLabelCmd(),
		d.newImageDeleteCmd(),
		d.newImageSearchCmd(),
		d.newImageStatsCmd(),
		d.newImageExportCmd(),
		d.newImageSyncCmd(),
		d.newImageUnignoreCmd(),
	)
	return cmd
}

func (d Deps) withImageDB(fn func(db *sql.DB) error) error {
	db, err := d.OpenDB()
	if err != nil {
		return fmt.Errorf("initializing database: %w", err)
	}
	defer db.Close()
	return fn(db.DB)
}

// imageErr maps a vault lookup miss to exit 2.
func (d Deps) imageErr(err error) error {
	if errors.Is(err, vault.ErrImageNotFound) {
		return d.NotFound("%v", err)
	}
	return err
}

func (d Deps) printImages(cmd *cobra.Command, images []vault.ImageEntry) error {
	if d.JSONOutput() {
		return d.WriteJSON(cmd.OutOrStdout(), images)
	}
	if len(images) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No images.")
		return nil
	}
	table := d.NewTable(cmd.OutOrStdout(), []string{"ID", "Label", "Source", "Size", "Added"})
	for _, im := range images {
		table.Append([]string{im.ID, truncateStr(im.Label, 40), im.Source, strconv.FormatInt(im.SizeBytes, 10), im.CreatedAt})
	}
	table.Render()
	return nil
}

func (d Deps) printImage(cmd *cobra.Command, im *vault.ImageEntry) error {
	if d.JSONOutput() {
		return d.WriteJSON(cmd.OutOrStdout(), im)
	}
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "ID:       %s\nLabel:    %s\nFile:     %s\nSize:     %d bytes\nSource:   %s\n", im.ID, im.Label, im.Path, im.SizeBytes, im.Source)
	if im.WorkflowID != "" {
		fmt.Fprintf(w, "Workflow: %s\n", im.WorkflowID)
	}
	if im.ExecutionID != "" {
		fmt.Fprintf(w, "Run:      %s\n", im.ExecutionID)
	}
	fmt.Fprintf(w, "Added:    %s\n", im.CreatedAt)
	return nil
}

func (d Deps) positiveLimit(limit int) error {
	if limit <= 0 {
		return d.InvalidInput("--limit must be positive, got %d", limit)
	}
	return nil
}

func (d Deps) newImageListCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the profile's images, newest first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := d.positiveLimit(limit); err != nil {
				return err
			}
			return d.withImageDB(func(db *sql.DB) error {
				images, err := vault.ListImages(cmd.Context(), db, d.ProfileID(), limit)
				if err != nil {
					return err
				}
				return d.printImages(cmd, images)
			})
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 200, "Most images to list")
	return cmd
}

func (d Deps) newImageSearchCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "search <text>",
		Short: "Find images whose label, filename, source or workflow contains text",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := d.positiveLimit(limit); err != nil {
				return err
			}
			return d.withImageDB(func(db *sql.DB) error {
				images, err := vault.SearchImages(cmd.Context(), db, d.ProfileID(), args[0], limit)
				if err != nil {
					return err
				}
				return d.printImages(cmd, images)
			})
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 100, "Most images to return")
	return cmd
}

func (d Deps) newImageGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <id>",
		Short: "Show one image's details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return d.withImageDB(func(db *sql.DB) error {
				im, err := vault.GetImage(cmd.Context(), db, d.ProfileID(), args[0])
				if err != nil {
					return d.imageErr(err)
				}
				return d.printImage(cmd, im)
			})
		},
	}
}

// imageMIMEType guesses a vault file's type from its extension; vault
// files without a recognised one are PNGs (vault.Register's default).
func imageMIMEType(path string) string {
	if t := mime.TypeByExtension(filepath.Ext(path)); t != "" {
		return t
	}
	return "image/png"
}

func (d Deps) newImageDataCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "data <id>",
		Short: "Print an image's contents as a base64 data URL",
		Long: "Prints the image as a data: URL (\"data:image/png;base64,…\"), which a web page can " +
			"display directly. With --json: {id, mime_type, size_bytes, data_url}. Without, the " +
			"data URL alone.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return d.withImageDB(func(db *sql.DB) error {
				im, err := vault.GetImage(cmd.Context(), db, d.ProfileID(), args[0])
				if err != nil {
					return d.imageErr(err)
				}
				data, err := os.ReadFile(im.Path)
				if err != nil {
					return fmt.Errorf("vault image file read: %w", err)
				}
				mimeType := imageMIMEType(im.Path)
				dataURL := "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data)
				if !d.JSONOutput() {
					_, err := fmt.Fprintln(cmd.OutOrStdout(), dataURL)
					return err
				}
				return d.WriteJSON(cmd.OutOrStdout(), map[string]any{
					"id": im.ID, "mime_type": mimeType, "size_bytes": len(data), "data_url": dataURL,
				})
			})
		},
	}
}

func (d Deps) newImageAddCmd() *cobra.Command {
	var label, source string
	cmd := &cobra.Command{
		Use:   "add <file>",
		Short: "Copy an image file into the vault",
		Long: "Copies the file into the profile's vault folder as img-NNN<ext> and prints the new " +
			"image. The original file is left where it is. A file already in the profile's folder " +
			"(where `image sync` finds images) is recorded where it is instead of copied, and " +
			"adding one that was deleted from the vault brings it back.",
		Example: `  monoagentcli image add ~/Pictures/logo.png --label "Logo"`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if fi, err := os.Stat(args[0]); err != nil {
				return d.InvalidInput("%v", err)
			} else if fi.IsDir() {
				return d.InvalidInput("%s is a directory, not an image file", args[0])
			}
			return d.withImageDB(func(db *sql.DB) error {
				ctx := vault.ContextWithProfileID(cmd.Context(), d.ProfileID())
				id, err := vault.Register(ctx, db, args[0], source, "", "")
				// Register indexes the image in the knowledge graph in the
				// background; let that finish before this process exits.
				defer vault.Wait()
				if err != nil {
					return fmt.Errorf("vault register: %w", err)
				}
				if label != "" {
					if err := vault.SetImageLabel(ctx, db, d.ProfileID(), id, label); err != nil {
						return err
					}
				}
				im, err := vault.GetImage(ctx, db, d.ProfileID(), id)
				if err != nil {
					return err
				}
				return d.printImage(cmd, im)
			})
		},
	}
	cmd.Flags().StringVar(&label, "label", "", "Name to show for the image")
	cmd.Flags().StringVar(&source, "source", "upload", "Where the image came from")
	return cmd
}

func (d Deps) newImageLabelCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "label <id> [label]",
		Short: "Rename an image; without a label, clear it",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			label := ""
			if len(args) == 2 {
				label = args[1]
			}
			return d.withImageDB(func(db *sql.DB) error {
				if err := vault.SetImageLabel(cmd.Context(), db, d.ProfileID(), args[0], label); err != nil {
					return d.imageErr(err)
				}
				if d.JSONOutput() {
					return d.WriteJSON(cmd.OutOrStdout(), map[string]string{"id": args[0], "label": label})
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Labelled %s %q.\n", args[0], label)
				return nil
			})
		},
	}
}

func (d Deps) newImageDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "delete <id>",
		Aliases: []string{"rm"},
		Short:   "Delete an image and its file (an image in the profile folder keeps its file)",
		Long: "Deletes the image and its vault file. An image that lives in the profile's folder " +
			"(discovered there, or saved there by the chat, a workflow or `image add`) keeps its " +
			"file, and `image sync` leaves it out from then on; `image add <path>` or " +
			"`image unignore <path>` brings it back.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return d.withImageDB(func(db *sql.DB) error {
				if err := vault.DeleteImage(cmd.Context(), db, d.ProfileID(), args[0]); err != nil {
					return d.imageErr(err)
				}
				if d.JSONOutput() {
					return d.WriteJSON(cmd.OutOrStdout(), map[string]string{"id": args[0]})
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Deleted %s.\n", args[0])
				return nil
			})
		},
	}
}

// newImageUnignoreCmd forgets that an image in the profile folder was
// deleted, so the next `image sync` lists it again.
func (d Deps) newImageUnignoreCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "unignore <path>",
		Short:   "Let image sync pick up a deleted profile-folder image again",
		Example: `  monoagentcli image unignore ~/.monoagent/profiles/<id>/images/logo.png`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := filepath.Abs(args[0])
			if err != nil {
				return d.InvalidInput("%v", err)
			}
			return d.withImageDB(func(db *sql.DB) error {
				ok, err := vault.UnignoreImagePath(cmd.Context(), db, d.ProfileID(), path)
				if err != nil {
					return err
				}
				if !ok {
					return d.NotFound("%s is not a deleted image of this profile", path)
				}
				if d.JSONOutput() {
					return d.WriteJSON(cmd.OutOrStdout(), map[string]any{"path": path, "unignored": true})
				}
				fmt.Fprintf(cmd.OutOrStdout(), "The next image sync will add %s again.\n", path)
				return nil
			})
		},
	}
}

func (d Deps) newImageStatsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stats",
		Short: "Count the profile's images and their total size",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return d.withImageDB(func(db *sql.DB) error {
				st, err := vault.GetImageStats(cmd.Context(), db, d.ProfileID())
				if err != nil {
					return err
				}
				if d.JSONOutput() {
					return d.WriteJSON(cmd.OutOrStdout(), st)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%d images, %d bytes\n", st.Count, st.TotalBytes)
				return nil
			})
		},
	}
}

func (d Deps) newImageExportCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "export <id> <dest-file>",
		Short:   "Copy an image out of the vault to a file",
		Example: `  monoagentcli image export img-004 ~/Desktop/banner.png`,
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[1] == "" {
				return d.InvalidInput("destination file is empty")
			}
			if fi, err := os.Stat(args[1]); err == nil && fi.IsDir() {
				return d.InvalidInput("%s is a directory; give a file name", args[1])
			}
			return d.withImageDB(func(db *sql.DB) error {
				im, err := vault.GetImage(cmd.Context(), db, d.ProfileID(), args[0])
				if err != nil {
					return d.imageErr(err)
				}
				dest, err := filepath.Abs(args[1])
				if err != nil {
					return d.InvalidInput("%v", err)
				}
				if err := copyImageFile(im.Path, dest); err != nil {
					return err
				}
				if d.JSONOutput() {
					return d.WriteJSON(cmd.OutOrStdout(), map[string]string{"id": im.ID, "path": dest})
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Saved %s to %s.\n", im.ID, dest)
				return nil
			})
		},
	}
}

func copyImageFile(src, dst string) (retErr error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); retErr == nil {
			retErr = cerr
		}
	}()
	_, err = io.Copy(out, in)
	return err
}
