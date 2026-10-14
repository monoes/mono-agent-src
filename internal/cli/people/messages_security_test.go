package people

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// newMessagesCLITestDB applies every real migration to a fresh SQLite file,
// then seeds person "p1" (platform_username alice@x.com) under profile
// "default". Commands under test open their own connection via initDB.
func newMessagesCLITestDB(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "cli-messages-test.db")

	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	if _, err := db.DB.Exec(`INSERT INTO people (id, platform_username, platform, profile_id) VALUES ('p1','alice@x.com','email','default')`); err != nil {
		t.Fatalf("seeding p1: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("closing seed db: %v", err)
	}
	return dbPath
}

// seedPersonMessage inserts a person_messages row with explicit control over
// every column the commands under test branch on.
func seedPersonMessage(t *testing.T, dbPath, id, externalID, direction, sender, status, metadata string) {
	t.Helper()
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	defer db.Close()
	if _, err := db.DB.Exec(
		`INSERT INTO person_messages (id, person_id, source, external_id, direction, sender, subject, body, metadata, status, profile_id)
		 VALUES (?, 'p1', 'outlook', ?, ?, ?, 'Hello', 'orig body', ?, ?, 'default')`,
		id, externalID, direction, sender, metadata, status,
	); err != nil {
		t.Fatalf("seeding message %s: %v", id, err)
	}
}

// fetchPersonMessage reads one row back through the real repository.
func fetchPersonMessage(t *testing.T, dbPath, id string) *storage.PersonMessage {
	t.Helper()
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	defer db.Close()
	msg, err := db.GetPersonMessage(id)
	if err != nil {
		t.Fatalf("GetPersonMessage %s: %v", id, err)
	}
	return msg
}

// stubOutlookNode makes the test Deps' service.outlook_mail node a fake,
// restored automatically when the test ends. The fake receives the
// live *sql.DB so it can observe database state mid-call.
func stubOutlookNode(t *testing.T, fn func(db *sql.DB, config map[string]interface{}) ([]workflow.NodeOutput, error)) {
	t.Helper()
	orig := outlookStub
	outlookStub = fn
	t.Cleanup(func() { outlookStub = orig })
}

// nodeOutput wraps a single item the way service.outlook_mail does.
func nodeOutput(item map[string]interface{}) []workflow.NodeOutput {
	return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{workflow.NewItem(item)}}}
}

// --- RB4 fix 1: reject-draft may only discard drafts ----------------------

