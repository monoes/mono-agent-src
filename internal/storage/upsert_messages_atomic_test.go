package storage

import (
	"strings"
	"testing"
)

// A row that fails partway through the batch rolls back every row before it,
// and the error names the row and says nothing was stored.
func TestUpsertPersonMessagesFailsPartwayStoresNothing(t *testing.T) {
	db := newUpsertPersonTestDB(t)
	if _, err := db.DB.Exec(`INSERT INTO people (id, platform_username, platform, profile_id) VALUES ('p1', 'ada@example.com', 'email', 'pr1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`CREATE TRIGGER fail_row BEFORE INSERT ON person_messages WHEN NEW.external_id = 'boom' BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	msgs := []*PersonMessage{
		{PersonID: "p1", Source: "gmail", ExternalID: "a", Body: "one"},
		{PersonID: "p1", Source: "gmail", ExternalID: "b", Body: "two"},
		{PersonID: "p1", Source: "gmail", ExternalID: "boom", Body: "three"},
		{PersonID: "p1", Source: "gmail", ExternalID: "d", Body: "four"},
	}
	err := db.UpsertPersonMessages(msgs, "pr1")
	if err == nil {
		t.Fatal("a failing row was accepted")
	}
	if !strings.Contains(err.Error(), "row 3") || !strings.Contains(err.Error(), "none were stored") {
		t.Errorf("error does not name the row and say none were stored: %v", err)
	}
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM person_messages`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d rows stored from a batch that failed on row 3", n)
	}
}
