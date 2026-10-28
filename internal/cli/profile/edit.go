package profile

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/text/unicode/norm"

	"github.com/monoes/mono-agent/internal/attachments"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/workflow"
)

// refuseNotOperator refuses changing or deleting a profile to anyone but the
// user: a profile holds their accounts and data, and a chat, a workflow run
// or an agent must not be able to rename it or erase it.
func (d Deps) refuseNotOperator(what string) error {
	if why := operator.Refusal(); why != "" {
		return d.InvalidInput("only the user %s a profile (it holds their accounts and data); the user runs this in their own terminal or in the app's Settings: %s", what, why)
	}
	return nil
}

// profileTable is one table that holds rows of a profile, with how many.
type profileTable struct {
	Table string `json:"table"`
	Rows  int    `json:"rows"`
}

type profileImpact struct {
	Profile profileJSON    `json:"profile"`
	Tables  []profileTable `json:"tables"`
	// FolderKept says the profile's folder on disk is never deleted.
	FolderKept bool `json:"folder_kept"`
	// Backup is the database snapshot taken before a delete ("" for --summary).
	// It holds every profile's data; old ones are pruned by later deletes.
	Backup string `json:"backup,omitempty"`
	// KeyRemoved says the profile's vault key left the OS keychain.
	KeyRemoved bool `json:"key_removed,omitempty"`
	// BrowsersUnbound is how many connected browsers bound to the profile were unbound.
	BrowsersUnbound int `json:"browsers_unbound,omitempty"`
	// AttachmentsRemoved is how many synced message attachment files were deleted.
	AttachmentsRemoved int `json:"attachments_removed,omitempty"`
	// Warnings are steps after the delete that did not finish.
	Warnings []string `json:"warnings,omitempty"`
}

const (
	// backupNote is said wherever a backup path is.
	backupNote = "It is a full copy of the database (every profile, not only this one), it is not encrypted, " +
		"and only the oldest are deleted automatically (beyond the newest 3 and 30 days): `monoagentcli profile backups purge` removes these copies. " +
		"The profile's vault key is deleted, so the vault secrets in that copy cannot be decrypted. " +
		"Its synced message attachment files are deleted with the profile (unless --keep-attachments), so restoring the copy brings back messages whose attachment paths point at files that are gone; sync again to fetch them."
)

func (d Deps) newProfileUpdateCmd() *cobra.Command {
	var name, icon string
	cmd := &cobra.Command{
		Use:   "update <name-or-id>",
		Short: "Rename a profile or change its icon",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := d.refuseNotOperator("renames or re-icons"); err != nil {
				return err
			}
			nameSet, iconSet := cmd.Flags().Changed("name"), cmd.Flags().Changed("icon")
			if !nameSet && !iconSet {
				return d.InvalidInput("nothing to update: pass --name and/or --icon")
			}
			name, icon = norm.NFC.String(strings.TrimSpace(name)), strings.TrimSpace(icon)
			if iconSet {
				if err := d.validateIcon(icon); err != nil {
					return err
				}
			}
			db, err := d.OpenDB()
			if err != nil {
				return err
			}
			defer db.Close()
			p, err := d.lookupProfile(db.DB, args[0])
			if err != nil {
				return err
			}
			// A name that is not changing (the app sends it along with a new icon)
			// is not judged again: it may be one an older version allowed.
			if nameSet && name != p.Name {
				if err := validateProfileName(name); err != nil {
					return d.InvalidInput("%v", err)
				}
				if err := d.checkNameFree(db.DB, name, p.ID); err != nil {
					return err
				}
				p.Name = name
			}
			if iconSet {
				p.Icon = icon
			}
			if _, err := db.DB.Exec(`UPDATE profiles SET name = ?, icon = ? WHERE id = ?`, p.Name, p.Icon, p.ID); err != nil {
				return fmt.Errorf("update profile: %w", err)
			}
			if d.JSONOutput() {
				return printJSON(p)
			}
			fmt.Printf("Updated profile: %s (%s)\n", p.Name, p.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "New profile name")
	cmd.Flags().StringVar(&icon, "icon", "", "Icon id from the agent-avatars manifest (empty clears it)")
	return cmd
}

// workflowFilesDir is where workflows are kept as files.
func workflowFilesDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".monoagent", "workflows"), nil
}

