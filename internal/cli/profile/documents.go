// internal/cli/profile/documents.go
package profile

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/monoes/mono-agent/internal/capturedocs"
	"github.com/monoes/mono-agent/internal/captureindex"
	"github.com/monoes/mono-agent/internal/capturesummary"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/profiledir"
	"github.com/monoes/mono-agent/internal/vault"

	"github.com/spf13/cobra"
)

// indexDocument runs knowledge_ingest for path and records the outcome via
// SetDocumentIndexed, stamping a real (mtime, size) staleness baseline on
// success. Shared by upload-document (index immediately after copying in)
// and the standalone `documents index` command (on-demand re-index).
// os.Stat failures are non-fatal here (best-effort baseline) since a
// missing baseline just means Stale can never trigger for this success --
// strictly safer than failing the whole ingest over a stat error. Returns
// setErr separately (rather than swallowing it) so each caller can decide
// how to surface a record-keeping failure distinctly from an indexing
// failure.
func indexDocument(ctx context.Context, db *sql.DB, profileID, id, path string) (indexed bool, indexErrMsg string, setErr error) {
	indexed = true
	if ingestErr := monomind.IngestDocument(ctx, db, profileID, path); ingestErr != nil {
		indexed = false
		indexErrMsg = ingestErr.Error()
	}
	var mtime, size int64
	if indexed {
		if fi, statErr := os.Stat(path); statErr == nil {
			mtime = fi.ModTime().UnixNano()
			size = fi.Size()
		}
	}
	setErr = vault.SetDocumentIndexed(ctx, db, profileID, id, indexed, indexErrMsg, mtime, size)
	return indexed, indexErrMsg, setErr
}

func (d Deps) newProfileUploadDocumentCmd() *cobra.Command {
	var source string
	cmd := &cobra.Command{
		Use:     "upload-document <path>",
		Short:   "Upload a profile document and index it for chat search",
		Args:    cobra.ExactArgs(1),
		Example: `  monoagentcli profile upload-document ~/resume.pdf`,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()

			ctx := vault.ContextWithProfileID(cmd.Context(), d.ProfileID())
			id, err := vault.RegisterDocument(ctx, db.DB, args[0], source)
			if err != nil {
				return fmt.Errorf("uploading document: %w", err)
			}

			docs, err := vault.ListDocuments(ctx, db.DB, d.ProfileID())
			if err != nil {
				return fmt.Errorf("looking up uploaded document: %w", err)
			}
			var storedPath string
			for _, d := range docs {
				if d.ID == id {
					storedPath = d.Path
				}
			}

			indexed, indexErrMsg, setErr := indexDocument(cmd.Context(), db.DB, d.ProfileID(), id, storedPath)
			if !indexed {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: document uploaded but indexing failed: %s\n", indexErrMsg)
			}
			if setErr != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not record indexing status: %v\n", setErr)
			}

			if d.JSONOutput() {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				out := map[string]interface{}{"id": id, "indexed": indexed}
				if indexErrMsg != "" {
					out["index_error"] = indexErrMsg
				}
				return enc.Encode(out)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Uploaded %q as %s.\n", args[0], id)
			if indexed {
				fmt.Fprintln(cmd.OutOrStdout(), "Indexed for knowledge search.")
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "Not indexed — %s\n", indexErrMsg)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&source, "source", "upload", "Where this document came from")
	return cmd
}

func (d Deps) newProfileDocumentsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "documents",
		Short: "Manage uploaded profile documents",
	}
	cmd.AddCommand(d.newProfileDocumentsListCmd(), d.newProfileDocumentsGetCmd(), d.newProfileDocumentsCaptureCmd(),
		d.newProfileDocumentsRmCmd(), d.newProfileDocumentsIndexCmd(), d.newProfileDocumentsSyncCmd())
	return cmd
}

