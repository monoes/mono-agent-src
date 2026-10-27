package workflowmanage

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"

	"github.com/monoes/mono-agent/internal/daemonhb"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/summary"
	"github.com/monoes/mono-agent/internal/workflow"
	"github.com/spf13/cobra"
)

// newWorkflowListCmd lists all workflows.
func (d Deps) newListCmd() *cobra.Command {
	var jsonOut bool
	var allProfiles bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all workflows",
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()

			store := d.WorkflowStore(db)
			ctx := context.Background()

			if allProfiles {
				return printAllProfilesWorkflows(ctx, db, store, jsonOut || d.JSONOutput())
			}
			workflows, err := profileWorkflows(ctx, store, d.ProfileID())
			if err != nil {
				return fmt.Errorf("list workflows: %w", err)
			}

			if jsonOut || d.JSONOutput() {
				// One grouped count for every row; a failure costs the
				// counts, not the list.
				counts, err := store.NodeCounts(ctx, d.ProfileID())
				if err != nil {
					fmt.Fprintf(os.Stderr, "warning: could not count workflow nodes: %v\n", err)
				}
				type listed struct {
					workflow.Workflow
					NodeCount int `json:"node_count"`
				}
				rows := make([]listed, 0, len(workflows))
				for _, wf := range workflows {
					rows = append(rows, listed{Workflow: wf, NodeCount: counts[wf.ID]})
				}
				return json.NewEncoder(os.Stdout).Encode(rows)
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tNAME\tACTIVE\tVERSION\tUPDATED AT")
			for _, wf := range workflows {
				active := "false"
				if wf.IsActive {
					active = "true"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\n",
					wf.ID, wf.Name, active, wf.Version,
					wf.UpdatedAt.Format(time.RFC3339),
				)
			}
			return w.Flush()
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output in JSON format")
	cmd.Flags().BoolVar(&allProfiles, "all-profiles", false, "List every profile's workflows, each tagged with its profile")
	return cmd
}

// newWorkflowGetCmd prints a single workflow as JSON.
func (d Deps) newGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <id>",
		Short: "Print a workflow as JSON",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()

			store := d.WorkflowStore(db)
			ctx := context.Background()

			wf, err := store.GetWorkflow(ctx, args[0])
			if err != nil {
				return fmt.Errorf("get workflow: %w", err)
			}
			if wf == nil || d.OwnedWorkflow(ctx, store, db.DB, d.ProfileID(), args[0]) == nil {
				return d.NotFound("workflow %q not found", args[0])
			}

			// review_hash is what `workflow activate --expect` takes: the hash of exactly this
			// definition, so an approval can be given for what was read here.
			hash, err := workflow.ReviewHashOf(ctx, store, wf)
			if err != nil {
				return fmt.Errorf("review hash: %w", err)
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(struct {
				*workflow.Workflow
				ReviewHash string `json:"review_hash"`
			}{wf, hash})
		},
	}
}

// newWorkflowActivateCmd enables a workflow and registers its triggers.
func (d Deps) newActivateCmd() *cobra.Command {
	var expect string
	c := &cobra.Command{
		Use:   "activate <id>",
		Short: "Activate a workflow and start its triggers",
		Long: "Activate a workflow and start its triggers.\n\n" +
			"Schedule and webhook triggers fire from `monoagentcli daemon`. A running daemon picks " +
			"the activation up within a few seconds — no restart needed — and it also follows later " +
			"edits, deactivation and deletion. Without a daemon running, nothing fires until one starts.\n\n" +
			"Activating is also how you approve a workflow an agent or the chat wrote: the approval is " +
			"recorded in the same step as the flag, for the definition as it is when this command reads it. " +
			"A change made after that is refused, not approved. Pass --expect with the review_hash that " +
			"`workflow get <id> --json` printed to approve exactly what you looked at.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			engine, closeBrowsers, err := d.BuildEngine(false)
			if err != nil {
				return fmt.Errorf("build engine: %w", err)
			}
			defer closeBrowsers()

			ctx := context.Background()
			if err := engine.Start(ctx); err != nil {
				return fmt.Errorf("start engine: %w", err)
			}
			defer engine.Stop() //nolint:errcheck

			// The operator's approval rides the activation itself: one publication writes the flag and the
			// approval, and only for the definition this command read (or --expect named).
			if err := engine.ActivateWorkflowWith(operator.ApprovingContext(ctx), args[0], workflow.ActivateOptions{ExpectHash: expect}); err != nil {
				if errors.Is(err, workflow.ErrChangedSincePreview) {
					return fmt.Errorf("activate workflow: %w: nothing was activated or approved; look at it again with `workflow get %s --json` and pass the review_hash it prints to --expect", err, args[0])
				}
				return fmt.Errorf("activate workflow: %w", err)
			}
			if d.JSONOutput() {
				return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{"id": args[0], "is_active": true})
			}

			fmt.Fprintf(os.Stdout, "Workflow %s activated.\n", args[0])
			if _, live := daemonhb.Read(); live {
				fmt.Fprintln(os.Stdout, "The running daemon starts this workflow's triggers within a few seconds.")
			} else {
				fmt.Fprintln(os.Stdout, "Note: no daemon is running. Start `monoagentcli daemon` (kept running) for this workflow's schedule and webhook triggers to fire.")
			}
			return nil
		},
	}
	c.Flags().StringVar(&expect, "expect", "", "Activate only if the workflow still has this review_hash (printed by workflow get --json): approve exactly what you reviewed")
	return c
}

