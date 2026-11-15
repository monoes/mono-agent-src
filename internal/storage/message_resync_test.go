package storage

import (
	"testing"
)

func messageRow(t *testing.T, db *Database, id string) (status, metadata string) {
	t.Helper()
	if err := db.DB.QueryRow(`SELECT status, COALESCE(metadata,'') FROM person_messages WHERE id = ?`, id).Scan(&status, &metadata); err != nil {
		t.Fatal(err)
	}
	return
}

func jsonField(t *testing.T, db *Database, metadata, path string) string {
	t.Helper()
	var v *string
	if err := db.DB.QueryRow(`SELECT json_extract(?, ?)`, metadata, path).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v == nil {
		return ""
	}
	return *v
}

func seedResyncMessage(t *testing.T, db *Database, direction string) {
	t.Helper()
	if _, err := db.DB.Exec(`INSERT INTO people (id, platform_username, platform, profile_id) VALUES ('p1', 'ada@example.com', 'email', 'pr1')`); err != nil {
		t.Fatal(err)
	}
	m := &PersonMessage{ID: "m1", PersonID: "p1", Source: "outlook", ExternalID: "ext1", Direction: direction, Subject: "old", Metadata: `{"_source":{"account":"old@x.com"}}`}
	if err := db.UpsertPersonMessage(m, "pr1"); err != nil {
		t.Fatal(err)
	}
}

// A re-sync refreshes what the platform provides and keeps what the user or
// the classify flow stored in the metadata.
func TestPersonMessageResyncKeepsClassification(t *testing.T) {
	db := newUpsertPersonTestDB(t)
	seedResyncMessage(t, db, "inbound")
	if _, err := db.DB.Exec(`UPDATE person_messages SET metadata = json_set(metadata, '$._classification', json('{"intent":"interested"}')) WHERE id = 'm1'`); err != nil {
		t.Fatal(err)
	}
	again := &PersonMessage{ID: "m1b", PersonID: "p1", Source: "outlook", ExternalID: "ext1", Direction: "inbound", Subject: "new", Metadata: `{"_source":{"account":"new@x.com"},"attachments":[{"name":"a.pdf"}]}`}
	if err := db.UpsertPersonMessage(again, "pr1"); err != nil {
		t.Fatal(err)
	}
	_, md := messageRow(t, db, "m1")
	if got := jsonField(t, db, md, "$._classification.intent"); got != "interested" {
		t.Errorf("_classification lost on re-sync: %s", md)
	}
	if got := jsonField(t, db, md, "$._source.account"); got != "new@x.com" {
		t.Errorf("provenance not refreshed: %s", md)
	}
	if got := jsonField(t, db, md, "$.attachments[0].name"); got != "a.pdf" {
		t.Errorf("attachments not refreshed: %s", md)
	}
}

// A re-sync or re-import that names no metadata leaves the stored metadata.
func TestPersonMessageResyncWithoutMetadataKeepsIt(t *testing.T) {
	db := newUpsertPersonTestDB(t)
	seedResyncMessage(t, db, "inbound")
	again := &PersonMessage{PersonID: "p1", Source: "outlook", ExternalID: "ext1", Direction: "inbound", Subject: "new"}
	if err := db.UpsertPersonMessage(again, "pr1"); err != nil {
		t.Fatal(err)
	}
	if _, md := messageRow(t, db, "m1"); jsonField(t, db, md, "$._source.account") != "old@x.com" {
		t.Errorf("metadata wiped by a metadata-less re-upsert: %q", md)
	}
}

// A re-sync (which names no status) never resets the status of a message the
// operator moved out of 'sent', and an explicit status cannot move it either.
func TestPersonMessageResyncKeepsOperatorStatus(t *testing.T) {
	for _, st := range []string{"draft", "pending_approval", "approved", "sending", "rejected", "failed", "expired"} {
		t.Run(st, func(t *testing.T) {
			db := newUpsertPersonTestDB(t)
			seedResyncMessage(t, db, "outbound")
			if _, err := db.DB.Exec(`UPDATE person_messages SET status = ? WHERE id = 'm1'`, st); err != nil {
				t.Fatal(err)
			}
			for _, incoming := range []string{"", "sent", "draft"} {
				again := &PersonMessage{PersonID: "p1", Source: "outlook", ExternalID: "ext1", Direction: "outbound", Status: incoming}
				if err := db.UpsertPersonMessage(again, "pr1"); err != nil {
					t.Fatal(err)
				}
				if got, _ := messageRow(t, db, "m1"); got != st {
					t.Fatalf("incoming status %q moved %q to %q", incoming, st, got)
				}
			}
		})
	}
}

// A message we sent stays sent; a synced inbound message takes an explicit
// status but not the default.
func TestPersonMessageResyncStatusRules(t *testing.T) {
	db := newUpsertPersonTestDB(t)
	seedResyncMessage(t, db, "outbound")
	again := &PersonMessage{PersonID: "p1", Source: "outlook", ExternalID: "ext1", Direction: "outbound", Status: "failed"}
	if err := db.UpsertPersonMessage(again, "pr1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := messageRow(t, db, "m1"); got != "sent" {
		t.Errorf("a sent-by-us message moved to %q", got)
	}

	// A synced inbound message takes only the status a sync sets; an incoming
	// approved or sending is ignored.
	for _, in := range []string{"approved", "sending", "draft", "pending_approval"} {
		db2 := newUpsertPersonTestDB(t)
		seedResyncMessage(t, db2, "inbound")
		again := &PersonMessage{PersonID: "p1", Source: "outlook", ExternalID: "ext1", Direction: "inbound", Status: in}
		if err := db2.UpsertPersonMessage(again, "pr1"); err != nil {
			t.Fatal(err)
		}
		if got, _ := messageRow(t, db2, "m1"); got != "sent" {
			t.Errorf("incoming %q on a non-operator row = %q, want sent", in, got)
		}
	}
}

