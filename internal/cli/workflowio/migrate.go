package workflowio

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/monoes/mono-agent/internal/workflow"
	"github.com/spf13/cobra"
)

// newWorkflowMigrateCmd migrates workflows from SQLite to JSON files.
func (d Deps) newMigrateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Migrate workflows from SQLite to JSON files (acts on every profile)",
		Long: "Copies the SQLite workflows of every profile to the JSON file store. " +
			"It acts on all profiles at once, so it takes no --profile.",
		RunE: d.runWorkflowMigrate(),
	}
}

func (d Deps) runWorkflowMigrate() func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		db, err := d.OpenDB()
		if err != nil {
			return fmt.Errorf("open sqlite store: %w", err)
		}
		defer db.Close()

		sqliteStore := workflow.NewSQLiteWorkflowStore(db.DB)

		home, err := os.UserHomeDir()
		if err != nil {
			home = os.Getenv("HOME")
		}
		wfDir := filepath.Join(home, ".monoagent", "workflows")
		fileStore, err := workflow.NewWorkflowFileStore(wfDir)
		if err != nil {
			return fmt.Errorf("open file store: %w", err)
		}

		workflows, err := sqliteStore.ListAllProfilesWorkflows(ctx)
		if err != nil {
			return fmt.Errorf("list workflows from sqlite: %w", err)
		}

		fmt.Printf("Found %d workflows in SQLite. Migrating to %s...\n", len(workflows), wfDir)

		var migrated, skipped int
		for _, wf := range workflows {
			full, err := sqliteStore.GetWorkflow(ctx, wf.ID)
			if err != nil {
				fmt.Printf("  SKIP %s (%s): load error: %v\n", wf.ID, wf.Name, err)
				skipped++
				continue
			}
			if full == nil {
				skipped++
				continue
			}

			for i, n := range full.Nodes {
				if n.Schema == nil {
					schema, err := workflow.LoadDefaultSchema(n.Type)
					if err != nil {
						schema = &workflow.NodeSchema{Fields: []workflow.NodeSchemaField{}}
					}
					full.Nodes[i].Schema = schema
				}
			}

			// A file that is already there is the canonical copy and may be mid-change: leave it.
			if _, exists, rerr := fileStore.ReadRawForMigrate(full.ID); exists || rerr != nil { // a file that cannot be read is not an absent one
				fmt.Printf("  SKIP %s (%s): a workflow file already exists\n", full.ID, full.Name)
				skipped++
				continue
			}
			if err := fileStore.SaveWorkflow(ctx, full); err != nil {
				fmt.Printf("  SKIP %s (%s): write error: %v\n", wf.ID, wf.Name, err)
				skipped++
				continue
			}
			fmt.Printf("  OK   %s (%s)\n", full.ID, full.Name)
			migrated++
		}

		fmt.Printf("\nMigration complete: %d migrated, %d skipped.\n", migrated, skipped)
		fmt.Println("SQLite data was NOT modified — safe to roll back.")
		return nil
	}
}
