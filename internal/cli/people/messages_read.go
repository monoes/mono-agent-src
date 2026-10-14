package people

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Read state of inbound messages (migration 050). New inbound messages
// start unread; opening a conversation in the desktop app marks it read.

func (d Deps) newPeopleMessagesReadCmd() *cobra.Command {
	var person string
	cmd := &cobra.Command{
		Use:   "read [message-id...]",
		Short: "Mark inbound messages read — the given ones, or all of a person's with --person",
		Example: `  monoagentcli people messages read 3f2a…
  monoagentcli --json people messages read --person <person-id>`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if person == "" && len(args) == 0 {
				return d.InvalidInput("name message ids or pass --person")
			}
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()
			n, err := db.MarkPersonMessagesRead(d.ProfileID(), person, args)
			if err != nil {
				return err
			}
			if d.JSONOutput() {
				return writeJSONTo(cmd.OutOrStdout(), map[string]any{"marked_read": n})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Marked %d message(s) read.\n", n)
			return nil
		},
	}
	cmd.Flags().StringVar(&person, "person", "", "Mark every inbound message of this person read")
	return cmd
}

func (d Deps) newPeopleMessagesUnreadCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unread <message-id>",
		Short: "Mark an inbound message unread again",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()
			if err := db.MarkPersonMessageUnread(d.ProfileID(), args[0]); err != nil {
				return d.NotFound("%s", err.Error())
			}
			if d.JSONOutput() {
				return writeJSONTo(cmd.OutOrStdout(), map[string]any{"id": args[0], "unread": true})
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Marked unread.")
			return nil
		},
	}
}
