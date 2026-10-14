package capturecli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/capture"
	"github.com/monoes/mono-agent/internal/captureexport"
)

// captureGlueCmds are the capture subcommands that connect the inbox to
// everything outside it: a portable archive of the library (GLU-08). A task
// made from one capture (GLU-03, `capture task`) stays in the CLI, which adds
// it to the group. They live in their own files so the capture group itself
// stays one short list.
func (d Deps) captureGlueCmds() []*cobra.Command {
	return []*cobra.Command{
		d.newCaptureExportCmd(),
		d.newCaptureImportCmd(),
	}
}

func (d Deps) newCaptureExportCmd() *cobra.Command {
	var (
		out        string
		since      string
		collection string
		inbox      string
		only       []string
	)
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Pack captures into one portable archive",
		Long: "Writes the selected captures to a single .tar.gz holding a manifest.json, a\n" +
			"README.txt and one directory per capture — the same files the inbox holds,\n" +
			"unchanged.\n" +
			"\n" +
			"Nothing about the archive needs this tool to read it: `tar -xzf` unpacks it,\n" +
			"the manifest lists every file with its size and sha256, and the README inside\n" +
			"explains the layout. That is the point — MHTML, PDF, Markdown and JSON were\n" +
			"chosen because they outlive whatever wrote them.",
		Example: "  monoagentcli capture export\n" +
			"  monoagentcli capture export --out ~/backups/reading.tar.gz --since 2026-09-01\n" +
			"  monoagentcli capture export --collection research --json\n" +
			"  monoagentcli capture export --out - | ssh other-box monoagentcli capture import -",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			sinceAt, err := captureexport.ParseSince(since)
			if err != nil {
				return d.InvalidInput("%s", err.Error())
			}
			src := expandPath(inbox)
			if src == "" {
				src = capture.DefaultInbox()
			}
			opts := captureexport.ExportOptions{
				Inbox:      src,
				Since:      sinceAt,
				SinceText:  strings.TrimSpace(since),
				Collection: strings.TrimSpace(collection),
				Only:       only,
			}

			dest := expandPath(out)
			if dest == "" {
				dest = defaultArchiveName(time.Now())
			}
			if dest == "-" {
				// A file is staged and renamed, so an interrupted export
				// never leaves something that looks like a backup. A pipe
				// cannot be: the bytes are downstream already. So the one
				// honest thing left is to say the stream is not an
				// archive, before whoever is on the other end saves it.
				piped := &countingWriter{w: cmd.OutOrStdout()}
				man, err := captureexport.Export(piped, opts)
				if err != nil {
					if piped.n > 0 {
						fmt.Fprintf(cmd.ErrOrStderr(),
							"%s of this export reached the pipe before it failed — that stream is not a complete archive, discard it\n",
							captureHumanBytes(piped.n))
					}
					return fmt.Errorf("exporting captures: %w", err)
				}
				reportSkipped(cmd, man)
				fmt.Fprintf(cmd.ErrOrStderr(), "%d capture(s), %s\n", man.Count, captureHumanBytes(man.Bytes))
				return nil
			}

			man, err := writeArchive(dest, opts)
			if err != nil {
				return err
			}
			reportSkipped(cmd, man)
			if d.JSONOutput() {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]any{"archive": dest, "manifest": man})
			}
			if man.Count == 0 {
				fmt.Fprintf(cmd.ErrOrStderr(), "No captures matched — %s is an empty archive\n", dest)
			} else {
				fmt.Fprintf(cmd.ErrOrStderr(), "Exported %d capture(s), %s from %s\n",
					man.Count, captureHumanBytes(man.Bytes), src)
			}
			fmt.Fprintln(cmd.OutOrStdout(), dest)
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "Archive to write, or - for stdout (default: ./captures-<timestamp>.tar.gz)")
	cmd.Flags().StringVar(&since, "since", "", "Only captures taken on or after this date (2026-09-01 or RFC3339)")
	cmd.Flags().StringVar(&collection, "collection", "", "Only captures filed under this collection")
	cmd.Flags().StringVar(&inbox, "inbox", "", "Inbox to export from (default: ~/.monomind/inbox)")
	cmd.Flags().StringSliceVar(&only, "only", nil, "Only these capture directory names (repeatable)")
	return cmd
}

// reportSkipped says out loud what the archive does not hold. The inbox is
// live, so this is an ordinary outcome rather than a failure — but an
// export that quietly left a capture behind is a backup with a hole in it.
func reportSkipped(cmd *cobra.Command, man *captureexport.Manifest) {
	for _, n := range man.Skipped {
		fmt.Fprintf(cmd.ErrOrStderr(), "skipped %s — %s\n", n.Name, n.Reason)
	}
}

