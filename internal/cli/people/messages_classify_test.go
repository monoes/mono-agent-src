package people

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/storage"
)

const richMetadata = `{"_source":{"source":"outlook","via":"service.outlook_mail","account":"me@x.com"},` +
	`"attachments":[{"filename":"cv.pdf","path":"/files/cv.pdf","size_bytes":12}],"connection_id":"outlook"}`

// withMessageClassification returns raw with _classification set to c
// (test seeding only; production writes go through storeMessageClassification).
func withMessageClassification(raw string, c messageClassification) (string, error) {
	md := map[string]json.RawMessage{}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &md); err != nil {
			return "", err
		}
	}
	blob, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	md["_classification"] = blob
	out, err := json.Marshal(md)
	return string(out), err
}

func storeClassification(t *testing.T, dbPath, id string, c messageClassification) error {
	t.Helper()
	var err error
	withJevDB(t, dbPath, func(db *storage.Database) { err = storeMessageClassification(db.DB, id, c) })
	return err
}

func TestStoreMessageClassificationKeepsMetadata(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dbPath := newMessagesCLITestDB(t)
	seedPersonMessage(t, dbPath, "m1", "E1", "inbound", "Bob", "sent", richMetadata)
	seedPersonMessage(t, dbPath, "m-empty", "E2", "inbound", "Bob", "sent", "")
	seedPersonMessage(t, dbPath, "m-null", "E3", "inbound", "Bob", "sent", "null")
	seedPersonMessage(t, dbPath, "m-array", "E4", "inbound", "Bob", "sent", `["not","an","object"]`)
	seedPersonMessage(t, dbPath, "m-bad", "E5", "inbound", "Bob", "sent", `{broken`)

	c := messageClassification{Intent: "lead", IntentP: 0.9, ShouldReplyP: 0.8, Model: "m", At: "2026-09-25T00:00:00Z"}
	if err := storeClassification(t, dbPath, "m1", c); err != nil {
		t.Fatal(err)
	}
	out := fetchPersonMessage(t, dbPath, "m1").Metadata
	md := parseMessageMetadata(out)
	if md.Source.Source != "outlook" || md.Source.Account != "me@x.com" || len(md.Attachments) != 1 || md.Attachments[0].Path != "/files/cv.pdf" {
		t.Fatalf("existing metadata lost: %s", out)
	}
	if md.Classification == nil || *md.Classification != c {
		t.Fatalf("classification = %+v", md.Classification)
	}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal([]byte(out), &raw)
	if string(raw["connection_id"]) != `"outlook"` {
		t.Fatalf("unknown key connection_id not preserved: %s", out)
	}

	// Replacing a classification keeps everything else too.
	c2 := c
	c2.Intent = "spam"
	if err := storeClassification(t, dbPath, "m1", c2); err != nil {
		t.Fatal(err)
	}
	if md := parseMessageMetadata(fetchPersonMessage(t, dbPath, "m1").Metadata); md.Classification.Intent != "spam" || len(md.Attachments) != 1 {
		t.Fatalf("reclassify: %+v", md)
	}

	for _, id := range []string{"m-empty", "m-null"} {
		if err := storeClassification(t, dbPath, id, c); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if parseMessageMetadata(fetchPersonMessage(t, dbPath, id).Metadata).Classification == nil {
			t.Fatalf("%s: classification not stored", id)
		}
	}
	for id, orig := range map[string]string{"m-array": `["not","an","object"]`, "m-bad": `{broken`} {
		if err := storeClassification(t, dbPath, id, c); err == nil || !strings.Contains(err.Error(), "not a JSON object") {
			t.Fatalf("%s: non-object metadata must be refused, got %v", id, err)
		}
		if got := fetchPersonMessage(t, dbPath, id).Metadata; got != orig {
			t.Fatalf("%s: refused metadata was changed: %s", id, got)
		}
	}
	if err := storeClassification(t, dbPath, "missing", c); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing message: %v", err)
	}
}