func (d Deps) newProfileDocumentsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List profile documents (uploads, discovered files, browser captures)",
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()

			// Browser captures live in the profile's inbox, which the
			// folder scan skips; bring their rows up to date first so the
			// listing always includes every capture (and backfills ones
			// saved before this existed). A failed sync still lists.
			_, _, syncErrs := capturedocs.Sync(cmd.Context(), db.DB, d.ProfileID())
			for _, e := range syncErrs {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: syncing browser captures: %v\n", e)
			}

			docs, err := vault.ListDocuments(cmd.Context(), db.DB, d.ProfileID())
			if err != nil {
				return fmt.Errorf("listing documents: %w", err)
			}
			if d.JSONOutput() {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(withSummaryState(docs, time.Now()))
			}
			if len(docs) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No documents uploaded.")
				return nil
			}
			table := d.NewTable(cmd.OutOrStdout(), []string{"ID", "Filename", "Source", "Added"})
			for _, d := range docs {
				table.Append([]string{d.ID, truncateStr(d.Filename, 50), d.Source, d.CreatedAt})
			}
			table.Render()
			return nil
		},
	}
}

// listedDocument is one `profile documents list --json` row: the vault
// entry plus, for a browser capture that asked for an AI summary, where
// that summary is (capturesummary.StateOf). Omitted for every other row, so
// the output is unchanged for anything that is not a summarized capture.
type listedDocument struct {
	vault.DocumentEntry
	SummaryStatus string `json:",omitempty"`
}

// withSummaryState reads each capture row's summary.json: one small file
// per capture, and only for capture rows.
func withSummaryState(docs []vault.DocumentEntry, now time.Time) []listedDocument {
	out := make([]listedDocument, 0, len(docs))
	for _, d := range docs {
		out = append(out, listedDocument{DocumentEntry: d, SummaryStatus: capturesummary.StateOf(d.CaptureDir, now)})
	}
	return out
}

func (d Deps) newProfileDocumentsRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <id>",
		Short: "Delete an uploaded profile document",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()

			if err := vault.DeleteDocument(cmd.Context(), db.DB, d.ProfileID(), args[0]); err != nil {
				return d.NotFound("%v", err)
			}
			if d.JSONOutput() {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]string{"id": args[0]})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Deleted %q.\n", args[0])
			return nil
		},
	}
}

