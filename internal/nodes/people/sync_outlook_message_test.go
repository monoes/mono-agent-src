package peoplenodes

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// setupMessagesDB extends the people test schema with person_messages, which
// the sync node writes to.
func setupMessagesDB(t *testing.T) *sql.DB {
	t.Helper()
	db := setupTestDB(t)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS person_messages (
		id TEXT PRIMARY KEY,
		person_id TEXT NOT NULL,
		source TEXT NOT NULL,
		external_id TEXT NOT NULL DEFAULT '',
		direction TEXT NOT NULL DEFAULT 'inbound',
		sender TEXT,
		subject TEXT,
		body TEXT,
		metadata TEXT,
		status TEXT,
		sent_at TIMESTAMP,
		profile_id TEXT NOT NULL DEFAULT 'default',
		created_at TIMESTAMP,
		UNIQUE(person_id, source, external_id)
	)`); err != nil {
		t.Fatalf("create person_messages: %v", err)
	}
	return db
}

// graphMessage is a trimmed Microsoft Graph message as service.outlook_mail
// hands it to the sync node, including the provenance and attachment fields
// that node adds.
func graphMessage() map[string]interface{} {
	return map[string]interface{}{
		"id":               "AAMkAGI2=",
		"subject":          "Q3 invoice",
		"bodyPreview":      "Invoice attached.",
		"receivedDateTime": "2026-07-20T10:00:00Z",
		"from": map[string]interface{}{
			"emailAddress": map[string]interface{}{"address": "billing@vendor.com", "name": "Vendor Billing"},
		},
		"toRecipients": []interface{}{
			map[string]interface{}{"emailAddress": map[string]interface{}{"address": "me@company.com"}},
		},
		"_source": map[string]interface{}{
			"source":      "outlook",
			"via":         "service.outlook_mail (Microsoft Graph)",
			"folder":      "inbox",
			"external_id": "AAMkAGI2=",
			"web_link":    "https://outlook.office365.com/mail/id/AAMkAGI2",
			"fetched_at":  "2026-07-20T10:05:00Z",
		},
		"attachments": []interface{}{
			map[string]interface{}{
				"filename":     "invoice.pdf",
				"path":         "/home/u/.monoagent/attachments/AAMkAGI2_/invoice.pdf",
				"content_type": "application/pdf",
				"size_bytes":   1234,
			},
		},
		"attachment_count": 1,
	}
}

// storedMetadata returns the decoded metadata blob of the single stored message.
func storedMetadata(t *testing.T, db *sql.DB) map[string]interface{} {
	t.Helper()
	var raw string
	if err := db.QueryRow(`SELECT metadata FROM person_messages`).Scan(&raw); err != nil {
		t.Fatalf("read stored metadata: %v", err)
	}
	var md map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &md); err != nil {
		t.Fatalf("stored metadata is not valid JSON (%q): %v", raw, err)
	}
	return md
}

func runSync(t *testing.T, db *sql.DB, msg map[string]interface{}, config map[string]interface{}) {
	t.Helper()
	SetGlobalPeopleDB(db)
	node := &SyncOutlookMessageNode{}
	input := workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(msg)}}
	if _, err := node.Execute(vault.ContextWithProfileID(context.Background(), "default"), input, config); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

func TestSyncOutlookMessage_RecordsProvenance(t *testing.T) {
	db := setupMessagesDB(t)
	runSync(t, db, graphMessage(), map[string]interface{}{})

	md := storedMetadata(t, db)
	src, ok := md["_source"].(map[string]interface{})
	if !ok {
		t.Fatalf("no _source block in metadata: %v", md)
	}
	for key, want := range map[string]string{
		"source":      "outlook",
		"via":         "service.outlook_mail (Microsoft Graph)",
		"folder":      "inbox",
		"external_id": "AAMkAGI2=",
		"web_link":    "https://outlook.office365.com/mail/id/AAMkAGI2",
	} {
		if got, _ := src[key].(string); got != want {
			t.Errorf("_source[%q] = %q, want %q", key, got, want)
		}
	}
	// On an inbox sync the synced account is the recipient, not the sender.
	if got, _ := src["account"].(string); got != "me@company.com" {
		t.Errorf("_source[account] = %q, want the mailbox being synced", got)
	}
}

func TestSyncOutlookMessage_RecordsAttachmentPaths(t *testing.T) {
	db := setupMessagesDB(t)
	runSync(t, db, graphMessage(), map[string]interface{}{})

	md := storedMetadata(t, db)
	atts, ok := md["attachments"].([]interface{})
	if !ok || len(atts) != 1 {
		t.Fatalf("attachments not stored: %v", md["attachments"])
	}
	att, _ := atts[0].(map[string]interface{})
	if got, _ := att["path"].(string); got == "" {
		t.Error("attachment stored without a readable path")
	}
	if got, _ := att["filename"].(string); got != "invoice.pdf" {
		t.Errorf("filename = %q, want invoice.pdf", got)
	}
}

// A message from an older node version carries no _source block; the sync must
// still record which system it came from rather than storing nothing.
func TestSyncOutlookMessage_ProvenanceFallback(t *testing.T) {
	db := setupMessagesDB(t)
	msg := graphMessage()
	delete(msg, "_source")
	delete(msg, "attachments")

	runSync(t, db, msg, map[string]interface{}{})

	md := storedMetadata(t, db)
	src, ok := md["_source"].(map[string]interface{})
	if !ok {
		t.Fatalf("no _source block in metadata: %v", md)
	}
	if got, _ := src["source"].(string); got != "outlook" {
		t.Errorf("_source[source] = %q, want outlook", got)
	}
	if got, _ := src["external_id"].(string); got != "AAMkAGI2=" {
		t.Errorf("_source[external_id] = %q, want the message id", got)
	}
}

func TestSyncOutlookMessage_EmailCaseIsOnePerson(t *testing.T) {
	db := setupMessagesDB(t)
	a := graphMessage()
	runSync(t, db, a, map[string]interface{}{})
	b := graphMessage()
	b["id"] = "second"
	b["from"] = map[string]interface{}{
		"emailAddress": map[string]interface{}{"address": " Billing@Vendor.com ", "name": "Vendor Billing"},
	}
	runSync(t, db, b, map[string]interface{}{})

	var n int
	var user string
	if err := db.QueryRow(`SELECT COUNT(*), MIN(platform_username) FROM people WHERE platform = 'email'`).Scan(&n, &user); err != nil {
		t.Fatal(err)
	}
	if n != 1 || user != "billing@vendor.com" {
		t.Errorf("people = %d (%q), want one person billing@vendor.com", n, user)
	}
}

// One message with an unreadable date is skipped and reported; the rest of
// the batch is stored, and a retry of the same batch succeeds.
func TestSyncOutlookMessage_BadReceivedDateSkipsThatMessage(t *testing.T) {
	db := setupMessagesDB(t)
	SetGlobalPeopleDB(db)
	good1 := graphMessage()
	bad := graphMessage()
	bad["id"] = "bad-id"
	bad["receivedDateTime"] = "yesterday"
	good2 := graphMessage()
	good2["id"] = "good2"
	run := func() []workflow.NodeOutput {
		node := &SyncOutlookMessageNode{}
		input := workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(good1), workflow.NewItem(bad), workflow.NewItem(good2)}}
		out, err := node.Execute(vault.ContextWithProfileID(context.Background(), "default"), input, map[string]interface{}{})
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		return out
	}
	for attempt := 1; attempt <= 2; attempt++ {
		out := run()
		var main, skipped []workflow.Item
		for _, o := range out {
			switch o.Handle {
			case "main":
				main = o.Items
			case "skipped":
				skipped = o.Items
			}
		}
		if len(main) != 2 {
			t.Errorf("attempt %d: %d saved items, want 2", attempt, len(main))
		}
		if len(skipped) != 1 || skipped[0].JSON["message_id"] != "bad-id" || !strings.Contains(skipped[0].JSON["reason"].(string), "receivedDateTime") {
			t.Fatalf("attempt %d: skipped = %v, want the bad message id with a reason", attempt, skipped)
		}
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM person_messages`).Scan(&n)
	if n != 2 {
		t.Errorf("%d messages stored, want the 2 good ones", n)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM person_messages WHERE external_id = 'bad-id'`).Scan(&n)
	if n != 0 {
		t.Errorf("the bad-date message was stored")
	}
}