// null in the incoming metadata deletes nothing; incoming _-prefixed keys
// other than the sync's own provenance, and the send flow's bookkeeping, do
// not replace the stored ones.
func TestPersonMessageResyncMetadataCannotReachOperatorKeys(t *testing.T) {
	db := newUpsertPersonTestDB(t)
	seedResyncMessage(t, db, "inbound")
	stored := `{"_source":{"account":"old@x.com"},"_classification":{"intent":"interested"},"_approval":{"by":"user"},"workflow":{"target_nodes":["n1"]},"hil_ref":"h1"}`
	if _, err := db.DB.Exec(`UPDATE person_messages SET metadata = ? WHERE id = 'm1'`, stored); err != nil {
		t.Fatal(err)
	}
	incoming := `{"_source":{"account":"new@x.com"},"_classification":null,"_approval":{"by":"attacker"},"workflow":null,"hil_ref":"x","attachments":[{"name":"a.pdf"}],"attachment_error":null}`
	again := &PersonMessage{PersonID: "p1", Source: "outlook", ExternalID: "ext1", Direction: "inbound", Metadata: incoming}
	if err := db.UpsertPersonMessage(again, "pr1"); err != nil {
		t.Fatal(err)
	}
	_, md := messageRow(t, db, "m1")
	for path, want := range map[string]string{
		"$._classification.intent":   "interested",
		"$._approval.by":             "user",
		"$.workflow.target_nodes[0]": "n1",
		"$.hil_ref":                  "h1",
		"$._source.account":          "new@x.com",
		"$.attachments[0].name":      "a.pdf",
	} {
		if got := jsonField(t, db, md, path); got != want {
			t.Errorf("%s = %q, want %q (metadata %s)", path, got, want, md)
		}
	}
}

func TestPersonMessageResyncMetadataShapes(t *testing.T) {
	cases := []struct{ name, stored, incoming, path, want string }{
		{"non-object stored is replaced by the sanitised incoming", `"just a string"`, `{"_source":{"account":"a"},"_approval":1}`, "$._source.account", "a"},
		{"array stored is replaced", `[1,2]`, `{"attachments":[{"name":"z"}]}`, "$.attachments[0].name", "z"},
		{"non-object incoming keeps stored", `{"_classification":{"intent":"i"}}`, `["x"]`, "$._classification.intent", "i"},
		{"invalid incoming keeps stored", `{"_classification":{"intent":"i"}}`, `{not json`, "$._classification.intent", "i"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := newUpsertPersonTestDB(t)
			seedResyncMessage(t, db, "inbound")
			if _, err := db.DB.Exec(`UPDATE person_messages SET metadata = ? WHERE id = 'm1'`, c.stored); err != nil {
				t.Fatal(err)
			}
			again := &PersonMessage{PersonID: "p1", Source: "outlook", ExternalID: "ext1", Direction: "inbound", Metadata: c.incoming}
			if err := db.UpsertPersonMessage(again, "pr1"); err != nil {
				t.Fatal(err)
			}
			_, md := messageRow(t, db, "m1")
			if got := jsonField(t, db, md, c.path); got != c.want {
				t.Errorf("%s = %q, want %q (metadata %s)", c.path, got, c.want, md)
			}
			if got := jsonField(t, db, md, "$._approval"); got != "" {
				t.Errorf("an incoming _approval reached the row: %s", md)
			}
		})
	}
}

// Text the operator approved is not rewritten by a re-upsert of the same
// message; an inbound message still refreshes.
func TestPersonMessageResyncKeepsApprovedText(t *testing.T) {
	for _, st := range []string{"draft", "pending_approval", "approved", "sending"} {
		t.Run(st, func(t *testing.T) {
			db := newUpsertPersonTestDB(t)
			seedResyncMessage(t, db, "outbound")
			if _, err := db.DB.Exec(`UPDATE person_messages SET status = ?, body = 'approved text', subject = 'approved subject' WHERE id = 'm1'`, st); err != nil {
				t.Fatal(err)
			}
			again := &PersonMessage{PersonID: "p1", Source: "outlook", ExternalID: "ext1", Direction: "inbound", Subject: "evil subject", Body: "evil body"}
			if err := db.UpsertPersonMessage(again, "pr1"); err != nil {
				t.Fatal(err)
			}
			var body, subject, dir string
			db.DB.QueryRow(`SELECT body, subject, direction FROM person_messages WHERE id = 'm1'`).Scan(&body, &subject, &dir)
			if body != "approved text" || subject != "approved subject" || dir != "outbound" {
				t.Fatalf("approved message rewritten: %q %q %q", body, subject, dir)
			}
		})
	}
	db := newUpsertPersonTestDB(t)
	seedResyncMessage(t, db, "inbound")
	again := &PersonMessage{PersonID: "p1", Source: "outlook", ExternalID: "ext1", Direction: "inbound", Subject: "edited upstream", Body: "new body"}
	if err := db.UpsertPersonMessage(again, "pr1"); err != nil {
		t.Fatal(err)
	}
	var body, subject string
	db.DB.QueryRow(`SELECT body, subject FROM person_messages WHERE id = 'm1'`).Scan(&body, &subject)
	if body != "new body" || subject != "edited upstream" {
		t.Fatalf("an inbound message did not refresh: %q %q", body, subject)
	}
}