// countingWriter reports how much of an export already left the building.
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// writeArchive builds the archive beside its destination and renames it
// into place, so an interrupted export never leaves a half-written .tar.gz
// that looks like a backup.
func writeArchive(dest string, opts captureexport.ExportOptions) (*captureexport.Manifest, error) {
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-capture-export-*")
	if err != nil {
		return nil, fmt.Errorf("stage archive: %w", err)
	}
	staged := tmp.Name()
	committed := false
	defer func() {
		tmp.Close()
		if !committed {
			_ = os.Remove(staged)
		}
	}()

	man, err := captureexport.Export(tmp, opts)
	if err != nil {
		return nil, fmt.Errorf("exporting captures: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return nil, fmt.Errorf("flush archive: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("write archive: %w", err)
	}
	if err := os.Chmod(staged, 0o600); err != nil {
		return nil, fmt.Errorf("secure archive: %w", err)
	}
	if err := os.Rename(staged, dest); err != nil {
		return nil, fmt.Errorf("publish archive %s: %w", dest, err)
	}
	committed = true
	return man, nil
}

// defaultArchiveName is sortable and says what it is without being opened.
func defaultArchiveName(now time.Time) string {
	return "captures-" + now.UTC().Format("20060102-150405") + ".tar.gz"
}

func (d Deps) newCaptureImportCmd() *cobra.Command {
	var (
		inbox     string
		collision string
		dryRun    bool
	)
	cmd := &cobra.Command{
		Use:   "import <archive>",
		Short: "Restore captures from a portable archive into the inbox",
		Long: "Unpacks an archive written by `capture export` into ~/.monomind/inbox.\n" +
			"\n" +
			"A capture whose directory name is already in the inbox is skipped by default:\n" +
			"an import is additive, and re-importing an overlapping archive should change\n" +
			"nothing. --on-collision rename keeps both copies; --on-collision overwrite\n" +
			"replaces the local one outright.\n" +
			"\n" +
			"Each capture is unpacked into a staging directory and renamed into place only\n" +
			"once it is whole, so nothing watching the inbox ever sees half of one.",
		Example: "  monoagentcli capture import captures-20260921-120000.tar.gz\n" +
			"  monoagentcli capture import reading.tar.gz --on-collision rename\n" +
			"  monoagentcli capture import reading.tar.gz --dry-run --json",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			mode, err := captureexport.ParseCollision(collision)
			if err != nil {
				return d.InvalidInput("%s", err.Error())
			}
			dest := expandPath(inbox)
			if dest == "" {
				dest = capture.DefaultInbox()
			}

			src := cmd.InOrStdin()
			if args[0] != "-" {
				path := expandPath(args[0])
				f, err := os.Open(path)
				if err != nil {
					if os.IsNotExist(err) {
						return d.NotFound("no such archive: %s", path)
					}
					return fmt.Errorf("open %s: %w", path, err)
				}
				defer f.Close()
				src = f
			}

			res, err := captureexport.Import(src, captureexport.ImportOptions{
				Inbox: dest, OnCollision: mode, DryRun: dryRun,
			})
			if err != nil {
				return d.InvalidInput("%s", err.Error())
			}

			if d.JSONOutput() {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(res)
			}
			return d.printImportResult(cmd, res, dest)
		},
	}
	cmd.Flags().StringVar(&inbox, "inbox", "", "Inbox to restore into (default: ~/.monomind/inbox)")
	cmd.Flags().StringVar(&collision, "on-collision", string(captureexport.CollisionSkip),
		"What to do with a capture already in the inbox: skip, rename or overwrite")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Report what would be imported without writing anything")
	return cmd
}

func (d Deps) printImportResult(cmd *cobra.Command, res *captureexport.ImportResult, inbox string) error {
	stderr := cmd.ErrOrStderr()
	for _, s := range res.Skipped {
		fmt.Fprintf(stderr, "skipped %s — %s\n", s.Name, s.Reason)
	}
	if len(res.Imported) == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Nothing imported into %s\n", inbox)
		return nil
	}
	table := d.NewTable(cmd.OutOrStdout(), []string{"TITLE", "URL", "SIZE", "PATH"})
	var total int64
	for _, i := range res.Imported {
		total += i.Bytes
		_ = table.Append([]string{
			truncateCaptureCell(i.Title, 40),
			truncateCaptureCell(i.URL, 60),
			captureHumanBytes(i.Bytes),
			i.Path,
		})
	}
	if err := table.Render(); err != nil {
		return err
	}
	verb := "Imported"
	if res.DryRun {
		verb = "Would import"
	}
	fmt.Fprintf(stderr, "\n%s %d capture(s), %s into %s\n", verb, len(res.Imported), captureHumanBytes(total), inbox)
	return nil
}