// newProfileDocumentsIndexCmd is the on-demand indexing action for a
// document the GUI/CLI already knows about (whether uploaded or
// discovered by a filesystem scan) -- the "Index" button for a Not
// indexed/Stale row. Output shape mirrors upload-document's (a lowercase
// JSON map, not documents-list's PascalCase struct encoding): behaviorally
// this is upload-document's sibling (both attempt one ingest and report
// pass/fail), not list's.
//
// A browser capture row goes to the profile's capture store instead (see
// internal/captureindex). --all is the backfill: every capture of the
// profile that is not indexed, has changed, or failed before.
func (d Deps) newProfileDocumentsIndexCmd() *cobra.Command {
	var all, allProfiles bool
	cmd := &cobra.Command{
		Use:   "index <id> | --all",
		Short: "Index (or re-index) a profile document for knowledge search",
		Long: "Index one document by id, or with --all every browser capture of the profile\n" +
			"that is not indexed yet, has changed since, or failed before. Captures are\n" +
			"indexed automatically by the extension bridge when they land; --all is the\n" +
			"backfill for captures saved before that, or while the bridge was not running.\n" +
			"--all-profiles does the same for every profile.",
		Example: `  monoagentcli profile documents index doc-003
  monoagentcli profile documents index --all
  monoagentcli profile documents index --all --all-profiles`,
		Args: func(cmd *cobra.Command, args []string) error {
			if all || allProfiles {
				if len(args) > 0 {
					return d.InvalidInput("give a document id or --all, not both")
				}
				return nil
			}
			if len(args) != 1 {
				return d.InvalidInput("give a document id, or --all for every capture")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()

			if all || allProfiles {
				return d.runIndexAllCaptures(cmd, db.DB, allProfiles)
			}

			doc, err := vault.GetDocument(cmd.Context(), db.DB, d.ProfileID(), args[0])
			if err != nil {
				return fmt.Errorf("looking up document: %w", err)
			}
			if doc == nil {
				return d.NotFound("document %q not found", args[0])
			}

			var indexed bool
			var indexErrMsg string
			if doc.CaptureDir != "" {
				ix := &captureindex.Indexer{}
				rep, ixErr := ix.IndexProfile(cmd.Context(), db.DB, d.ProfileID(), captureindex.Options{IDs: []string{doc.ID}, Force: true, RetryFailed: true})
				switch {
				case ixErr != nil:
					indexErrMsg = ixErr.Error()
				case len(rep.Outcomes) == 0:
					// Sync dropped the row: its capture left the inbox.
					return d.NotFound("capture %q is no longer in the inbox", args[0])
				default:
					indexed, indexErrMsg = rep.Outcomes[0].Indexed, rep.Outcomes[0].Error
				}
			} else {
				var setErr error
				indexed, indexErrMsg, setErr = indexDocument(cmd.Context(), db.DB, d.ProfileID(), args[0], doc.Path)
				if setErr != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not record indexing status: %v\n", setErr)
				}
			}

			if d.JSONOutput() {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				out := map[string]interface{}{"id": args[0], "indexed": indexed}
				if indexErrMsg != "" {
					out["index_error"] = indexErrMsg
				}
				return enc.Encode(out)
			}
			if indexed {
				fmt.Fprintln(cmd.OutOrStdout(), "Indexed for knowledge search.")
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "Not indexed — %s\n", indexErrMsg)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "Index every browser capture of the profile that is not indexed, has changed, or failed before")
	cmd.Flags().BoolVar(&allProfiles, "all-profiles", false, "With --all: every profile, not only the active one")
	return cmd
}

// runIndexAllCaptures is `profile documents index --all`: one retrying pass
// per profile. Exit 1 when any capture could not be indexed, so a script
// can tell.
func (d Deps) runIndexAllCaptures(cmd *cobra.Command, db *sql.DB, allProfiles bool) error {
	ids := []string{d.ProfileID()}
	if allProfiles {
		profiles, err := profiledir.List(cmd.Context(), db)
		if err != nil {
			return fmt.Errorf("listing profiles: %w", err)
		}
		ids = ids[:0]
		for _, p := range profiles {
			ids = append(ids, p.ID)
		}
	}
	ix := &captureindex.Indexer{}
	reports := make([]*captureindex.Report, 0, len(ids))
	failed := 0
	for _, id := range ids {
		rep, err := ix.IndexProfile(cmd.Context(), db, id, captureindex.Options{RetryFailed: true})
		if err != nil {
			return fmt.Errorf("indexing captures of %s: %w", id, err)
		}
		reports = append(reports, rep)
		failed += rep.Failed
		if !d.JSONOutput() {
			for _, o := range rep.Outcomes {
				if o.Indexed {
					fmt.Fprintf(cmd.OutOrStdout(), "indexed  %s  %s\n", o.ID, o.Title)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "FAILED   %s  %s — %s\n", o.ID, o.Title, o.Error)
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: %d indexed, %d failed, %d already up to date.\n", id, rep.Indexed, rep.Failed, rep.UpToDate)
		}
	}
	if d.JSONOutput() {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		if err := enc.Encode(map[string]any{"profiles": reports}); err != nil {
			return err
		}
	}
	if failed > 0 {
		return d.Reported(fmt.Errorf("%d capture(s) could not be indexed", failed))
	}
	return nil
}

func (d Deps) newProfileSearchKnowledgeCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "search-knowledge <query>",
		Short:   "Search uploaded profile documents (for testing — chat uses this automatically)",
		Args:    cobra.ExactArgs(1),
		Example: `  monoagentcli profile search-knowledge "backend frameworks"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()

			results, err := monomind.SearchKnowledge(cmd.Context(), db.DB, d.ProfileID(), args[0])
			if err != nil {
				return fmt.Errorf("searching profile documents: %w", err)
			}
			if d.JSONOutput() {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(results)
			}
			if len(results) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No matching content found.")
				return nil
			}
			for _, r := range results {
				fmt.Fprintf(cmd.OutOrStdout(), "[%.2f] %s: %s\n", r.Score, r.Path, r.Excerpt)
			}
			return nil
		},
	}
}
