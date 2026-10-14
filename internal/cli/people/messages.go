package people

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/spf13/cobra"
)

func (d Deps) newPeopleMessagesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "messages",
		Aliases: []string{"history"},
		Short:   "Manage a person's message/interaction history",
		Long:    "Store and list messages or interactions for a person, ingested from any source (Outlook, social platforms, manual notes, ...).",
	}

	cmd.AddCommand(
		d.newPeopleMessagesAddCmd(),
		d.newPeopleMessagesListCmd(),
		d.newPeopleMessagesShowCmd(),
		d.newPeopleMessagesImportCmd(),
		d.newPeopleMessagesAllCmd(),
		d.newPeopleMessagesComposeCmd(),
		d.newPeopleMessagesReplyCmd(),
		d.newPeopleMessagesDraftsCmd(),
		d.newPeopleMessagesSendDraftCmd(),
		d.newPeopleMessagesRejectDraftCmd(),
		d.newPeopleMessagesClassifyCmd(),
		d.newPeopleMessagesReadCmd(),
		d.newPeopleMessagesUnreadCmd(),
	)

	return cmd
}

func (d Deps) newPeopleMessagesAllCmd() *cobra.Command {
	var (
		source string
		limit  int
		unread bool
	)

	cmd := &cobra.Command{
		Use:   "all",
		Short: "List synced messages/interactions across every person (a unified communications feed)",
		Example: `  monoagentcli people messages all
  monoagentcli people messages all --source outlook --limit 50 --json
  monoagentcli people messages all --unread`,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			messages, err := db.ListAllPersonMessagesFiltered(d.ProfileID(), source, unread, limit, 0)
			if err != nil {
				return fmt.Errorf("listing messages: %w", err)
			}
			if messages == nil {
				messages = []*storage.PersonMessageWithPerson{} // [] not null in --json
			}

			if d.JSONOutput() {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(messages)
			}

			if len(messages) == 0 {
				fmt.Println("No messages found.")
				return nil
			}

			table := d.NewTable(os.Stdout, []string{"ID", "From", "Source", "Direction", "Subject", "Files", "Sent At"}, nil)

			for _, m := range messages {
				shortID := m.ID
				if len(shortID) > 8 {
					shortID = shortID[:8]
				}
				from := m.PersonFullName
				if from == "" {
					from = m.PersonPlatformUsername
				}
				sentAt := ""
				if !m.SentAt.IsZero() {
					sentAt = m.SentAt.Format("2006-01-02 15:04:05")
				}
				files := ""
				if n := len(parseMessageMetadata(m.Metadata).Attachments); n > 0 {
					files = fmt.Sprintf("%d", n)
				}
				table.Append([]string{
					shortID, truncateStr(from, 24), m.Source, m.Direction,
					truncateStr(m.Subject, 40), files, sentAt,
				})
			}
			table.Render()
			fmt.Fprintf(os.Stderr, "\nTotal: %d message(s)\n", len(messages))
			fmt.Fprintf(os.Stderr, "Full text, source details, and attachment paths: monoagentcli people messages show <id>\n")
			return nil
		},
	}

	cmd.Flags().StringVar(&source, "source", "", "Filter by source")
	cmd.Flags().IntVarP(&limit, "limit", "n", 100, "Maximum number of results")
	cmd.Flags().BoolVar(&unread, "unread", false, "Only inbound messages not marked read yet")

	return cmd
}

