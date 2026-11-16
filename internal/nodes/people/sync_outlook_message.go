package peoplenodes

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/nodes/nodeprofile"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// SyncOutlookMessageNode upserts a Person (keyed by sender email) and a
// PersonMessage for each Microsoft Graph message item produced by
// service.outlook_mail's list_messages operation. Rows that don't already
// exist in People are created; existing ones are matched by email address.
// Type: "people.sync_outlook_message"
type SyncOutlookMessageNode struct{}

func (n *SyncOutlookMessageNode) Type() string { return "people.sync_outlook_message" }

func (n *SyncOutlookMessageNode) Execute(
	ctx context.Context,
	input workflow.NodeInput,
	config map[string]interface{},
) ([]workflow.NodeOutput, error) {
	if globalPeopleDB == nil {
		return nil, fmt.Errorf("people.sync_outlook_message: database not available")
	}
	db := &storage.Database{DB: globalPeopleDB}

	profileID, err := nodeprofile.Require(ctx, "people.sync_outlook_message", config)
	if err != nil {
		return nil, err
	}
	source, _ := config["source"].(string)
	if source == "" {
		source = "outlook"
	}
	// "inbound" (default) syncs the sender of each message as the person —
	// use for an inbox/received-mail read. "outbound" syncs each recipient
	// as the person instead — use for a sentitems/sent-mail read, where the
	// sender is always the account's own address.
	direction, _ := config["direction"].(string)
	if direction == "" {
		direction = "inbound"
	}

	var savedItems, skippedItems []workflow.Item

	for _, item := range input.Items {
		data := item.JSON

		subject, _ := data["subject"].(string)
		body, _ := data["bodyPreview"].(string)
		if bodyObj, ok := data["body"].(map[string]interface{}); ok {
			if full, ok := bodyObj["content"].(string); ok && full != "" {
				body = full
			}
		}
		messageID, _ := data["id"].(string)
		var sentAt time.Time
		if receivedRaw, _ := data["receivedDateTime"].(string); receivedRaw != "" {
			t, err := time.Parse(time.RFC3339, receivedRaw)
			if err != nil {
				// Skip only this message: erroring would fail every retry of
				// the batch on the one odd message, after earlier ones were written.
				skippedItems = append(skippedItems, workflow.NewItem(map[string]interface{}{
					"message_id": messageID,
					"reason":     fmt.Sprintf("receivedDateTime %q is not an RFC 3339 time", receivedRaw),
				}))
				continue
			}
			sentAt = t
		}

		// Provenance travels with the item from the node that fetched it, so
		// the answer to "where did this come from?" is recorded rather than
		// reconstructed. Fall back to a minimal block for items produced by an
		// older node version or hand-built input.
		provenance, _ := data["_source"].(map[string]interface{})
		if provenance == nil {
			provenance = map[string]interface{}{"source": source}
		}
		if _, ok := provenance["external_id"]; !ok {
			provenance["external_id"] = messageID
		}

		var counterparts []map[string]interface{}
		var fromAddr string
		if direction == "outbound" {
			fromObj, _ := data["from"].(map[string]interface{})
			fromEmailAddr, _ := fromObj["emailAddress"].(map[string]interface{})
			fromAddr, _ = fromEmailAddr["address"].(string)
			toRecipients, _ := data["toRecipients"].([]interface{})
			for _, r := range toRecipients {
				rm, ok := r.(map[string]interface{})
				if !ok {
					continue
				}
				if ea, ok := rm["emailAddress"].(map[string]interface{}); ok {
					counterparts = append(counterparts, ea)
				}
			}
		} else {
			fromObj, _ := data["from"].(map[string]interface{})
			if ea, ok := fromObj["emailAddress"].(map[string]interface{}); ok {
				counterparts = append(counterparts, ea)
			}
		}

		// The synced mailbox — the account this message was read from. On a
		// sent-mail sync that is the sender; on an inbox sync it is whoever the
		// mail was addressed to.
		if _, ok := provenance["account"]; !ok {
			account := fromAddr
			if direction != "outbound" {
				if toRecipients, _ := data["toRecipients"].([]interface{}); len(toRecipients) > 0 {
					if rm, ok := toRecipients[0].(map[string]interface{}); ok {
						if ea, ok := rm["emailAddress"].(map[string]interface{}); ok {
							account, _ = ea["address"].(string)
						}
					}
				}
			}
			if account != "" {
				provenance["account"] = account
			}
		}

		metadata := map[string]interface{}{"_source": provenance}
		if atts, ok := data["attachments"].([]interface{}); ok && len(atts) > 0 {
			metadata["attachments"] = atts
		} else if atts, ok := data["attachments"].([]map[string]interface{}); ok && len(atts) > 0 {
			metadata["attachments"] = atts
		}
		if attErr, ok := data["attachment_error"].(string); ok && attErr != "" {
			metadata["attachment_error"] = attErr
		}
		metadataJSON, err := json.Marshal(metadata)
		if err != nil {
			return nil, fmt.Errorf("people.sync_outlook_message: encode metadata: %w", err)
		}

		for _, ea := range counterparts {
			counterpartEmail, _ := ea["address"].(string)
			// One mailbox is one person whatever the case the sender wrote it in.
			counterpartEmail = strings.ToLower(strings.TrimSpace(counterpartEmail))
			counterpartName, _ := ea["name"].(string)
			if counterpartEmail == "" {
				continue // Cannot upsert a person without an identifying email.
			}

			person := &storage.Person{
				PlatformUsername: counterpartEmail,
				Platform:         "email",
				FullName:         counterpartName,
				ProfileID:        profileID,
			}
			if err := db.UpsertPerson(person); err != nil {
				return nil, fmt.Errorf("people.sync_outlook_message: upsert person %s: %w", counterpartEmail, err)
			}
			// UpsertPerson only fills in person.ID when the caller pre-set it;
			// re-fetch to get the resolved ID for the (possibly pre-existing) row.
			resolved, err := db.GetPersonByUsername(counterpartEmail, "email", profileID)
			if err != nil {
				return nil, fmt.Errorf("people.sync_outlook_message: resolve person %s: %w", counterpartEmail, err)
			}
			if resolved == nil {
				return nil, fmt.Errorf("people.sync_outlook_message: person %s not found after upsert", counterpartEmail)
			}

			sender := counterpartEmail
			if direction == "outbound" {
				sender = fromAddr
			}
			msg := &storage.PersonMessage{
				PersonID:   resolved.ID,
				Source:     source,
				ExternalID: messageID,
				Direction:  direction,
				Sender:     sender,
				Subject:    subject,
				Body:       body,
				Metadata:   string(metadataJSON),
				SentAt:     sentAt,
			}
			if err := db.UpsertPersonMessage(msg, profileID); err != nil {
				return nil, fmt.Errorf("people.sync_outlook_message: upsert message for %s: %w", counterpartEmail, err)
			}

			out := map[string]interface{}{
				"person_id":  resolved.ID,
				"email":      counterpartEmail,
				"message_id": messageID,
			}
			savedItems = append(savedItems, workflow.NewItem(out))
		}
	}

	out := []workflow.NodeOutput{{Handle: "main", Items: savedItems}}
	if len(skippedItems) > 0 {
		out = append(out, workflow.NodeOutput{Handle: "skipped", Items: skippedItems})
	}
	return out, nil
}