// A sync that adds keys between candidate selection and the store must not
// be overwritten by the stale copy read earlier.
func TestStoreMessageClassificationNoLostUpdate(t *testing.T) {
	dbPath := seedInboxMessages(t)
	var cands []inboxCandidate
	withJevDB(t, dbPath, func(db *storage.Database) {
		var err error
		if cands, err = selectInboxCandidates(db.DB, "default", "", 0, 0, false); err != nil {
			t.Fatal(err)
		}
		// Concurrent Outlook sync: rewrites metadata after we read it.
		if _, err := db.DB.Exec(`UPDATE person_messages SET metadata = json_set(metadata, '$.attachment_error', 'quota') WHERE id = 'm-new'`); err != nil {
			t.Fatal(err)
		}
	})
	if len(cands) != 1 || cands[0].ID != "m-new" || strings.Contains(cands[0].Metadata, "attachment_error") {
		t.Fatalf("candidates = %+v", cands)
	}
	if err := storeClassification(t, dbPath, "m-new", messageClassification{Intent: "lead", IntentP: 0.9}); err != nil {
		t.Fatal(err)
	}
	out := fetchPersonMessage(t, dbPath, "m-new").Metadata
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["attachment_error"]) != `"quota"` || raw["_classification"] == nil || raw["attachments"] == nil {
		t.Fatalf("concurrent update lost: %s", out)
	}
}

func TestInboxSenderName(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ sender, full, want string }{
		{"Bob Buyer", "Robert", "Bob Buyer"},
		{"bob@x.com", "Robert Buyer", "Robert Buyer"},
		{"bob@x.com", "bob@x.com", ""},
		{"", "", ""},
		{`"Bob B" <bob@x.com>`, "", "Bob B"},
		{"<bob@x.com>", "", ""},
	} {
		if got := inboxSenderName(tc.sender, tc.full); got != tc.want {
			t.Errorf("inboxSenderName(%q, %q) = %q, want %q", tc.sender, tc.full, got, tc.want)
		}
	}
}

func seedInboxMessages(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TYPESAFE_API_KEY", "test-key")
	dbPath := newMessagesCLITestDB(t)
	classified, _ := withMessageClassification(`{"_source":{"source":"outlook"}}`,
		messageClassification{Intent: "personal", IntentP: 0.8, Model: "old", At: "2026-01-01T00:00:00Z"})
	seedPersonMessage(t, dbPath, "m-new", "E1", "inbound", "Bob Buyer", "sent", richMetadata)
	seedPersonMessage(t, dbPath, "m-out", "E2", "outbound", "me", "sent", `{}`)
	seedPersonMessage(t, dbPath, "m-done", "E3", "inbound", "Carol", "sent", classified)
	return dbPath
}

func runMessagesClassify(t *testing.T, dbPath string, args ...string) (string, error) {
	t.Helper()
	cfg := &testConfig{DBPath: dbPath, ProfileID: "default"}
	if len(args) > 0 && args[0] == "--json" {
		cfg.JSONOutput, args = true, args[1:]
	}
	var out bytes.Buffer
	cmd := testDeps(cfg).newPeopleMessagesClassifyCmd()
	cmd.SetArgs(args)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	return out.String(), err
}

func TestMessagesClassifyDisabled(t *testing.T) {
	dbPath := seedInboxMessages(t)
	srv := jevtest.NewServer(t, jevtest.Fixed(map[string]string{"intent": "lead"}))
	_, err := runMessagesClassify(t, dbPath)
	if err == nil || !strings.Contains(err.Error(), "jev enable inbox") {
		t.Fatalf("want an error naming `jev enable inbox`, got %v", err)
	}
	if srv.Calls() != 0 {
		t.Fatalf("disabled inbox surface made %d Jev calls", srv.Calls())
	}
	if md := parseMessageMetadata(fetchPersonMessage(t, dbPath, "m-new").Metadata); md.Classification != nil {
		t.Fatal("disabled surface stored a classification")
	}
}

