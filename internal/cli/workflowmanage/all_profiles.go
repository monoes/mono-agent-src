package workflowmanage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/monoes/mono-agent/internal/profiledir"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/summary"
	"github.com/monoes/mono-agent/internal/workflow"
)

// The All profiles view's lists. Each runs the ordinary per-profile read
// once per profile and tags the rows, so no query learns a new scope.

// profileWorkflows is one profile's own workflows. The store's file half
// has no profile filter, so the COALESCE(profile_id,'default') rule its
// SQLite half uses is applied here.
func profileWorkflows(ctx context.Context, store *workflow.HybridWorkflowStore, profileID string) ([]workflow.Workflow, error) {
	all, err := store.ListWorkflows(ctx, profileID)
	if err != nil {
		return nil, err
	}
	out := make([]workflow.Workflow, 0, len(all))
	for _, wf := range all {
		owner := wf.ProfileID
		if owner == "" {
			owner = "default"
		}
		if owner == profileID {
			out = append(out, wf)
		}
	}
	return out, nil
}

// allProfilesWorkflow is one row of `workflow list --all-profiles --json`.
type allProfilesWorkflow struct {
	workflow.Workflow
	NodeCount   int    `json:"node_count"`
	ProfileName string `json:"profile_name"`
}

func printAllProfilesWorkflows(ctx context.Context, db *storage.Database, store *workflow.HybridWorkflowStore, asJSON bool) error {
	profiles, err := profiledir.List(ctx, db.DB)
	if err != nil {
		return fmt.Errorf("list profiles: %w", err)
	}
	rows := []allProfilesWorkflow{}
	for _, p := range profiles {
		wfs, err := profileWorkflows(ctx, store, p.ID)
		if err != nil {
			return fmt.Errorf("list workflows of %s: %w", p.Name, err)
		}
		counts, err := store.NodeCounts(ctx, p.ID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not count workflow nodes of %s: %v\n", p.Name, err)
		}
		for _, wf := range wfs {
			if wf.ProfileID == "" {
				wf.ProfileID = p.ID
			}
			rows = append(rows, allProfilesWorkflow{Workflow: wf, NodeCount: counts[wf.ID], ProfileName: p.Name})
		}
	}
	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(rows)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PROFILE\tID\tNAME\tACTIVE\tUPDATED AT")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%t\t%s\n", r.ProfileName, r.ID, r.Name, r.IsActive, r.UpdatedAt.Format(time.RFC3339))
	}
	return w.Flush()
}

// allProfilesRecentExecutions is `workflow executions --all --all-profiles`:
// each profile's newest runs, tagged, merged newest first, at most limit
// (limit < 0: no limit).
func allProfilesRecentExecutions(ctx context.Context, db *sql.DB, limit int) ([]summary.ExecRow, error) {
	profiles, err := profiledir.List(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("list profiles: %w", err)
	}
	rows := []summary.ExecRow{}
	for _, p := range profiles {
		got, err := summary.RecentExecutions(ctx, db, p.ID, limit)
		if err != nil {
			return nil, fmt.Errorf("executions of %s: %w", p.Name, err)
		}
		for _, r := range got {
			r.ProfileID, r.ProfileName = p.ID, p.Name
			rows = append(rows, r)
		}
	}
	return summary.SortRecent(rows, limit), nil
}