func (d Deps) newPeopleMessagesAddCmd() *cobra.Command {
	var (
		source     string
		externalID string
		direction  string
		sender     string
		subject    string
		body       string
		sentAt     string
	)

	cmd := &cobra.Command{
		Use:   "add <person-id>",
		Short: "Record a single message/interaction for a person",
		Args:  cobra.ExactArgs(1),
		Example: `  monoagentcli people messages add abc123 --source outlook --subject "Re: intro" --body "Thanks for reaching out"
  monoagentcli people messages add abc123 --source instagram --direction outbound --body "hey!"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if source == "" {
				return fmt.Errorf("--source is required")
			}
			if direction == "" {
				direction = "inbound"
			}

			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			msg := &storage.PersonMessage{
				PersonID:   args[0],
				Source:     source,
				ExternalID: externalID,
				Direction:  direction,
				Sender:     sender,
				Subject:    subject,
				Body:       body,
			}
			if sentAt != "" {
				t, err := time.Parse(time.RFC3339, sentAt)
				if err != nil {
					return fmt.Errorf("parsing --sent-at (expected RFC3339): %w", err)
				}
				msg.SentAt = t
			}

			if err := db.UpsertPersonMessage(msg, d.ProfileID()); err != nil {
				return fmt.Errorf("saving message: %w", err)
			}

			if d.JSONOutput() {
				return printReviewJSON(msg)
			}
			fmt.Fprintf(os.Stdout, "Saved message %s for person %s.\n", msg.ID, msg.PersonID)
			return nil
		},
	}

	cmd.Flags().StringVar(&source, "source", "", "Source of the message, e.g. outlook, gmail, instagram, linkedin, x, telegram, manual (required)")
	cmd.Flags().StringVar(&externalID, "external-id", "", "Source-native message/thread id, for idempotent re-import")
	cmd.Flags().StringVar(&direction, "direction", "inbound", "Message direction: inbound or outbound")
	cmd.Flags().StringVar(&sender, "sender", "", "Sender name or address")
	cmd.Flags().StringVar(&subject, "subject", "", "Message subject")
	cmd.Flags().StringVar(&body, "body", "", "Message body")
	cmd.Flags().StringVar(&sentAt, "sent-at", "", "When the message was sent, RFC3339 (defaults to now)")
	_ = cmd.MarkFlagRequired("source")

	return cmd
}

// messageMetadata is the decoded person_messages.metadata blob: where the
// message came from and which files arrived with it.
type messageMetadata struct {
	Source struct {
		Source     string `json:"source"`
		Via        string `json:"via"`
		Account    string `json:"account"`
		Folder     string `json:"folder"`
		ExternalID string `json:"external_id"`
		WebLink    string `json:"web_link"`
		FetchedAt  string `json:"fetched_at"`
	} `json:"_source"`
	Attachments []struct {
		Filename    string `json:"filename"`
		Path        string `json:"path"`
		ContentType string `json:"content_type"`
		SizeBytes   int64  `json:"size_bytes"`
		Note        string `json:"note"`
		Error       string `json:"error"`
	} `json:"attachments"`
	AttachmentError string `json:"attachment_error"`
	// Classification is the Jev intent label (`people messages classify`);
	// nil until a message was classified at or above the inbox threshold.
	Classification *messageClassification `json:"_classification,omitempty"`
}

// parseMessageMetadata decodes a message's metadata blob. Messages stored
// before provenance was recorded (or by a source that writes its own shape)
// simply yield empty fields rather than an error.
func parseMessageMetadata(raw string) messageMetadata {
	var md messageMetadata
	if raw == "" {
		return md
	}
	_ = json.Unmarshal([]byte(raw), &md)
	return md
}

// newPeopleMessagesShowCmd prints one message in full, together with its
// provenance and the on-disk path of every attachment — everything a reader
// (human or agent) needs to judge the message and open what came with it.
func (d Deps) newPeopleMessagesShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <message-id>",
		Short: "Show one message in full, with its source and attachment file paths",
		Long: "Print a single message: subject, sender, body, where it came from (system, account, " +
			"folder, link to the original) and the local path of every attachment.\n\n" +
			"Attachment paths are real files — read them directly to see what was attached.",
		Args: cobra.ExactArgs(1),
		Example: `  monoagentcli people messages show 3f2a91c0
  monoagentcli --json people messages show 3f2a91c0`,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			if err := assertMessageInProfile(db.DB, args[0], d.ProfileID()); err != nil {
				return err
			}
			msg, err := db.GetPersonMessage(args[0])
			if err != nil {
				return fmt.Errorf("loading message: %w", err)
			}
			if msg == nil {
				return fmt.Errorf("message %s not found", args[0])
			}

			if d.JSONOutput() {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(msg)
			}

			md := parseMessageMetadata(msg.Metadata)
			sentAt := ""
			if !msg.SentAt.IsZero() {
				sentAt = msg.SentAt.Format("2006-01-02 15:04:05")
			}

			fmt.Printf("Subject:   %s\n", msg.Subject)
			fmt.Printf("From:      %s\n", msg.Sender)
			fmt.Printf("Direction: %s\n", msg.Direction)
			if sentAt != "" {
				fmt.Printf("Sent:      %s\n", sentAt)
			}

			// Provenance — always shown, so a reader is never left guessing
			// which system a message came from or how it got here.
			fmt.Printf("\nSOURCE\n")
			src := md.Source.Source
			if src == "" {
				src = msg.Source
			}
			fmt.Printf("  system:      %s\n", src)
			if md.Source.Via != "" {
				fmt.Printf("  synced via:  %s\n", md.Source.Via)
			}
			if md.Source.Account != "" {
				fmt.Printf("  account:     %s\n", md.Source.Account)
			}
			if md.Source.Folder != "" {
				fmt.Printf("  folder:      %s\n", md.Source.Folder)
			}
			externalID := md.Source.ExternalID
			if externalID == "" {
				externalID = msg.ExternalID
			}
			if externalID != "" {
				fmt.Printf("  id there:    %s\n", externalID)
			}
			if md.Source.WebLink != "" {
				fmt.Printf("  open it:     %s\n", md.Source.WebLink)
			}
			if md.Source.FetchedAt != "" {
				fmt.Printf("  synced at:   %s\n", md.Source.FetchedAt)
			}

			if len(md.Attachments) > 0 {
				fmt.Printf("\nATTACHMENTS (%d) — read these paths directly\n", len(md.Attachments))
				for _, a := range md.Attachments {
					switch {
					case a.Path != "":
						fmt.Printf("  %s  (%s, %d bytes)\n      %s\n", a.Filename, a.ContentType, a.SizeBytes, a.Path)
					case a.Error != "":
						fmt.Printf("  %s  — not downloaded: %s\n", a.Filename, a.Error)
					default:
						fmt.Printf("  %s  — %s\n", a.Filename, a.Note)
					}
				}
			}
			if md.AttachmentError != "" {
				fmt.Printf("\nAttachments could not be fetched: %s\n", md.AttachmentError)
			}
			if c := md.Classification; c != nil {
				fmt.Printf("\nCLASSIFICATION (Jev %s, %s)\n", c.Model, c.At)
				fmt.Printf("  intent:        %s (p=%.2f)\n", c.Intent, c.IntentP)
				fmt.Printf("  should reply:  p=%.2f\n", c.ShouldReplyP)
			}

			fmt.Printf("\nBODY\n%s\n", msg.Body)
			return nil
		},
	}
}

func (d Deps) newPeopleMessagesListCmd() *cobra.Command {
	var (
		source string
		limit  int
		intent string
	)

	cmd := &cobra.Command{
		Use:   "list <person-id>",
		Short: "List a person's message/interaction history",
		Args:  cobra.ExactArgs(1),
		Example: `  monoagentcli people messages list abc123
  monoagentcli people messages list abc123 --source outlook --json
  monoagentcli people messages list abc123 --intent lead`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if intent != "" && !slices.Contains(inboxIntents, intent) {
				return d.InvalidInput("--intent must be one of %s, got %q", strings.Join(inboxIntents, ", "), intent)
			}
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			fetch := limit
			if intent != "" {
				fetch = maxMessagesScan // filter first, then apply --limit
			}
			messages, err := db.ListPersonMessages(args[0], source, d.ProfileID(), fetch, 0)
			if err != nil {
				return fmt.Errorf("listing messages: %w", err)
			}
			if intent != "" {
				messages = filterMessagesByIntent(messages, intent, limit)
			}
			if messages == nil {
				messages = []*storage.PersonMessage{} // [] not null in --json
			}

			if d.JSONOutput() {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(messages)
			}

			if len(messages) == 0 {
				fmt.Println("No messages found.")
				return nil
			}

			table := d.NewTable(os.Stdout, []string{"ID", "Source", "Direction", "Sender", "Subject", "Files", "Sent At"}, nil)

			for _, m := range messages {
				shortID := m.ID
				if len(shortID) > 8 {
					shortID = shortID[:8]
				}
				sentAt := ""
				if !m.SentAt.IsZero() {
					sentAt = m.SentAt.Format("2006-01-02 15:04:05")
				}
				files := ""
				if n := len(parseMessageMetadata(m.Metadata).Attachments); n > 0 {
					files = fmt.Sprintf("%d", n)
				}
				table.Append([]string{
					shortID, m.Source, m.Direction,
					truncateStr(m.Sender, 20), truncateStr(m.Subject, 30), files, sentAt,
				})
			}
			table.Render()
			fmt.Fprintf(os.Stderr, "\nTotal: %d message(s)\n", len(messages))
			fmt.Fprintf(os.Stderr, "Full text, source details, and attachment paths: monoagentcli people messages show <id>\n")
			return nil
		},
	}

	cmd.Flags().StringVar(&source, "source", "", "Filter by source")
	cmd.Flags().IntVarP(&limit, "limit", "n", 100, "Maximum number of results")
	cmd.Flags().StringVar(&intent, "intent", "", "Only messages classified with this intent ("+strings.Join(inboxIntents, ", ")+"; see people messages classify)")

	return cmd
}

func (d Deps) newPeopleMessagesImportCmd() *cobra.Command {
	var (
		filePath string
		source   string
	)

	cmd := &cobra.Command{
		Use:     "import <person-id>",
		Short:   "Bulk-import a person's message history from a JSON array file",
		Args:    cobra.ExactArgs(1),
		Example: `  monoagentcli people messages import abc123 --file outlook_thread.json --source outlook`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if filePath == "" {
				return fmt.Errorf("--file is required")
			}
			if source == "" {
				return fmt.Errorf("--source is required")
			}

			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			data, err := os.ReadFile(filePath)
			if err != nil {
				return fmt.Errorf("reading file %s: %w", filePath, err)
			}

			var raw []map[string]interface{}
			if err := json.Unmarshal(data, &raw); err != nil {
				return fmt.Errorf("parsing JSON array: %w", err)
			}

			var imported int
			for _, r := range raw {
				msg := &storage.PersonMessage{
					PersonID:   args[0],
					Source:     source,
					ExternalID: getStr(r, "external_id"),
					Direction:  getStr(r, "direction"),
					Sender:     getStr(r, "sender"),
					Subject:    getStr(r, "subject"),
					Body:       getStr(r, "body"),
				}
				if v := getStr(r, "sent_at"); v != "" {
					if t, err := time.Parse(time.RFC3339, v); err == nil {
						msg.SentAt = t
					}
				}
				if err := db.UpsertPersonMessage(msg, d.ProfileID()); err != nil {
					return fmt.Errorf("importing message: %w", err)
				}
				imported++
			}

			fmt.Fprintf(os.Stdout, "Imported %d message(s) for person %s from %s.\n", imported, args[0], source)
			return nil
		},
	}

	cmd.Flags().StringVar(&filePath, "file", "", "Path to JSON file containing a message array (required)")
	cmd.Flags().StringVar(&source, "source", "", "Source of the messages, e.g. outlook, gmail, instagram (required)")
	_ = cmd.MarkFlagRequired("file")
	_ = cmd.MarkFlagRequired("source")

	return cmd
}

func getStr(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}