// newWorkflowDeactivateCmd disables a workflow and unregisters its triggers.
func (d Deps) newDeactivateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "deactivate <id>",
		Short: "Deactivate a workflow and stop its triggers",
		Long: "Deactivate a workflow and stop its triggers. A running `monoagentcli daemon` " +
			"stops firing them within a few seconds — no restart needed.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			engine, closeBrowsers, err := d.BuildEngine(false)
			if err != nil {
				return fmt.Errorf("build engine: %w", err)
			}
			defer closeBrowsers()

			ctx := context.Background()
			if err := engine.Start(ctx); err != nil {
				return fmt.Errorf("start engine: %w", err)
			}
			defer engine.Stop() //nolint:errcheck

			if err := engine.DeactivateWorkflow(ctx, args[0]); err != nil {
				return fmt.Errorf("deactivate workflow: %w", err)
			}
			unfinished := engine.UnfinishedChange(ctx, args[0])
			if d.JSONOutput() {
				out := map[string]interface{}{"id": args[0], "is_active": false}
				if unfinished != "" {
					out["file_left_as_is"] = true
					out["unfinished_change"] = unfinished
				}
				return json.NewEncoder(os.Stdout).Encode(out)
			}

			fmt.Fprintf(os.Stdout, "Workflow %s deactivated.\n", args[0])
			if unfinished != "" {
				fmt.Fprintln(os.Stdout, "Deactivated in the database; the file was left as it is. Run `monoagentcli doctor` to settle the unfinished change.")
			}
			return nil
		},
	}
}

// newWorkflowDeleteCmd deletes a workflow (with confirmation unless --force).
func (d Deps) newDeleteCmd() *cobra.Command {
	var force, yes bool

	cmd := &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a workflow",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			workflowID := args[0]

			// Fail fast for unknown ids with the standard not-found sentinel
			// (exit 2), before the confirmation prompt or the engine (with
			// its browser/trigger machinery) spins up.
			preDB, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			store := d.WorkflowStore(preDB)
			existing, gerr := store.GetWorkflow(context.Background(), workflowID)
			if existing != nil && d.OwnedWorkflow(context.Background(), store, preDB.DB, d.ProfileID(), workflowID) == nil {
				existing = nil // another profile's: not found here, and its grants untouched
			}
			closeErr := preDB.Close()
			if gerr != nil {
				return fmt.Errorf("get workflow: %w", gerr)
			}
			if existing == nil {
				return d.NotFound("workflow %q not found", workflowID)
			}
			if closeErr != nil {
				return fmt.Errorf("close database: %w", closeErr)
			}
			if err := d.CheckOrgReferences(cmd.Context(), workflowID, force); err != nil {
				return err
			}

			if !force && !yes {
				fmt.Fprintf(os.Stdout, "Delete workflow %q? This is irreversible. [y/N] ", workflowID)
				reader := bufio.NewReader(os.Stdin)
				answer, _ := reader.ReadString('\n')
				answer = strings.TrimSpace(strings.ToLower(answer))
				if answer != "y" && answer != "yes" {
					fmt.Fprintln(os.Stdout, "Aborted.")
					return nil
				}
			}

			engine, closeBrowsers, err := d.BuildEngine(false)
			if err != nil {
				return fmt.Errorf("build engine: %w", err)
			}
			defer closeBrowsers()

			ctx := context.Background()
			if err := engine.Start(ctx); err != nil {
				return fmt.Errorf("start engine: %w", err)
			}
			defer engine.Stop() //nolint:errcheck

			if err := engine.DeleteWorkflow(ctx, workflowID); err != nil {
				return fmt.Errorf("delete workflow: %w", err)
			}
			if d.JSONOutput() {
				return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{"id": workflowID, "deleted": true})
			}

			fmt.Fprintf(os.Stdout, "Workflow %s deleted.\n", workflowID)
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Skip confirmation prompt, and revoke org grants and automation roles that use the workflow")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip confirmation prompt (still refuses a workflow that orgs use)")
	return cmd
}