// TestRejectDraftRefusesNonDraft is the regression test for the reject-draft
// guard: before it, reject-draft happily deleted ANY message row (and
// best-effort deleted the mail itself) — including synced inbox history.
func TestRejectDraftRefusesNonDraft(t *testing.T) {
	dbPath := newMessagesCLITestDB(t)
	seedPersonMessage(t, dbPath, "m-sent", "EXT-1", "inbound", "alice@x.com", "sent", `{"connection_id":"outlook"}`)

	cmd := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default"}).newPeopleMessagesRejectDraftCmd()
	cmd.SetArgs([]string{"m-sent"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("reject-draft on a sent message must fail")
	}
	if !strings.Contains(err.Error(), "not a draft") {
		t.Errorf("error = %v, want it to say the message is not a draft", err)
	}
	if msg := fetchPersonMessage(t, dbPath, "m-sent"); msg == nil {
		t.Error("reject-draft must not delete a non-draft message")
	}
}

func TestRejectDraftDeletesDraft(t *testing.T) {
	dbPath := newMessagesCLITestDB(t)
	seedPersonMessage(t, dbPath, "m-draft", "EXT-2", "outbound", "", "draft", `{"connection_id":"outlook"}`)

	// The remote delete is best-effort; stub it as succeeding.
	stubOutlookNode(t, func(_ *sql.DB, _ map[string]interface{}) ([]workflow.NodeOutput, error) {
		return nodeOutput(map[string]interface{}{"status": "deleted", "message_id": "EXT-2"}), nil
	})

	cmd := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default"}).newPeopleMessagesRejectDraftCmd()
	cmd.SetArgs([]string{"m-draft"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("reject-draft on a real draft: %v", err)
	}
	if msg := fetchPersonMessage(t, dbPath, "m-draft"); msg != nil {
		t.Errorf("draft row still present after reject-draft: %+v", msg)
	}
}

// --- RB4 fix 2: replies capture the Graph id from node output -------------

// TestOutlookResultMessageIDFixtureShapes pins the helper against both node
// output shapes: immediate sends report {status,message_id,reply_all} while
// draft-creating ops return the full Graph message object with "id".
func TestOutlookResultMessageIDFixtureShapes(t *testing.T) {
	t.Parallel()
	fixtures := []struct {
		name string
		json string
		want string
	}{
		{"send shape prefers message_id", `{"status":"sent","message_id":"M1","reply_all":false}`, "M1"},
		{"draft object falls back to id", `{"id":"D2","subject":"Re: Hello"}`, "D2"},
		{"both present, message_id wins", `{"id":"D3","message_id":"M3"}`, "M3"},
		{"neither present", `{"status":"sent"}`, ""},
	}
	for _, fx := range fixtures {
		var item map[string]interface{}
		if err := json.Unmarshal([]byte(fx.json), &item); err != nil {
			t.Fatalf("%s: bad fixture: %v", fx.name, err)
		}
		if got := outlookResultMessageID(nodeOutput(item)); got != fx.want {
			t.Errorf("%s: outlookResultMessageID(%s) = %q, want %q", fx.name, fx.json, got, fx.want)
		}
	}
	if got := outlookResultMessageID(nil); got != "" {
		t.Errorf("no outputs: got %q, want empty", got)
	}
	if got := outlookResultMessageID([]workflow.NodeOutput{{Handle: "main"}}); got != "" {
		t.Errorf("no items: got %q, want empty", got)
	}
}

// TestReplyCapturesGraphMessageIDAndOwnSender exercises `people messages
// reply`: the recorded reply must carry the Graph id from the node's
// message_id field (previously always empty — a later reply/send-draft on
// that row could not work), and must not stamp the counterparty as Sender.
func TestReplyCapturesGraphMessageIDAndOwnSender(t *testing.T) {
	dbPath := newMessagesCLITestDB(t)
	seedPersonMessage(t, dbPath, "m-orig", "AAA-111", "inbound", "alice@x.com", "sent", `{"connection_id":"outlook"}`)

	stubOutlookNode(t, func(_ *sql.DB, _ map[string]interface{}) ([]workflow.NodeOutput, error) {
		return nodeOutput(map[string]interface{}{"status": "sent", "message_id": "REP-2", "reply_all": true}), nil
	})

	cmd := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default"}).newPeopleMessagesReplyCmd()
	cmd.SetArgs([]string{"m-orig", "--connection", "outlook", "--body", "hey", "--reply-all"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("reply: %v", err)
	}

	msg := fetchPersonMessage(t, dbPath, "m-orig")
	_ = msg
	// The reply is a new row, keyed by the Graph id the node reported.
	reply := fetchPersonMessageByExternalID(t, dbPath, "REP-2")
	if reply == nil {
		t.Fatal("reply row with Graph external id REP-2 not recorded")
	}
	if reply.Direction != "outbound" {
		t.Errorf("reply direction = %q, want outbound", reply.Direction)
	}
	if reply.Sender == "alice@x.com" {
		t.Errorf("outbound reply Sender = counterparty %q — sender is the connected account, never the counterparty", reply.Sender)
	}
}

// TestComposeReplyCapturesGraphMessageID exercises `compose --reply-to` with
// the draft-creating shape (Graph message object under "id") so the recorded
// draft is later sendable/rejectable by its Graph id.
func TestComposeReplyCapturesGraphMessageID(t *testing.T) {
	dbPath := newMessagesCLITestDB(t)
	seedPersonMessage(t, dbPath, "m-orig2", "AAA-222", "inbound", "alice@x.com", "sent", `{"connection_id":"outlook"}`)

	stubOutlookNode(t, func(_ *sql.DB, _ map[string]interface{}) ([]workflow.NodeOutput, error) {
		return nodeOutput(map[string]interface{}{"id": "DR-3", "subject": "Re: Hello"}), nil
	})

	cmd := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default"}).newPeopleMessagesComposeCmd()
	cmd.SetArgs([]string{"p1", "--connection", "outlook", "--body", "re", "--reply-to", "m-orig2", "--draft"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("compose --reply-to --draft: %v", err)
	}

	draft := fetchPersonMessageByExternalID(t, dbPath, "DR-3")
	if draft == nil {
		t.Fatal("draft reply row with Graph external id DR-3 not recorded")
	}
	if draft.Status != "draft" {
		t.Errorf("draft status = %q, want draft", draft.Status)
	}
	if draft.Sender == "alice@x.com" {
		t.Errorf("outbound draft Sender = counterparty %q", draft.Sender)
	}
}

// --- RB4 fix 3: plain compose must not record the recipient as Sender -----

func TestComposeRecordsOwnSenderNotRecipient(t *testing.T) {
	dbPath := newMessagesCLITestDB(t)

	var gotTo string
	stubOutlookNode(t, func(_ *sql.DB, config map[string]interface{}) ([]workflow.NodeOutput, error) {
		gotTo, _ = config["to"].(string)
		return nodeOutput(map[string]interface{}{"status": "sent", "to": "alice@x.com", "subject": "Hi"}), nil
	})

	cmd := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default"}).newPeopleMessagesComposeCmd()
	cmd.SetArgs([]string{"p1", "--connection", "outlook", "--subject", "Hi", "--body", "hello"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("compose: %v", err)
	}
	if gotTo != "alice@x.com" {
		t.Errorf("node received to = %q, want the person's stored address", gotTo)
	}

	// The only outbound row for p1 must not carry the counterparty as Sender.
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	defer db.Close()
	rows, err := db.ListPersonMessages("p1", "outlook", "default", 10, 0)
	if err != nil {
		t.Fatalf("ListPersonMessages: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d outbound rows, want 1", len(rows))
	}
	if rows[0].Sender == "alice@x.com" {
		t.Errorf("outbound Sender = recipient %q — sender is the connected account", rows[0].Sender)
	}
}

// --- RB4 fix 8: send-draft flips to "sending" before the Graph call -------

// TestSendDraftMarksSendingBeforeGraphCall observes the row status from
// inside the stubbed Graph call: it must already be "sending" there, closing
// the check-then-act window where a concurrent send/reject double-fires.
func TestSendDraftMarksSendingBeforeGraphCall(t *testing.T) {
	dbPath := newMessagesCLITestDB(t)
	seedPersonMessage(t, dbPath, "m-send", "EXT-9", "outbound", "", "draft", `{"connection_id":"outlook"}`)

	var statusDuringCall string
	stubOutlookNode(t, func(db *sql.DB, _ map[string]interface{}) ([]workflow.NodeOutput, error) {
		_ = db.QueryRow(`SELECT status FROM person_messages WHERE id = 'm-send'`).Scan(&statusDuringCall)
		return nodeOutput(map[string]interface{}{"status": "sent", "message_id": "SENT-NEW"}), nil
	})

	cmd := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default"}).newPeopleMessagesSendDraftCmd()
	cmd.SetArgs([]string{"m-send"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("send-draft: %v", err)
	}

	if statusDuringCall != "sending" {
		t.Errorf("status during Graph call = %q, want \"sending\" (flip must happen before the call)", statusDuringCall)
	}
	msg := fetchPersonMessage(t, dbPath, "m-send")
	if msg.Status != "sent" {
		t.Errorf("final status = %q, want sent", msg.Status)
	}
	if msg.ExternalID != "SENT-NEW" {
		t.Errorf("external id = %q, want the reassigned Graph id SENT-NEW", msg.ExternalID)
	}
}

// TestSendDraftRestoresDraftStatusOnFailure: a failed Graph send must leave
// the row as a draft so it can be retried, not stranded in "sending".
func TestSendDraftRestoresDraftStatusOnFailure(t *testing.T) {
	dbPath := newMessagesCLITestDB(t)
	seedPersonMessage(t, dbPath, "m-fail", "EXT-10", "outbound", "", "draft", `{"connection_id":"outlook"}`)

	var statusDuringCall string
	stubOutlookNode(t, func(db *sql.DB, _ map[string]interface{}) ([]workflow.NodeOutput, error) {
		_ = db.QueryRow(`SELECT status FROM person_messages WHERE id = 'm-fail'`).Scan(&statusDuringCall)
		return nil, fmt.Errorf("graph unavailable")
	})

	cmd := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default"}).newPeopleMessagesSendDraftCmd()
	cmd.SetArgs([]string{"m-fail"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("send-draft must surface the Graph failure")
	}

	if statusDuringCall != "sending" {
		t.Errorf("status during Graph call = %q, want \"sending\"", statusDuringCall)
	}
	msg := fetchPersonMessage(t, dbPath, "m-fail")
	if msg.Status != "draft" {
		t.Errorf("status after failed send = %q, want restored \"draft\"", msg.Status)
	}
}

// --- RB4 fix 9: compose refuses non-address recipients --------------------

func TestComposeRefusesContactWithoutEmail(t *testing.T) {
	dbPath := newMessagesCLITestDB(t)

	// A contact whose stored handle is not an email address.
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	if _, err := db.DB.Exec(`INSERT INTO people (id, platform_username, platform, profile_id) VALUES ('p2','bobby','instagram','default')`); err != nil {
		t.Fatalf("seeding p2: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("closing seed db: %v", err)
	}

	cfg := &testConfig{DBPath: dbPath, ProfileID: "default"}
	cmd := testDeps(cfg).newPeopleMessagesComposeCmd()
	cmd.SetArgs([]string{"p2", "--connection", "outlook", "--body", "hi"})
	err = cmd.Execute()
	if err == nil {
		t.Fatal("compose to a contact with no email address must fail")
	}
	if !strings.Contains(err.Error(), "no email address") {
		t.Errorf("error = %v, want the no-email-address message", err)
	}

	// An explicit --to override bypasses the stored handle.
	stubOutlookNode(t, func(_ *sql.DB, _ map[string]interface{}) ([]workflow.NodeOutput, error) {
		return nodeOutput(map[string]interface{}{"status": "sent"}), nil
	})
	cmd2 := testDeps(cfg).newPeopleMessagesComposeCmd()
	cmd2.SetArgs([]string{"p2", "--connection", "outlook", "--body", "hi", "--to", "bobby@example.com"})
	if err := cmd2.Execute(); err != nil {
		t.Fatalf("compose with --to override: %v", err)
	}
}

// fetchPersonMessageByExternalID finds a row by its source-native id.
func fetchPersonMessageByExternalID(t *testing.T, dbPath, externalID string) *storage.PersonMessage {
	t.Helper()
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	defer db.Close()
	rows, err := db.ListPersonMessages("p1", "outlook", "default", 50, 0)
	if err != nil {
		t.Fatalf("ListPersonMessages: %v", err)
	}
	for _, m := range rows {
		if m.ExternalID == externalID {
			return m
		}
	}
	return nil
}