func TestMessagesClassifyStoresSkipsAndReclassifies(t *testing.T) {
	dbPath := seedInboxMessages(t)
	enableJev(t, dbPath, "default", jevconf.Inbox)
	srv := jevtest.NewServer(t, jevtest.Fixed(map[string]string{"intent": "lead", "should_reply": "0.83"}))

	if out, err := runMessagesClassify(t, dbPath); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if srv.Calls() != 1 {
		t.Fatalf("want 1 call (only the unclassified inbound message), got %d", srv.Calls())
	}
	req := srv.Requests()[0]
	state := req.State.(map[string]any)
	if len(state) != 3 || state["sender_name"] != "Bob Buyer" || state["untrusted_subject"] != "Hello" || state["untrusted_body"] != "orig body" {
		t.Fatalf("state = %v", state)
	}
	if len(req.Questions) != 2 || req.Questions["intent"].Type != "choice" || req.Questions["should_reply"].Type != "noul" {
		t.Fatalf("questions = %+v", req.Questions)
	}

	md := parseMessageMetadata(fetchPersonMessage(t, dbPath, "m-new").Metadata)
	if c := md.Classification; c == nil || c.Intent != "lead" || c.IntentP < 0.9 || c.ShouldReplyP != 0.83 || c.Model != "jev-test" || c.At == "" {
		t.Fatalf("classification = %+v", md.Classification)
	}
	if md.Source.Source != "outlook" || len(md.Attachments) != 1 {
		t.Fatalf("classify dropped existing metadata: %+v", md)
	}
	if old := parseMessageMetadata(fetchPersonMessage(t, dbPath, "m-done").Metadata).Classification; old.Intent != "personal" {
		t.Fatalf("already-classified message was touched: %+v", old)
	}
	if md := parseMessageMetadata(fetchPersonMessage(t, dbPath, "m-out").Metadata); md.Classification != nil {
		t.Fatal("outbound message was classified")
	}

	// A second run has nothing left to do.
	if _, err := runMessagesClassify(t, dbPath); err != nil || srv.Calls() != 1 {
		t.Fatalf("re-run: err=%v calls=%d", err, srv.Calls())
	}
	// --reclassify asks again for both inbound messages, one request each.
	if _, err := runMessagesClassify(t, dbPath, "--reclassify"); err != nil || srv.Calls() != 3 {
		t.Fatalf("reclassify: err=%v calls=%d", err, srv.Calls())
	}
	if c := parseMessageMetadata(fetchPersonMessage(t, dbPath, "m-done").Metadata).Classification; c.Intent != "lead" {
		t.Fatalf("--reclassify did not replace: %+v", c)
	}
}

func TestMessagesClassifyBelowThresholdStoresUnsure(t *testing.T) {
	dbPath := seedInboxMessages(t)
	enableJev(t, dbPath, "default", jevconf.Inbox)
	withJevDB(t, dbPath, func(db *storage.Database) {
		if err := jevconf.SetThreshold(db.DB, "default", jevconf.Inbox, 0.99); err != nil {
			t.Fatal(err)
		}
	})
	srv := jevtest.NewServer(t, jevtest.Fixed(map[string]string{"intent": "lead"}))
	out, err := runMessagesClassify(t, dbPath, "--json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"below_threshold": true`) {
		t.Fatalf("out=%s", out)
	}
	md := parseMessageMetadata(fetchPersonMessage(t, dbPath, "m-new").Metadata)
	if md.Classification == nil || !md.Classification.Unsure || md.Classification.Intent != "" {
		t.Fatalf("below-threshold answer should be stored as unsure with no intent: %+v", md.Classification)
	}
	calls := srv.Calls()
	// A second run must not pay for the same message again.
	if _, err := runMessagesClassify(t, dbPath, "--json"); err != nil {
		t.Fatal(err)
	}
	if srv.Calls() != calls {
		t.Fatalf("unsure message was classified again: %d → %d calls", calls, srv.Calls())
	}
}

func TestMessagesListIntentFilter(t *testing.T) {
	dbPath := seedInboxMessages(t)
	lead, _ := withMessageClassification(richMetadata, messageClassification{Intent: "lead", IntentP: 0.9})
	withJevDB(t, dbPath, func(db *storage.Database) {
		if _, err := db.DB.Exec(`UPDATE person_messages SET metadata = ? WHERE id = 'm-new'`, lead); err != nil {
			t.Fatal(err)
		}
	})

	stdout := captureStdout(t, func() {
		cmd := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true}).newPeopleMessagesListCmd()
		cmd.SetArgs([]string{"p1", "--intent", "lead"})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
	})
	var msgs []storage.PersonMessage
	if err := json.Unmarshal([]byte(stdout), &msgs); err != nil {
		t.Fatalf("%v: %s", err, stdout)
	}
	if len(msgs) != 1 || msgs[0].ID != "m-new" {
		t.Fatalf("--intent lead returned %+v", msgs)
	}

	cmd := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default"}).newPeopleMessagesListCmd()
	cmd.SetArgs([]string{"p1", "--intent", "bogus"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("an unknown --intent must be rejected")
	}
}

func TestParseAge(t *testing.T) {
	t.Parallel()
	for in, ok := range map[string]bool{"": true, "12h": true, "7d": true, "1.5d": true, "x": false, "-1h": false} {
		if _, err := parseAge(in); (err == nil) != ok {
			t.Errorf("parseAge(%q) err=%v", in, err)
		}
	}
}