// profileWorkflowFiles are the names of the profile's workflow files in dir:
// those whose file says it is the profile's (whatever the file is called) and
// the <id>.json of a workflow the database has for the profile when the file
// names no profile. A file whose workflow id is a row of another profile is
// never taken, whatever profile it claims.
func profileWorkflowFiles(ctx context.Context, db *sql.DB, profileID string) (dir string, names []string, err error) {
	dir, err = workflowFilesDir()
	if err != nil {
		return "", nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return dir, nil, nil
		}
		return "", nil, err
	}
	for _, e := range entries {
		base, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !e.Type().IsRegular() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		wf, err := workflow.ParseWorkflowFileBytes(data)
		if err != nil {
			continue
		}
		var owner sql.NullString
		for _, id := range []string{wf.ID, base} {
			if id == "" {
				continue
			}
			var o string
			if db.QueryRowContext(ctx, `SELECT profile_id FROM workflows WHERE id = ?`, id).Scan(&o) == nil {
				if o != profileID {
					owner = sql.NullString{String: o, Valid: true}
					break
				}
				owner = sql.NullString{String: o, Valid: true}
			}
		}
		switch {
		case owner.Valid && owner.String != profileID:
			continue // a row of another profile has this id
		case wf.ProfileID == profileID, wf.ProfileID == "" && owner.Valid:
			names = append(names, e.Name())
		}
	}
	return dir, names, nil
}

