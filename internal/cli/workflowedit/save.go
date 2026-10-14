package workflowedit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/monoes/mono-agent/internal/workflow"
	"github.com/spf13/cobra"
)

// newWorkflowSaveCmd saves a whole workflow document — the shape `workflow
// get` prints (nodes with node_type/position_x/position_y, connections with
// source_node_id/target_node_id) — creating it when it has no id and
// replacing its definition when it has one. This is the desktop editor's
// save: unlike `import` it never remaps ids or dedupes against earlier
// imports, because the editor owns the ids it sends.
func (d Deps) newSaveCmd() *cobra.Command {
	var inputFile string

	cmd := &cobra.Command{
		Use:   "save",
		Short: "Create or update a workflow from a full workflow document (--file or stdin)",
		Long: "Reads a workflow document in the shape `workflow get` prints and saves it: " +
			"a document without an id creates a workflow, one with an id replaces that " +
			"workflow's name, description, nodes and connections. Saving an existing " +
			"workflow never changes whether it is active — use activate/deactivate.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var raw []byte
			var err error
			if inputFile != "" && inputFile != "-" {
				raw, err = os.ReadFile(inputFile)
			} else {
				raw, err = io.ReadAll(cmd.InOrStdin())
			}
			if err != nil {
				if os.IsNotExist(err) {
					return d.NotFound("read input: %v", err)
				}
				return fmt.Errorf("read input: %w", err)
			}
			var doc workflow.Workflow
			if err := json.Unmarshal(raw, &doc); err != nil {
				return d.InvalidInput("parse workflow document: %v", err)
			}

			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()
			store := d.WorkflowStore(db)
			ctx := context.Background()

			wf, err := d.SaveDocument(ctx, store, d.ProfileID(), doc)
			if err != nil {
				return err
			}
			// ensureSQLWorkflow's counterpart for this path: the file is
			// canonical, but executions and profile scoping need a metadata
			// row tagged with the saving profile, even when mirroring was
			// skipped (ids shared with another workflow).
			_, _ = db.DB.ExecContext(ctx, `INSERT OR IGNORE INTO workflows
				(id, name, description, is_active, version, profile_id, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				wf.ID, wf.Name, wf.Description, wf.IsActive, wf.Version, d.ProfileID(),
				wf.CreatedAt.UTC().Format(time.RFC3339), wf.UpdatedAt.UTC().Format(time.RFC3339))
			_, _ = db.DB.ExecContext(ctx, `UPDATE workflows SET profile_id = ? WHERE id = ?`, d.ProfileID(), wf.ID)

			if d.JSONOutput() {
				summary := *wf
				summary.Nodes, summary.Connections = nil, nil
				return json.NewEncoder(os.Stdout).Encode(summary)
			}
			fmt.Fprintf(os.Stdout, "Saved workflow: %s  (id: %s)\n", wf.Name, wf.ID)
			return nil
		},
	}

	cmd.Flags().StringVarP(&inputFile, "file", "f", "", "Path to the JSON document, or - for stdin (default: stdin)")
	return cmd
}

// SaveDocument persists doc for profileID. An id owned by another
// profile is reported as not found, like every other profile-scoped lookup.
// For an existing workflow the stored activation, version and creation time
// win over the document's: the editor sends none of them, and honouring
// its zero values deactivated the workflow on every auto-save-before-run.
// Nodes without a schema get their type's default one.
func (d Deps) SaveDocument(ctx context.Context, store *workflow.HybridWorkflowStore, profileID string, doc workflow.Workflow) (*workflow.Workflow, error) {
	wf := &workflow.Workflow{
		ID:          doc.ID,
		Name:        doc.Name,
		Description: doc.Description,
		IsActive:    doc.IsActive,
		ProfileID:   profileID,
	}
	if doc.ID != "" {
		existing := d.OwnedWorkflow(ctx, store, store.RawDB(), profileID, doc.ID)
		if existing == nil {
			if other, _ := store.GetWorkflow(ctx, doc.ID); other != nil {
				return nil, d.NotFound("workflow %q not found", doc.ID)
			}
		} else {
			wf.IsActive = existing.IsActive
			wf.Version = existing.Version
			wf.CreatedAt = existing.CreatedAt
		}
	}
	for _, n := range doc.Nodes {
		if n.Schema == nil {
			n.Schema, _ = workflow.LoadDefaultSchema(n.Type)
		}
		n.WorkflowID = ""
		wf.Nodes = append(wf.Nodes, n)
	}
	for _, c := range doc.Connections {
		c.WorkflowID = ""
		wf.Connections = append(wf.Connections, c)
	}
	if err := store.SaveWorkflow(ctx, wf); err != nil {
		return nil, fmt.Errorf("save workflow: %w", err)
	}
	return wf, nil
}
