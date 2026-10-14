package people

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/connections"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
	"github.com/spf13/cobra"
)

// newPeopleMessagesComposeCmd sends (or drafts) an email to a person via
// service.outlook_mail and records the result on their message history —
// the CLI equivalent of the GUI's Compose button on a person's Profile page.
func (d Deps) newPeopleMessagesComposeCmd() *cobra.Command {
	var (
		connectionID string
		subject      string
		body         string
		bodyType     string
		asDraft      bool
		toOverride   string
		cc           string
		bcc          string
		replyTo      string
		replyAll     bool
	)

	cmd := &cobra.Command{
		Use:   "compose <person-id>",
		Short: "Send or save-as-draft an email to a person, recorded on their message history",
		Args:  cobra.ExactArgs(1),
		Example: `  monoagentcli people messages compose abc123 --connection outlook --subject "Hi" --body "Thanks for reaching out"
  monoagentcli people messages compose abc123 --connection outlook --subject "Hi" --body "..." --draft
  monoagentcli people messages compose abc123 --connection outlook --subject "Hi" --body "..." --to "a@x.com,b@x.com"
  monoagentcli people messages compose abc123 --connection outlook --subject "Hi" --body "..." --cc "c@x.com,d@x.com"
  monoagentcli people messages compose abc123 --connection outlook --body "Sounds good" --reply-to <message-id> --reply-all`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if body == "" {
				return fmt.Errorf("--body is required")
			}

			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			// --reply-to threads this compose onto an existing conversation
			// via Graph's createReply/createReplyAll instead of starting a
			// fresh conversationId.
			if replyTo != "" {
				orig, err := db.GetPersonMessage(replyTo)
				if err != nil {
					return err
				}
				if orig == nil {
					return fmt.Errorf("message %s not found", replyTo)
				}
				if err := assertMessageInProfile(db.DB, replyTo, d.ProfileID()); err != nil {
					return err
				}
				if orig.ExternalID == "" {
					return fmt.Errorf("message %s has no associated Outlook message id, cannot reply", replyTo)
				}

				operation, status := "reply", "sent"
				if asDraft {
					operation, status = "create_reply", "draft"
				}

				config := map[string]interface{}{
					"operation":  operation,
					"message_id": orig.ExternalID,
					"reply_all":  replyAll,
					"body":       body,
				}
				if toOverride != "" {
					config["to"] = toOverride
				}
				if cc != "" {
					config["cc"] = cc
				}
				if bcc != "" {
					config["bcc"] = bcc
				}
				outputs, err := d.runOutlookNode(cmd, db.DB, connectionID, config)
				if err != nil {
					return err
				}

				externalID := outlookResultMessageID(outputs)
				metaBytes, _ := json.Marshal(map[string]string{"connection_id": connectionID})

				replySubject := orig.Subject
				if replySubject != "" && replySubject[:min(4, len(replySubject))] != "Re: " {
					replySubject = "Re: " + replySubject
				}

				msg := &storage.PersonMessage{
					PersonID:   args[0],
					Source:     "outlook",
					ExternalID: externalID,
					Direction:  "outbound",
					// Outbound sender is the connected account, never the
					// counterparty (orig.Sender) — left empty here; the true
					// from-address is recorded when the sent mail syncs back
					// via people.sync_outlook_message.
					Sender:   "",
					Subject:  replySubject,
					Body:     body,
					Metadata: string(metaBytes),
					Status:   status,
					SentAt:   time.Now().UTC(),
				}
				if err := db.UpsertPersonMessage(msg, d.ProfileID()); err != nil {
					return fmt.Errorf("saving message: %w", err)
				}

				if d.JSONOutput() {
					return json.NewEncoder(os.Stdout).Encode(msg)
				}
				verb := "Sent"
				if asDraft {
					verb = "Drafted"
				}
				what := "reply"
				if replyAll {
					what = "reply-all"
				}
				fmt.Fprintf(os.Stdout, "%s %s to message %s (new message id: %s)\n", verb, what, replyTo, msg.ID)
				return nil
			}

			var toAddr string
			if err := db.DB.QueryRow(
				`SELECT platform_username FROM people WHERE id = ? AND profile_id = ?`,
				args[0], d.ProfileID(),
			).Scan(&toAddr); err != nil {
				return fmt.Errorf("person %s not found in profile %s: %w", args[0], d.ProfileID(), err)
			}
			if toOverride != "" {
				toAddr = toOverride
			}
			// platform_username holds whatever handle the contact was created
			// with — often not an email address. Graph needs a real address,
			// so refuse to "send" to a non-address instead of silently
			// bouncing.
			if !strings.Contains(toAddr, "@") {
				return fmt.Errorf("contact has no email address — set one or pass --to")
			}

			operation, status := "send_message", "sent"
			if asDraft {
				operation, status = "create_draft", "draft"
			}
			if bodyType == "" {
				bodyType = "html"
			}

			config := map[string]interface{}{
				"operation": operation,
				"to":        toAddr,
				"subject":   subject,
				"body":      body,
				"body_type": bodyType,
			}
			if cc != "" {
				config["cc"] = cc
			}
			if bcc != "" {
				config["bcc"] = bcc
			}
			outputs, err := d.runOutlookNode(cmd, db.DB, connectionID, config)
			if err != nil {
				return err
			}

			var externalID string
			if len(outputs) > 0 && len(outputs[0].Items) > 0 {
				externalID = getStr(outputs[0].Items[0].JSON, "id")
			}
			metaBytes, _ := json.Marshal(map[string]string{"connection_id": connectionID})

			msg := &storage.PersonMessage{
				PersonID:   args[0],
				Source:     "outlook",
				ExternalID: externalID,
				Direction:  "outbound",
				// The sender is the connected account, not the recipient
				// (toAddr) — left empty here; the true from-address is
				// recorded when the sent mail syncs back via
				// people.sync_outlook_message.
				Sender:   "",
				Subject:  subject,
				Body:     body,
				Metadata: string(metaBytes),
				Status:   status,
				SentAt:   time.Now().UTC(),
			}
			if err := db.UpsertPersonMessage(msg, d.ProfileID()); err != nil {
				return fmt.Errorf("saving message: %w", err)
			}

			if d.JSONOutput() {
				return json.NewEncoder(os.Stdout).Encode(msg)
			}
			verb := "Sent"
			if asDraft {
				verb = "Drafted"
			}
			fmt.Fprintf(os.Stdout, "%s message to %s (message id: %s)\n", verb, toAddr, msg.ID)
			return nil
		},
	}

	cmd.Flags().StringVar(&connectionID, "connection", "", "Outlook connection ID or platform name, e.g. \"outlook\" (required)")
	cmd.Flags().StringVar(&subject, "subject", "", "Subject")
	cmd.Flags().StringVar(&body, "body", "", "Body (required)")
	cmd.Flags().StringVar(&bodyType, "body-type", "html", "Body type: text or html")
	cmd.Flags().BoolVar(&asDraft, "draft", false, "Save as a draft instead of sending immediately")
	cmd.Flags().StringVar(&toOverride, "to", "", "Recipient address(es), comma-separated for multiple (default: the person's stored address; with --reply-to, added to the reply)")
	cmd.Flags().StringVar(&cc, "cc", "", "CC address(es), comma-separated for multiple")
	cmd.Flags().StringVar(&bcc, "bcc", "", "BCC address(es), comma-separated for multiple")
	cmd.Flags().StringVar(&replyTo, "reply-to", "", "Message id (from message history) to reply to, threading onto its existing conversation instead of starting a new one")
	cmd.Flags().BoolVar(&replyAll, "reply-all", false, "With --reply-to, reply to everyone on the original message instead of just the sender")
	_ = cmd.MarkFlagRequired("connection")
	_ = cmd.MarkFlagRequired("body")

	return cmd
}

