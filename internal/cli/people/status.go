package people

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/spf13/cobra"
)

// newPeopleStatusCmd returns the `people status` subcommand group: a
// manually-written, freeform status update per person (e.g. "Just closed the
// Q1 deal") — an append-only personal-CRM-style log, entirely unrelated to
// the draft/sent/failed status tracked by `people messages`.
func (d Deps) newPeopleStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Manage a person's status updates (freeform notes, e.g. \"Just closed the deal\")",
		Long:  "Post and read manually-written status updates for a person — an append-only personal log, unrelated to message send/draft status.",
	}

	cmd.AddCommand(
		d.newPeopleStatusSetCmd(),
		d.newPeopleStatusGetCmd(),
		d.newPeopleStatusHistoryCmd(),
	)

	return cmd
}

func (d Deps) newPeopleStatusSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "set <person-id> <text>",
		Short:   "Post a new status update for a person",
		Args:    cobra.ExactArgs(2),
		Example: `  monoagentcli people status set abc123 "Just closed the Q1 deal"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			update, err := db.AddPersonStatusUpdate(args[0], d.ProfileID(), args[1])
			if err != nil {
				return fmt.Errorf("saving status update: %w", err)
			}

			if d.JSONOutput() {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(update)
			}
			fmt.Fprintf(os.Stdout, "Posted status update %s for person %s.\n", update.ID, update.PersonID)
			return nil
		},
	}
}

func (d Deps) newPeopleStatusGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "get <person-id>",
		Short:   "Show a person's latest status update",
		Args:    cobra.ExactArgs(1),
		Example: `  monoagentcli people status get abc123`,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			update, err := db.GetLatestPersonStatusUpdate(args[0], d.ProfileID())
			if err != nil {
				return fmt.Errorf("getting status update: %w", err)
			}

			if d.JSONOutput() {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(update)
			}
			if update == nil {
				fmt.Println("No status set for this person.")
				return nil
			}
			fmt.Fprintf(os.Stdout, "%s\n(%s)\n", update.Text, update.CreatedAt.Format("2006-01-02 15:04:05"))
			return nil
		},
	}
}

func (d Deps) newPeopleStatusHistoryCmd() *cobra.Command {
	var limit int

	cmd := &cobra.Command{
		Use:   "history <person-id>",
		Short: "List every status update ever posted for a person, newest first",
		Args:  cobra.ExactArgs(1),
		Example: `  monoagentcli people status history abc123
  monoagentcli people status history abc123 --limit 10 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			updates, err := db.ListPersonStatusUpdates(args[0], d.ProfileID(), limit)
			if err != nil {
				return fmt.Errorf("listing status updates: %w", err)
			}
			if updates == nil {
				updates = []*storage.PersonStatusUpdate{}
			}

			if d.JSONOutput() {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(updates)
			}

			if len(updates) == 0 {
				fmt.Println("No status updates found.")
				return nil
			}

			table := d.NewTable(os.Stdout, []string{"ID", "Text", "Posted At"}, nil)
			for _, u := range updates {
				shortID := u.ID
				if len(shortID) > 8 {
					shortID = shortID[:8]
				}
				table.Append([]string{shortID, truncateStr(u.Text, 60), u.CreatedAt.Format("2006-01-02 15:04:05")})
			}
			table.Render()
			fmt.Fprintf(os.Stderr, "\nTotal: %d update(s)\n", len(updates))
			return nil
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum number of results (0 = unlimited)")

	return cmd
}
