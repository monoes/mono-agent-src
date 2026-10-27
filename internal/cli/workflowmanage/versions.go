package workflowmanage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
	"github.com/spf13/cobra"
)

// VersionRow is one line of `workflow versions <id>`.
type VersionRow struct {
	Version     int       `json:"version"`
	Current     bool      `json:"current"`
	CreatedAt   time.Time `json:"created_at"`
	Nodes       int       `json:"nodes"`
	Connections int       `json:"connections"`
	Note        string    `json:"note"`
	Hash        string    `json:"definition_hash"`
}

// newWorkflowVersionsCmd lists a workflow's definition history and holds the
// show/restore/prune subcommands.
func (d Deps) newVersionsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "versions <workflow-id>",
		Short: "List a workflow's saved versions (show, restore and prune them with the subcommands)",
		Long: "Every save that changes a workflow's definition (name, description, nodes, " +
			"connections) records a version. Executions run the version they were created " +
			"against, so a waiting run resumes on the graph it started with. Versions are " +
			"kept until pruned with `workflow versions prune`.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return d.withOwnedWorkflow(args[0], func(ctx context.Context, store *workflow.HybridWorkflowStore, wf *workflow.Workflow) error {
				// Record the current definition first, so a workflow saved
				// before versions existed (or edited by hand) lists it.
				current, err := store.CaptureWorkflowVersion(ctx, wf.ID)
				if err != nil {
					return fmt.Errorf("record current version: %w", err)
				}
				versions, err := store.ListWorkflowVersions(ctx, wf.ID)
				if err != nil {
					return fmt.Errorf("list versions: %w", err)
				}
				rows := make([]VersionRow, 0, len(versions))
				for _, v := range versions {
					rows = append(rows, VersionRow{
						Version: v.Version, Current: v.Version == current, CreatedAt: v.CreatedAt,
						Nodes: len(v.Snapshot.Nodes), Connections: len(v.Snapshot.Connections),
						Note: v.Note, Hash: v.Hash,
					})
				}
				if d.JSONOutput() {
					return json.NewEncoder(os.Stdout).Encode(rows)
				}
				w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintln(w, "VERSION\tCREATED AT\tNODES\tCONNECTIONS\tNOTE")
				for _, r := range rows {
					mark := ""
					if r.Current {
						mark = " (current)"
					}
					fmt.Fprintf(w, "%d%s\t%s\t%d\t%d\t%s\n", r.Version, mark,
						r.CreatedAt.Local().Format("2006-01-02 15:04:05"), r.Nodes, r.Connections, r.Note)
				}
				return w.Flush()
			})
		},
	}
	cmd.AddCommand(
		d.newVersionsShowCmd(),
		d.newVersionsRestoreCmd(),
		d.newVersionsPruneCmd(),
	)
	return cmd
}

func (d Deps) newVersionsShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <workflow-id> <version>",
		Short: "Print one saved version of a workflow (its name, description, nodes and connections)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := d.parseVersionArg(args[1])
			if err != nil {
				return err
			}
			return d.withOwnedWorkflow(args[0], func(ctx context.Context, store *workflow.HybridWorkflowStore, wf *workflow.Workflow) error {
				v, err := d.lookupVersion(ctx, store, wf.ID, n)
				if err != nil {
					return err
				}
				if d.JSONOutput() {
					return json.NewEncoder(os.Stdout).Encode(v)
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(v)
			})
		},
	}
}

func (d Deps) newVersionsRestoreCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restore <workflow-id> <version>",
		Short: "Make a saved version the workflow's definition again (recorded as a new version)",
		Long: "Saves the workflow with the definition of the given version. History is kept: " +
			"the restore is recorded as a new version. Whether the workflow is active is unchanged. " +
			"A version that fails `workflow validate` is restored anyway, with a warning for each error.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := d.parseVersionArg(args[1])
			if err != nil {
				return err
			}
			return d.withOwnedWorkflow(args[0], func(ctx context.Context, store *workflow.HybridWorkflowStore, wf *workflow.Workflow) error {
				v, err := d.lookupVersion(ctx, store, wf.ID, n)
				if err != nil {
					return err
				}
				restored := v.Snapshot.Apply(wf)
				if err := store.SaveWorkflowWithNote(operator.ApprovingContext(ctx), restored, fmt.Sprintf("restored from version %d", n)); err != nil {
					return fmt.Errorf("restore version %d: %w", n, err)
				}
				// An old version may not pass today's checks: say so, but
				// restore it anyway.
				warnings := []string{}
				for _, e := range workflow.ValidateWorkflow(restored, workflow.ValidateOptions{}).Errors {
					warnings = append(warnings, "fails `workflow validate`: "+e)
				}
				if d.JSONOutput() {
					return json.NewEncoder(os.Stdout).Encode(map[string]any{
						"workflow_id": wf.ID, "restored_from": n, "version": restored.Version, "warnings": warnings,
					})
				}
				fmt.Fprintf(os.Stdout, "Restored workflow %s to version %d (now version %d)\n", wf.ID, n, restored.Version)
				for _, w := range warnings {
					fmt.Fprintln(os.Stderr, "Warning:", w)
				}
				return nil
			})
		},
	}
}

