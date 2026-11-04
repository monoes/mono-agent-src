// internal/cli/profile/documents_remove.go
package profile

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/profiledir"
	"github.com/monoes/mono-agent/internal/vault"

	"github.com/spf13/cobra"
)

// removedDocument is one `profile documents remove --json` row.
type removedDocument struct {
	ID          string   `json:"id"`
	Path        string   `json:"path"`
	Source      string   `json:"source"`
	Excluded    bool     `json:"excluded"`
	FileDeleted bool     `json:"file_deleted"`
	Warnings    []string `json:"warnings,omitempty"`
}

// removeResult is the --json shape of `profile documents remove`.
type removeResult struct {
	Removed []removedDocument `json:"removed"`
	Errors  []string          `json:"errors"`
}

// resolveDocPath turns a path given on the command line into the absolute
// path vault_documents holds: relative paths are taken from the profile folder.
func resolveDocPath(root, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(root, p)
}

// removeOne removes the document named by target (an id, or a path as above).
func (d Deps) removeOne(ctx context.Context, db *sql.DB, root, target string, deleteFile bool) (removedDocument, error) {
	var out removedDocument
	pid := d.ProfileID()
	doc, err := vault.GetDocument(ctx, db, pid, target)
	if err != nil {
		return out, err
	}
	if doc == nil {
		if id, ok, lookupErr := vault.DocumentIDByPath(ctx, db, pid, resolveDocPath(root, target)); lookupErr != nil {
			return out, lookupErr
		} else if ok {
			doc, err = vault.GetDocument(ctx, db, pid, id)
			if err != nil {
				return out, err
			}
		}
	}
	if doc == nil {
		return out, fmt.Errorf("document %q not found", target)
	}
	out = removedDocument{ID: doc.ID, Path: doc.Path, Source: doc.Source}

	if doc.Source != "discovered" {
		// An upload or capture is MonoAgent's own copy: deleting it is the
		// whole removal, as `documents rm` does.
		if err := vault.DeleteDocument(ctx, db, pid, doc.ID); err != nil {
			return out, err
		}
		return out, nil
	}
	if deleteFile {
		// Check the file is deletable before anything changes.
		if err := vault.CheckDiscoveredFile(root, doc.Path); err != nil {
			return out, err
		}
	}
	if doc.Indexed {
		if err := monomind.ForgetDocument(ctx, db, pid, doc.Path); err != nil {
			out.Warnings = append(out.Warnings, fmt.Sprintf("could not remove it from the knowledge graph: %v", err))
		}
	}
	// Tombstone first: a failure after the row is gone must not let a scan re-add it.
	if err := vault.ExcludeDocument(ctx, db, pid, doc.Path, doc.Filename); err != nil {
		return out, err
	}
	out.Excluded = true
	if err := vault.DeleteDocument(ctx, db, pid, doc.ID); err != nil {
		return out, err
	}
	if deleteFile {
		if err := vault.RemoveDiscoveredFile(root, doc.Path); err != nil {
			return out, fmt.Errorf("removed %s from MonoAgent but could not delete its file: %w", doc.ID, err)
		}
		out.FileDeleted = true
	}
	return out, nil
}

