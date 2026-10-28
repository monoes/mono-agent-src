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
	"strings"

	"github.com/monoes/mono-agent/internal/vault"
	"github.com/spf13/cobra"
)

// newImageCmd manages the active profile's media vault (vault_images): the
// images, audio and video that workflows generate or the user uploads,
// addressable as @img-NNN (the id format predates audio and video and is kept
// for every kind). The GUI's Media Vault page goes through these commands.
// `image` is the command's old name and still works.
func (d Deps) newImageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "media",
		Aliases: []string{"image"},
		Short:   "Manage the media vault (images, audio, video): list, add, label, export, delete, sync, unignore",
		Long: "Media items — images, audio and video — belong to the profile named by --profile and are " +
			"addressed by id (img-001, …; the prefix is historic and the same for every kind). " +
			"Workflows refer to them as @img-001. Each item's \"kind\" is image, audio or video from " +
			"its file extension. `image` is the old name of this command group.",
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
		d.newMediaScanCmd(),
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
		fmt.Fprintln(cmd.OutOrStdout(), "No media.")
		return nil
	}
	table := d.NewTable(cmd.OutOrStdout(), []string{"ID", "Kind", "Label", "Source", "Size", "Added"})
	for _, im := range images {
		table.Append([]string{im.ID, kindOrFile(im.Kind), truncateStr(im.Label, 40), im.Source, strconv.FormatInt(im.SizeBytes, 10), im.CreatedAt})
	}
	table.Render()
	return nil
}

func (d Deps) printImage(cmd *cobra.Command, im *vault.ImageEntry) error {
	if d.JSONOutput() {
		return d.WriteJSON(cmd.OutOrStdout(), im)
	}
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "ID:       %s\nKind:     %s\nLabel:    %s\nFile:     %s\nSize:     %d bytes\nSource:   %s\n", im.ID, kindOrFile(im.Kind), im.Label, im.Path, im.SizeBytes, im.Source)
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
		Short: "List the profile's media, newest first",
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
	cmd.Flags().IntVar(&limit, "limit", 200, "Most items to list")
	return cmd
}

func (d Deps) newImageSearchCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "search <text>",
		Short: "Find media whose label, filename, source or workflow contains text",
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
		Short: "Show one item's details",
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

// kindOrFile is an item's kind for display: image, audio, video, or "file"
// for anything else the vault holds (a PDF or text file).
func kindOrFile(kind string) string {
	if kind == "" {
		return "file"
	}
	return kind
}

// mediaMIMEFallback covers audio and video extensions a machine's mime
// database may not know (Go's built-in table has no audio or video).
var mediaMIMEFallback = map[string]string{
	".mp3": "audio/mpeg", ".wav": "audio/wav", ".m4a": "audio/mp4", ".aac": "audio/aac",
	".ogg": "audio/ogg", ".oga": "audio/ogg", ".opus": "audio/ogg", ".flac": "audio/flac",
	".mp4": "video/mp4", ".m4v": "video/mp4", ".webm": "video/webm", ".mov": "video/quicktime",
	".mkv": "video/x-matroska", ".ogv": "video/ogg",
}

// imageMIMEType guesses a vault file's type from its extension; vault
// files without a recognised one are PNGs (vault.Register's default).
func imageMIMEType(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if t, ok := mediaMIMEFallback[ext]; ok {
		return t
	}
	if t := mime.TypeByExtension(ext); t != "" {
		return t
	}
	return "image/png"
}

// maxDataBytes is the most `media data` reads of an item that is not an image
// (it holds the file, then its base64, in memory); `media export` has no limit.
const maxDataBytes = 25 << 20

func (d Deps) newImageDataCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "data <id>",
		Short: "Print a media item's contents as a base64 data URL",
		Long: "Prints the item as a data: URL (\"data:image/png;base64,…\"), which a web page can " +
			"display directly (meant for images; a video or audio file is read whole into memory). With --json: {id, mime_type, size_bytes, data_url}. Without, the " +
			"data URL alone.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return d.withImageDB(func(db *sql.DB) error {
				im, err := vault.GetImage(cmd.Context(), db, d.ProfileID(), args[0])
				if err != nil {
					return d.imageErr(err)
				}
				if fi, statErr := os.Stat(im.Path); statErr == nil && im.Kind != "image" && fi.Size() > maxDataBytes {
					return d.InvalidInput("%s is %d MiB: `media data` reads the whole file into memory, so it takes at most %d MiB of audio, video or other files; use `media export %s <file>` instead",
						im.ID, fi.Size()>>20, maxDataBytes>>20, im.ID)
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
		Short: "Copy an image, audio or video file into the vault",
		Long: "Copies the file into the profile's vault folder as img-NNN<ext> and prints the new " +
			"item. The original file is left where it is. A file already in the profile's folder " +
			"(where `media sync` finds media) is recorded where it is instead of copied, and " +
			"adding one that was deleted from the vault brings it back.",
		Example: `  monoagentcli media add ~/Pictures/logo.png --label "Logo"
  monoagentcli media add ~/Music/theme.mp3 --label "Theme"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if fi, err := os.Stat(args[0]); err != nil {
				return d.InvalidInput("%v", err)
			} else if fi.IsDir() {
				return d.InvalidInput("%s is a directory, not a media file", args[0])
			}
			return d.withImageDB(func(db *sql.DB) error {
				ctx := vault.ContextWithProfileID(cmd.Context(), d.ProfileID())
				id, err := vault.Register(ctx, db, args[0], source, "", "")
				// Register indexes the image in the knowledge graph in the
				// background; let that settle before this process exits.
				defer vault.Settle()
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
	cmd.Flags().StringVar(&label, "label", "", "Name to show for the item")
	cmd.Flags().StringVar(&source, "source", "upload", "Where the item came from")
	return cmd
}

func (d Deps) newImageLabelCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "label <id> [label]",
		Short: "Rename a media item; without a label, clear it",
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
		Short:   "Delete a media item and its file (an item in the profile folder keeps its file)",
		Long: "Deletes the item and its vault file. An item that lives in the profile's folder " +
			"(discovered there, or saved there by the chat, a workflow or `media add`) keeps its " +
			"file, and `media sync` leaves it out from then on; `media add <path>` or " +
			"`media unignore <path>` brings it back.",
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
// deleted, so the next `media sync` lists it again.
func (d Deps) newImageUnignoreCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "unignore <path>",
		Short:   "Let media sync pick up a deleted profile-folder item again",
		Example: `  monoagentcli media unignore ~/.monoagent/profiles/<id>/images/logo.png`,
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
					return d.NotFound("%s is not a deleted media item of this profile", path)
				}
				if d.JSONOutput() {
					return d.WriteJSON(cmd.OutOrStdout(), map[string]any{"path": path, "unignored": true})
				}
				fmt.Fprintf(cmd.OutOrStdout(), "The next media sync will add %s again.\n", path)
				return nil
			})
		},
	}
}

func (d Deps) newImageStatsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stats",
		Short: "Count the profile's media items and their total size",
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
				fmt.Fprintf(cmd.OutOrStdout(), "%d items, %d bytes\n", st.Count, st.TotalBytes)
				return nil
			})
		},
	}
}

func (d Deps) newImageExportCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "export <id> <dest-file>",
		Short:   "Copy a media item out of the vault to a file",
		Example: `  monoagentcli media export img-004 ~/Desktop/banner.png`,
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