// newPeopleMessagesReplyCmd replies to a previously recorded message
// (inbound or outbound) on the same Outlook thread, via service.outlook_mail's
// createReply/createReplyAll/reply/replyAll operations — the CLI equivalent
// of hitting Reply/Reply All on an email instead of composing a new one.
func (d Deps) newPeopleMessagesReplyCmd() *cobra.Command {
	var (
		connectionID string
		body         string
		asDraft      bool
		replyAll     bool
	)

	cmd := &cobra.Command{
		Use:   "reply <message-id>",
		Short: "Reply (or reply-all) on the same thread as a recorded message, recorded on message history",
		Args:  cobra.ExactArgs(1),
		Example: `  monoagentcli people messages reply abc123 --connection outlook --body "Sounds good, thanks!"
  monoagentcli people messages reply abc123 --connection outlook --body "Looping everyone in" --reply-all
  monoagentcli people messages reply abc123 --connection outlook --body "..." --draft`,
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
			if msg.ExternalID == "" {
				return fmt.Errorf("message %s has no associated Outlook message id, cannot reply", args[0])
			}

			resolvedConnectionID := connectionID
			if resolvedConnectionID == "" {
				resolvedConnectionID, err = draftMessageConnectionID(msg)
				if err != nil {
					return fmt.Errorf("--connection is required (could not recover it from message history): %w", err)
				}
			}

			operation, status := "reply", "sent"
			if asDraft {
				operation, status = "create_reply", "draft"
			}

			config := map[string]interface{}{
				"operation":  operation,
				"message_id": msg.ExternalID,
				"reply_all":  replyAll,
			}
			if body != "" {
				config["body"] = body
			}
			outputs, err := d.runOutlookNode(cmd, db.DB, resolvedConnectionID, config)
			if err != nil {
				return err
			}

			externalID := outlookResultMessageID(outputs)
			metaBytes, _ := json.Marshal(map[string]string{"connection_id": resolvedConnectionID})

			subject := msg.Subject
			if subject != "" && subject[:min(4, len(subject))] != "Re: " {
				subject = "Re: " + subject
			}

			reply := &storage.PersonMessage{
				PersonID:   msg.PersonID,
				Source:     "outlook",
				ExternalID: externalID,
				Direction:  "outbound",
				// Outbound sender is the connected account, never the
				// counterparty (msg.Sender) — left empty here; the true
				// from-address is recorded when the sent mail syncs back
				// via people.sync_outlook_message.
				Sender:   "",
				Subject:  subject,
				Body:     body,
				Metadata: string(metaBytes),
				Status:   status,
				SentAt:   time.Now().UTC(),
			}
			if err := db.UpsertPersonMessage(reply, d.ProfileID()); err != nil {
				return fmt.Errorf("saving message: %w", err)
			}

			if d.JSONOutput() {
				return json.NewEncoder(os.Stdout).Encode(reply)
			}
			verb := "Sent"
			if asDraft {
				verb = "Drafted"
			}
			what := "reply"
			if replyAll {
				what = "reply-all"
			}
			fmt.Fprintf(os.Stdout, "%s %s to message %s (new message id: %s)\n", verb, what, args[0], reply.ID)
			return nil
		},
	}

	cmd.Flags().StringVar(&connectionID, "connection", "", "Outlook connection ID or platform name, e.g. \"outlook\" (defaults to the connection that synced/sent the original message, if recorded)")
	cmd.Flags().StringVar(&body, "body", "", "Reply body/comment. Left empty, Outlook sends just the quoted original with no added text")
	cmd.Flags().BoolVar(&asDraft, "draft", false, "Save as a draft instead of sending immediately")
	cmd.Flags().BoolVar(&replyAll, "reply-all", false, "Reply to everyone on the original message instead of just the sender")

	return cmd
}

