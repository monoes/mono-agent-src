package people

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/spf13/cobra"
)

// newPeopleMessagesDraftsCmd lists draft messages awaiting confirmation —
// the same set shown in the GUI's Human in Loop page.
func (d Deps) newPeopleMessagesDraftsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "drafts",
		Short: "List draft messages awaiting confirmation (also shown in the GUI's Human in Loop page)",
		Example: `  monoagentcli people messages drafts
  monoagentcli people messages drafts --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			drafts, err := db.ListPersonMessagesByStatus(d.ProfileID(), "draft")
			if err != nil {
				return fmt.Errorf("listing drafts: %w", err)
			}
			if drafts == nil {
				drafts = []*storage.PersonMessageWithPerson{}
			}

			if d.JSONOutput() {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(drafts)
			}

			if len(drafts) == 0 {
				fmt.Println("No pending drafts.")
				return nil
			}

			table := d.NewTable(os.Stdout, []string{"ID", "To", "Subject", "Created"}, nil)
			for _, d := range drafts {
				to := d.PersonFullName
				if to == "" {
					to = d.PersonPlatformUsername
				}
				shortID := d.ID
				if len(shortID) > 8 {
					shortID = shortID[:8]
				}
				table.Append([]string{shortID, truncateStr(to, 24), truncateStr(d.Subject, 40), d.CreatedAt.Format("2006-01-02 15:04:05")})
			}
			table.Render()
			fmt.Fprintf(os.Stderr, "\nUse 'monoagentcli people messages send-draft <full-id>' or 'reject-draft <full-id>' (get the full ID with --json).\n")
			return nil
		},
	}
	return cmd
}

// newPeopleMessagesSendDraftCmd sends a previously-created draft as-is.
func (d Deps) newPeopleMessagesSendDraftCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "send-draft <message-id>",
		Short: "Send a previously-created draft message",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			msg, err := db.GetPersonMessage(args[0])
			if err != nil {
				return err
			}
			if msg == nil {
				return fmt.Errorf("message %s not found", args[0])
			}
			if err := assertMessageInProfile(db.DB, args[0], d.ProfileID()); err != nil {
				return err
			}
			if msg.Status != "draft" {
				return fmt.Errorf("message %s is not a draft (status: %s)", args[0], msg.Status)
			}
			connectionID, err := draftMessageConnectionID(msg)
			if err != nil {
				return err
			}

			config := map[string]interface{}{"operation": "send_draft", "message_id": msg.ExternalID}
			// Flip the draft to "sending" before the Graph call so a
			// concurrent send-draft/reject-draft on the same row sees a
			// non-draft and bails, instead of double-sending. Restore the
			// draft status if the send fails so it can be retried.
			if err := db.UpdatePersonMessageStatus(args[0], "sending"); err != nil {
				return fmt.Errorf("updating status: %w", err)
			}
			outputs, err := d.runOutlookNode(cmd, db.DB, connectionID, config)
			if err != nil {
				if rErr := db.UpdatePersonMessageStatus(args[0], "draft"); rErr != nil {
					return fmt.Errorf("%w (restoring draft status also failed: %v)", err, rErr)
				}
				return err
			}

			if err := db.UpdatePersonMessageStatus(args[0], "sent"); err != nil {
				return fmt.Errorf("updating status: %w", err)
			}
			// Graph reassigns a new message id when a draft is sent (moved
			// into Sent Items), so the stored external_id must be updated to
			// stay valid for a later reply/get_message/delete_message.
			msg.Status = "sent"
			if len(outputs) > 0 && len(outputs[0].Items) > 0 {
				if newID := getStr(outputs[0].Items[0].JSON, "message_id"); newID != "" && newID != msg.ExternalID {
					if err := db.UpdatePersonMessageExternalID(args[0], newID); err != nil {
						return fmt.Errorf("updating external id: %w", err)
					}
					msg.ExternalID = newID
				}
			}
			if d.JSONOutput() {
				return printReviewJSON(msg)
			}
			fmt.Fprintf(os.Stdout, "Sent draft %s.\n", args[0])
			return nil
		},
	}
	return cmd
}

// newPeopleMessagesRejectDraftCmd discards a draft: best-effort removes it
// from the mailbox's Drafts folder too, then deletes the local history row.
func (d Deps) newPeopleMessagesRejectDraftCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reject-draft <message-id>",
		Short: "Discard a draft message",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			msg, err := db.GetPersonMessage(args[0])
			if err != nil {
				return err
			}
			if msg == nil {
				return fmt.Errorf("message %s not found", args[0])
			}
			if err := assertMessageInProfile(db.DB, args[0], d.ProfileID()); err != nil {
				return err
			}
			// Only drafts may be discarded: without this guard reject-draft
			// would delete ANY message row (and best-effort delete the mail
			// itself) — a synced inbox message is not a draft to reject.
			if msg.Status != "draft" {
				return fmt.Errorf("message %s is not a draft (status: %s)", args[0], msg.Status)
			}
			if connectionID, cErr := draftMessageConnectionID(msg); cErr == nil && msg.ExternalID != "" {
				config := map[string]interface{}{"operation": "delete_message", "message_id": msg.ExternalID}
				_, _ = d.runOutlookNode(cmd, db.DB, connectionID, config) // best-effort
			}

			if err := db.DeletePersonMessage(args[0]); err != nil {
				return fmt.Errorf("deleting message: %w", err)
			}
			if d.JSONOutput() {
				return printReviewJSON(map[string]string{"id": args[0], "status": "discarded"})
			}
			fmt.Fprintf(os.Stdout, "Discarded draft %s.\n", args[0])
			return nil
		},
	}
	return cmd
}

// assertMessageInProfile fails if the message does not belong to the active
// profile. GetPersonMessage/UpdatePersonMessageStatus/DeletePersonMessage look
// up by bare ID with no profile predicate, so without this check a draft owned
// by one profile could be sent or discarded from another.
func assertMessageInProfile(db *sql.DB, msgID, profileID string) error {
	if profileID == "" {
		profileID = "default"
	}
	var owner string
	err := db.QueryRow("SELECT profile_id FROM person_messages WHERE id = ?", msgID).Scan(&owner)
	if err == sql.ErrNoRows {
		return fmt.Errorf("message %s not found", msgID)
	}
	if err != nil {
		return err
	}
	if owner != profileID {
		return fmt.Errorf("message %s not found", msgID)
	}
	return nil
}

// draftMessageConnectionID extracts the connection_id stashed in a draft
// message's Metadata by newPeopleMessagesComposeCmd.
func draftMessageConnectionID(msg *storage.PersonMessage) (string, error) {
	var meta struct {
		ConnectionID string `json:"connection_id"`
	}
	if msg.Metadata == "" {
		return "", fmt.Errorf("message %s has no associated connection", msg.ID)
	}
	if err := json.Unmarshal([]byte(msg.Metadata), &meta); err != nil || meta.ConnectionID == "" {
		return "", fmt.Errorf("message %s has no associated connection", msg.ID)
	}
	return meta.ConnectionID, nil
}