func (d Deps) newProfileDeleteCmd() *cobra.Command {
	var summary, keepAttachments bool
	var confirmName string
	cmd := &cobra.Command{
		Use:   "delete <name-or-id>",
		Short: "Delete a profile and everything stored under it",
		Long: "Delete a profile with its rows in every profile-scoped table (people, messages, workflows, tasks, " +
			"publications, vault items, documents, credentials, chats, ...), its workflow files, its settings entries " +
			"and its vault key in the OS keychain, and the message attachments it synced (--keep-attachments leaves them). " +
			"The profile's folder on disk is kept. A database snapshot is taken first; snapshots older than 30 days " +
			"beyond the newest 3 are removed. --summary only prints what would be deleted. Deleting needs --yes-delete-profile <the profile's " +
			"exact name>, only the user can run it, and it is refused while the profile has running workflows.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !summary {
				if err := d.refuseNotOperator("deletes"); err != nil {
					return err
				}
				if confirmName == "" {
					return d.InvalidInput("deleting a profile is permanent: pass --yes-delete-profile <its exact name> (or --summary to see what it would remove)")
				}
			}
			db, err := d.OpenDB()
			if err != nil {
				return err
			}
			defer db.Close()
			p, err := d.lookupProfile(db.DB, args[0])
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			if summary {
				return d.printDeleteSummary(ctx, db.DB, p)
			}
			if confirmName != p.Name {
				return d.InvalidInput("--yes-delete-profile %q does not match the profile's name %q", confirmName, p.Name)
			}
			var n int
			if err := db.DB.QueryRow(`SELECT COUNT(*) FROM profiles`).Scan(&n); err != nil {
				return fmt.Errorf("count profiles: %w", err)
			}
			if n <= 1 {
				return d.InvalidInput("cannot delete the only profile: create another one first")
			}
			if err := refuseRunning(ctx, db.DB, p.ID); err != nil {
				return d.InvalidInput("%v", err)
			}
			var attWarnings []string
			impact := profileImpact{Profile: *p, FolderKept: true}
			impact.Backup, err = db.SnapshotBeforeProfileDelete(ctx)
			if err != nil {
				return fmt.Errorf("backing up the database before deleting (nothing was deleted): %w", err)
			}
			if paths, lerr := db.ProfileDeleteBackups(); lerr == nil {
				pruneBackups(paths, impact.Backup, time.Now())
			}
			var attFiles []string
			if !keepAttachments {
				attFiles, err = profileAttachments(ctx, db.DB, p.ID)
				if err != nil {
					return fmt.Errorf("listing the profile's message attachments (nothing was deleted): %w", err)
				}
			}
			wfDir, wfFiles, ferr := profileWorkflowFiles(ctx, db.DB, p.ID)
			if ferr != nil {
				return fmt.Errorf("listing the profile's workflow files (nothing was deleted): %w", ferr)
			}
			tables, err := deleteProfile(ctx, db.DB, p.ID)
			if err != nil {
				return err
			}
			impact.Tables = tables
			// After the commit: files and the keychain are not part of the transaction.
			for _, name := range wfFiles {
				if err := os.Remove(filepath.Join(wfDir, name)); err != nil && !os.IsNotExist(err) {
					impact.Warnings = append(impact.Warnings, fmt.Sprintf("workflow file %s was not removed: %v", name, err))
				}
			}
			// Messages may have arrived since the list was made: re-check just before unlinking.
			if kept, rerr := unreferenced(ctx, db.DB, attFiles); rerr != nil {
				attFiles = nil
				impact.Warnings = append(impact.Warnings, "attachment files were kept because their references could not be re-checked: "+rerr.Error())
			} else {
				attFiles = kept
			}
			impact.AttachmentsRemoved, attWarnings = removeAttachments(attFiles)
			impact.Warnings = append(impact.Warnings, attWarnings...)
			if d.UnbindBrowsers != nil {
				if n, err := d.UnbindBrowsers(ctx, p.ID); err != nil {
					impact.Warnings = append(impact.Warnings, "browsers bound to the profile were not all unbound (`monoagentcli extension unbind <browser>`): "+err.Error())
				} else {
					impact.BrowsersUnbound = n
				}
			}
			removed, kerr := secrets.DeleteProfileKEK(p.ID)
			impact.KeyRemoved = removed
			if kerr != nil {
				impact.Warnings = append(impact.Warnings, "the vault key may remain: "+kerr.Error())
			}
			if d.JSONOutput() {
				return printJSON(impact)
			}
			fmt.Printf("Deleted profile: %s (%s)\nDatabase backup: %s\n%s\nIts folder %s was left on disk.\n", p.Name, p.ID, impact.Backup, backupNote, p.RootDir)
			if keepAttachments {
				fmt.Printf("Its synced message attachments (%s) were kept.\n", attachments.Dir())
			} else {
				fmt.Printf("Removed %d synced message attachment file(s).\n", impact.AttachmentsRemoved)
			}
			for _, w := range impact.Warnings {
				fmt.Printf("warning: %s\n", w)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&summary, "summary", false, "Only print what deleting would remove")
	cmd.Flags().BoolVar(&keepAttachments, "keep-attachments", false, "Leave the profile's synced message attachment files on disk")
	cmd.Flags().StringVar(&confirmName, "yes-delete-profile", "", "Confirm the deletion by giving the profile's exact name")
	return cmd
}

func (d Deps) printDeleteSummary(ctx context.Context, db *sql.DB, p *profileJSON) error {
	scope, err := profileScope(ctx, db)
	if err != nil {
		return err
	}
	tables, err := countScope(ctx, db, scope, p.ID)
	if err != nil {
		return err
	}
	if _, files, err := profileWorkflowFiles(ctx, db, p.ID); err == nil && len(files) > 0 {
		tables = append(tables, profileTable{Table: "workflow files", Rows: len(files)})
	}
	impact := profileImpact{Profile: *p, Tables: tables, FolderKept: true}
	if d.JSONOutput() {
		return printJSON(impact)
	}
	fmt.Printf("Deleting %s would remove:\n", p.Name)
	for _, t := range tables {
		fmt.Printf("  %-32s %d\n", t.Table, t.Rows)
	}
	fmt.Printf("and its vault key and synced message attachments (%s; --keep-attachments leaves them). Its folder %s would be left on disk.\n", attachments.Dir(), p.RootDir)
	return nil
}