// newWorkflowExecutionsCmd lists recent executions for a workflow.
func (d Deps) newExecutionsCmd() *cobra.Command {
	var limit int
	var jsonOut bool
	var all bool
	var allProfiles bool

	cmd := &cobra.Command{
		Use:   "executions <workflow-id> | --all",
		Short: "List recent executions for a workflow, or across the profile with --all",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if all == (len(args) == 1) {
				return d.InvalidInput("pass either a workflow id or --all")
			}
			if allProfiles && !all {
				return d.InvalidInput("--all-profiles goes with --all")
			}
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()

			if all {
				n := limit
				if n <= 0 {
					n = -1 // SQLite: no limit
				}
				var rows []summary.ExecRow
				if allProfiles {
					rows, err = allProfilesRecentExecutions(cmd.Context(), db.DB, n)
				} else {
					rows, err = summary.RecentExecutions(cmd.Context(), db.DB, d.ProfileID(), n)
				}
				if err != nil {
					return fmt.Errorf("list executions: %w", err)
				}
				ids := make([]string, len(rows))
				for i, r := range rows {
					ids[i] = r.ID
				}
				if rr := replyRedactor(cmd.Context(), db.DB, ids...); rr != nil {
					for i := range rows {
						rows[i].Error = rr.String(rows[i].Error)
					}
				}
				if jsonOut || d.JSONOutput() {
					return json.NewEncoder(os.Stdout).Encode(rows)
				}
				w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintln(w, "ID\tWORKFLOW\tSTATUS\tTRIGGER TYPE\tCREATED AT\tERROR")
				for _, r := range rows {
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.WorkflowName, r.Status, r.TriggerType, r.CreatedAt, r.Error)
				}
				return w.Flush()
			}

			store := d.WorkflowStore(db)
			ctx := context.Background()

			// Load the workflow and enforce the profile boundary before
			// listing executions — otherwise an ID guessed from another
			// profile leaks that profile's execution history here.
			wf, err := store.GetWorkflow(ctx, args[0])
			if err != nil {
				return fmt.Errorf("get workflow: %w", err)
			}
			if wf == nil {
				return d.NotFound("workflow %q not found", args[0])
			}
			if wf.ProfileID != "" && wf.ProfileID != d.ProfileID() {
				return d.NotFound("workflow %q not found", args[0])
			}

			executions, err := store.ListExecutions(ctx, args[0], limit)
			if err != nil {
				return fmt.Errorf("list executions: %w", err)
			}
			if executions == nil {
				executions = []workflow.WorkflowExecution{}
			}
			ids := make([]string, len(executions))
			for i, e := range executions {
				ids[i] = e.ID
			}
			if rr := replyRedactor(ctx, db.DB, ids...); rr != nil {
				for i := range executions {
					rr.Execution(&executions[i])
				}
			}

			if jsonOut || d.JSONOutput() {
				return json.NewEncoder(os.Stdout).Encode(executions)
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tSTATUS\tTRIGGER TYPE\tSTARTED AT\tFINISHED AT\tERROR")
			for _, e := range executions {
				startedAt := ""
				if e.StartedAt != nil {
					startedAt = e.StartedAt.Format(time.RFC3339)
				}
				finishedAt := ""
				if e.FinishedAt != nil {
					finishedAt = e.FinishedAt.Format(time.RFC3339)
				}
				errStr := e.ErrorMessage
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
					e.ID, e.Status, e.TriggerType,
					startedAt, finishedAt, errStr,
				)
			}
			return w.Flush()
		},
	}

	cmd.Flags().IntVar(&limit, "limit", 20, "Maximum number of executions to show (0 = all)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output in JSON format")
	cmd.Flags().BoolVar(&all, "all", false, "List recent executions across every workflow in the profile, newest first")
	cmd.Flags().BoolVar(&allProfiles, "all-profiles", false, "With --all: every profile's runs, tagged with their profile")
	return cmd
}

// replyRedactor hides reply tokens in a run's error and trigger data from a
// marked process (workflow.ReplyRedactor); the user's own terminal gets nil.
func replyRedactor(ctx context.Context, db *sql.DB, executionIDs ...string) *workflow.ReplyRedactor {
	if operator.Marker() == "" {
		return nil
	}
	return workflow.NewReplyRedactor(ctx, db, executionIDs...)
}

// newWorkflowCreateCmd creates a blank workflow and prints its ID.
func (d Deps) newCreateCmd() *cobra.Command {
	var description string
	var active bool

	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new blank workflow",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()

			now := time.Now().UTC()
			wf := &workflow.Workflow{
				ID:          uuid.New().String(),
				Name:        args[0],
				Description: description,
				IsActive:    active,
				Version:     1,
				ProfileID:   d.ProfileID(),
				CreatedAt:   now,
				UpdatedAt:   now,
			}

			store := d.WorkflowStore(db)
			ctx := context.Background()
			// Same persistence as import: file store plus the SQLite rows
			// that `workflow run --json` and executions read.
			if err := d.CreateWorkflow(operator.ApprovingContext(ctx), store, wf, false); err != nil {
				return fmt.Errorf("create workflow: %w", err)
			}

			if d.JSONOutput() {
				return json.NewEncoder(os.Stdout).Encode(wf)
			}
			fmt.Fprintf(os.Stdout, "Created workflow: %s  (id: %s)\n", wf.Name, wf.ID)
			return nil
		},
	}

	cmd.Flags().StringVar(&description, "description", "", "Workflow description")
	cmd.Flags().BoolVar(&active, "active", false, "Mark workflow as active immediately")
	return cmd
}