func (d Deps) newVersionsPruneCmd() *cobra.Command {
	var keep int
	var all bool
	cmd := &cobra.Command{
		Use:   "prune <workflow-id> | --all",
		Short: "Delete all but the newest --keep versions of a workflow (or of every workflow with --all)",
		Long: "Versions an unfinished (queued, running or waiting) execution is pinned to are " +
			"always kept, whatever --keep says.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if all == (len(args) == 1) {
				return d.InvalidInput("pass either a workflow id or --all")
			}
			if keep < 1 {
				return d.InvalidInput("--keep must be at least 1")
			}
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()
			store := d.WorkflowStore(db)
			ctx := context.Background()

			var ids []string
			if all {
				if ids, err = profileWorkflowIDs(ctx, db, d.ProfileID()); err != nil {
					return err
				}
			} else {
				if d.OwnedWorkflow(ctx, store, db.DB, d.ProfileID(), args[0]) == nil {
					return d.NotFound("workflow %q not found", args[0])
				}
				ids = []string{args[0]}
			}
			deleted := map[string]int64{}
			var total int64
			for _, id := range ids {
				n, err := store.PruneWorkflowVersions(ctx, id, keep)
				if err != nil {
					return err
				}
				if n > 0 {
					deleted[id] = n
				}
				total += n
			}
			if d.JSONOutput() {
				return json.NewEncoder(os.Stdout).Encode(map[string]any{"deleted": total, "by_workflow": deleted})
			}
			fmt.Fprintf(os.Stdout, "Deleted %d version(s)\n", total)
			return nil
		},
	}
	cmd.Flags().IntVar(&keep, "keep", 0, "Number of newest versions to keep per workflow (required, at least 1)")
	cmd.Flags().BoolVar(&all, "all", false, "Prune every workflow of the profile")
	_ = cmd.MarkFlagRequired("keep")
	return cmd
}

// withOwnedWorkflow opens the store and runs fn on workflow id when the
// active profile owns it; otherwise it reports not found.
func (d Deps) withOwnedWorkflow(id string, fn func(context.Context, *workflow.HybridWorkflowStore, *workflow.Workflow) error) error {
	db, err := d.OpenDB()
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	store := d.WorkflowStore(db)
	ctx := context.Background()
	wf := d.OwnedWorkflow(ctx, store, db.DB, d.ProfileID(), id)
	if wf == nil {
		return d.NotFound("workflow %q not found", id)
	}
	return fn(ctx, store, wf)
}

func (d Deps) parseVersionArg(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 0, d.InvalidInput("version must be a positive number, got %q", s)
	}
	return n, nil
}

func (d Deps) lookupVersion(ctx context.Context, store *workflow.HybridWorkflowStore, id string, n int) (*workflow.WorkflowVersion, error) {
	v, err := store.GetWorkflowVersion(ctx, id, n)
	if err != nil {
		return nil, fmt.Errorf("get version: %w", err)
	}
	if v == nil {
		return nil, d.NotFound("workflow %q has no version %d", id, n)
	}
	return v, nil
}

// profileWorkflowIDs lists the ids of the profile's workflows in SQLite,
// where versions live.
func profileWorkflowIDs(ctx context.Context, db *storage.Database, profileID string) ([]string, error) {
	rows, err := db.DB.QueryContext(ctx,
		`SELECT id FROM workflows WHERE profile_id = ?`, profileID)
	if err != nil {
		return nil, fmt.Errorf("list workflows: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("list workflows: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