// refuseRunning refuses deleting a profile while a workflow of it is running.
func refuseRunning(ctx context.Context, q scopeQuerier, id string) error {
	var n int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_executions WHERE profile_id = ? AND status IN ('RUNNING', 'QUEUED')`, id).Scan(&n); err != nil {
		return fmt.Errorf("check running workflows: %w", err)
	}
	var open int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_publications WHERE profile_id = ? OR workflow_id IN (SELECT id FROM workflows WHERE profile_id = ?)`, id, id).Scan(&open); err != nil && !strings.Contains(err.Error(), "no such table") {
		return fmt.Errorf("check unfinished workflow changes: %w", err)
	}
	if open > 0 {
		return fmt.Errorf("the profile has %d unfinished workflow change(s): run `monoagentcli doctor fix workflows.publications.recover` (or resolve them with `doctor`), then delete again", open)
	}
	if n > 0 {
		return fmt.Errorf("the profile has %d running or queued workflow execution(s): let them finish or cancel them first with `monoagentcli workflow execution cancel <execution-id>` (an execution left RUNNING by a crashed run also blocks the delete until it is cancelled)", n)
	}
	return nil
}

// deleteProfile removes everything the profile owns, and then the profile,
// all or nothing. What it owns is read inside the transaction, so a row added
// since the caller looked is not missed. It returns what was removed.
func deleteProfile(ctx context.Context, db *sql.DB, id string) ([]profileTable, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Rows in child tables that reference these through their own keys are
	// checked at commit, after the cascades have run.
	if _, err := tx.ExecContext(ctx, `PRAGMA defer_foreign_keys = ON`); err != nil {
		return nil, err
	}
	if err := refuseRunning(ctx, tx, id); err != nil {
		return nil, err
	}
	scope, err := profileScope(ctx, tx)
	if err != nil {
		return nil, err
	}
	tables, err := countScope(ctx, tx, scope, id)
	if err != nil {
		return nil, err
	}
	violationsBefore, err := foreignKeyViolations(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("check foreign keys: %w", err)
	}
	for _, e := range scope {
		if _, err := tx.ExecContext(ctx, `DELETE FROM "`+e.Table+`" WHERE `+e.Where, sql.Named("p", id)); err != nil {
			return nil, fmt.Errorf("delete from %s: %w", e.Table, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM profiles WHERE id = ?`, id); err != nil {
		return nil, fmt.Errorf("delete profile: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM settings WHERE key = 'active_profile_id' AND value = ?`, id); err != nil {
		return nil, err
	}
	if err := repairDanglingReferences(ctx, tx, violationsBefore); err != nil {
		return nil, fmt.Errorf("delete profile: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("delete profile: %w", err)
	}
	return tables, nil
}

func (d Deps) newProfileBackupsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backups",
		Short: "The database snapshots taken before deleting a profile",
	}
	list := &cobra.Command{
		Use:   "list",
		Short: "List the pre-delete snapshots",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return err
			}
			defer db.Close()
			paths, err := db.ProfileDeleteBackups()
			if err != nil {
				return err
			}
			if d.JSONOutput() {
				if paths == nil {
					paths = []string{}
				}
				return printJSON(paths)
			}
			for _, p := range paths {
				fmt.Println(p)
			}
			return nil
		},
	}
	var yes bool
	purge := &cobra.Command{
		Use:   "purge",
		Short: "Delete every pre-delete snapshot",
		Long:  "Delete every pre-delete-profile-*.db snapshot. " + backupNote + " Only the user can run it.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := d.refuseNotOperator("purges backups of"); err != nil {
				return err
			}
			if !yes {
				return d.InvalidInput("purging deletes the snapshots for good: pass --yes")
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
			for _, p := range paths {
				if err := os.Remove(p); err != nil {
					return fmt.Errorf("remove %s: %w", p, err)
				}
			}
			if d.JSONOutput() {
				return printJSON(map[string]int{"removed": len(paths)})
			}
			fmt.Printf("Removed %d snapshot(s).\n", len(paths))
			return nil
		},
	}
	purge.Flags().BoolVar(&yes, "yes", false, "Confirm")
	cmd.AddCommand(list, purge)
	return cmd
}