// outlookResultMessageID extracts the Graph message id from a
// service.outlook_mail output item. Immediately-sending operations report
// {status, message_id, reply_all}; draft-creating operations return the full
// Graph message object, whose id lives under "id". Read both so a reply's
// history row always carries the Graph id a later reply/send-draft needs.
func outlookResultMessageID(outputs []workflow.NodeOutput) string {
	if len(outputs) == 0 || len(outputs[0].Items) == 0 {
		return ""
	}
	j := outputs[0].Items[0].JSON
	if id := getStr(j, "message_id"); id != "" {
		return id
	}
	return getStr(j, "id")
}

// runOutlookNode resolves credentials for the given connection ID/platform
// name (scoped to d.ProfileID()) and executes service.outlook_mail with the
// given config, in-process — shared by compose/send-draft/reject-draft so
// none of them duplicate credential resolution or node dispatch. The node
// comes from d.NodeRegistry, so tests can stub the Graph call without a
// real connection.
func (d Deps) runOutlookNode(cmd *cobra.Command, db *sql.DB, connectionID string, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	if connectionID == "" {
		connectionID = "outlook"
	}
	registry := d.NodeRegistry(db)
	factory, ok := registry.Get("service.outlook_mail")
	if !ok {
		return nil, fmt.Errorf("service.outlook_mail node not registered")
	}

	connStore := connections.NewStore(db)
	credData, err := d.ResolveCredential(cmd.Context(), connStore, connectionID, d.ProfileID())
	if err != nil {
		return nil, fmt.Errorf("resolving connection %q: %w", connectionID, err)
	}
	for k, v := range credData {
		config[k] = v
	}

	outputs, err := factory().Execute(cmd.Context(), workflow.NodeInput{}, config)
	if err != nil {
		op, _ := config["operation"].(string)
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	return outputs, nil
}