func (d Deps) newProfileDocumentsRemoveCmd() *cobra.Command {
	var deleteFile bool
	cmd := &cobra.Command{
		Use:   "remove <id|path>...",
		Short: "Remove documents from MonoAgent (an auto-discovered file is kept on disk and not re-added)",
		Long: "Remove documents by id or path (a relative path is taken from the profile folder).\n" +
			"An uploaded document is deleted. An auto-discovered one (source \"discovered\") is\n" +
			"deleted from the index and the knowledge graph and added to the profile's excluded\n" +
			"list, so the folder scan does not register it again; its file stays where it is.\n" +
			"`profile documents excluded` lists them and `profile documents restore` brings one back.\n" +
			"--delete-file also deletes the file from the profile folder; only the user does that.",
		Example: `  monoagentcli profile documents remove doc-004 doc-005 --json
  monoagentcli profile documents remove career-profile.md --delete-file`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if deleteFile {
				if why := opgate.Refusal(); why != "" {
					return clicore.OperatorOnly("only the user deletes a document's file from the profile folder; an agent can remove the document from MonoAgent without --delete-file: %s", why)
				}
			}
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()
			root := profiledir.Root(db.DB, d.ProfileID())
			if root == "" {
				return d.InvalidInput("invalid profile id %q", d.ProfileID())
			}

			res := removeResult{Removed: []removedDocument{}, Errors: []string{}}
			for _, target := range args {
				r, err := d.removeOne(cmd.Context(), db.DB, root, target, deleteFile)
				if err != nil {
					res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", target, err))
					continue
				}
				res.Removed = append(res.Removed, r)
			}
			if d.JSONOutput() {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if err := enc.Encode(res); err != nil {
					return err
				}
			} else {
				for _, r := range res.Removed {
					fmt.Fprintf(cmd.OutOrStdout(), "Removed %s (%s).\n", r.ID, r.Path)
					for _, w := range r.Warnings {
						fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s: %s\n", r.ID, w)
					}
					if r.FileDeleted {
						fmt.Fprintf(cmd.OutOrStdout(), "Deleted the file %s.\n", r.Path)
					}
				}
			}
			if len(res.Errors) > 0 {
				if !d.JSONOutput() {
					for _, e := range res.Errors {
						fmt.Fprintf(cmd.ErrOrStderr(), "error: %s\n", e)
					}
				}
				return d.Reported(fmt.Errorf("%s", strings.Join(res.Errors, "; ")))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&deleteFile, "delete-file", false, "Also delete an auto-discovered document's file from the profile folder (user only)")
	return cmd
}

// excludedListed is one `profile documents excluded --json` row.
type excludedListed struct {
	Path         string `json:"path"`
	RelativePath string `json:"relative_path"`
	Filename     string `json:"filename"`
	ExcludedAt   string `json:"excluded_at"`
}

func (d Deps) newProfileDocumentsExcludedCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "excluded",
		Short: "List auto-discovered documents removed from MonoAgent (not re-added by the folder scan)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()
			root := profiledir.Root(db.DB, d.ProfileID())
			list, err := vault.ListExcludedDocuments(cmd.Context(), db.DB, d.ProfileID())
			if err != nil {
				return err
			}
			rows := make([]excludedListed, 0, len(list))
			for _, e := range list {
				rel, relErr := filepath.Rel(root, e.Path)
				if relErr != nil {
					rel = e.Path
				}
				rows = append(rows, excludedListed{Path: e.Path, RelativePath: rel, Filename: e.Filename, ExcludedAt: e.CreatedAt})
			}
			if d.JSONOutput() {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(rows)
			}
			if len(rows) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No excluded documents.")
				return nil
			}
			table := d.NewTable(cmd.OutOrStdout(), []string{"Path", "Excluded"})
			for _, r := range rows {
				table.Append([]string{truncateStr(r.RelativePath, 70), r.ExcludedAt})
			}
			table.Render()
			return nil
		},
	}
}

func (d Deps) newProfileDocumentsRestoreCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restore <path>...",
		Short: "Let the folder scan add an excluded document again",
		Long: "Lift the exclusion on each path (relative paths are taken from the profile folder) and\n" +
			"scan the folder, so the document is registered again if its file is still there.",
		Example: `  monoagentcli profile documents restore career-profile.md`,
		Args:    cobra.MinimumNArgs(1),
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
			restored := []string{}
			var missing []string
			for _, a := range args {
				p := resolveDocPath(root, a)
				ok, err := vault.RestoreExcludedDocument(cmd.Context(), db.DB, d.ProfileID(), p)
				if err != nil {
					return err
				}
				if ok {
					restored = append(restored, p)
				} else {
					missing = append(missing, a)
				}
			}
			if len(restored) > 0 {
				if _, _, err := scanFolder(cmd.Context(), db.DB, d.ProfileID(), root); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: restored, but scanning the folder failed: %v\n", err)
				}
			}
			if d.JSONOutput() {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if err := enc.Encode(map[string]any{"restored": restored, "not_excluded": missing}); err != nil {
					return err
				}
			} else {
				for _, p := range restored {
					fmt.Fprintf(cmd.OutOrStdout(), "Restored %s.\n", p)
				}
			}
			if len(missing) > 0 {
				return d.NotFound("not in the excluded list: %v", missing)
			}
			return nil
		},
	}
}
