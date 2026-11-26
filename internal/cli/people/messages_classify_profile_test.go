package people

import (
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
)

// A message whose own profile disagrees with its person's (only possible past
// migration 172's triggers: an older binary, a hand edit) is classified in
// neither profile: its text must not be sent for one it may not belong to.
func TestInboxCandidatesNeverCrossProfiles(t *testing.T) {
	dbPath := newMessagesCLITestDB(t)
	seedPersonMessage(t, dbPath, "mine", "e1", "inbound", "a@x.com", "sent", "")
	seedPersonMessage(t, dbPath, "stray", "e2", "inbound", "a@x.com", "sent", "")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		"DROP " + "TRIGGER person_messages_profile_on_update",
		`UPDATE person_messages SET profile_id = 'other' WHERE id = 'stray'`,
	} {
		if _, err := db.DB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for profile, want := range map[string]int{"default": 1, "other": 0} {
		got, err := selectInboxCandidates(db.DB, profile, "", 0, 0, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != want || (want == 1 && got[0].ID != "mine") {
			t.Errorf("%s: candidates = %+v, want %d", profile, got, want)
		}
	}
}
