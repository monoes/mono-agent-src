package people

import (
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
)

// R12-09 (caller side): a link row naming a person of another profile does not
// leak that person's name or handle into this profile's labels.
func TestConfirmedLinksDoNotLabelAnotherProfilesPerson(t *testing.T) {
	_, db := newLinksCLITestDB(t)
	if _, err := db.DB.Exec(`INSERT INTO people (id, profile_id, platform, platform_username, full_name)
		VALUES ('px', 'other', 'X', 'secret-handle', 'Secret Name')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertPersonLink(&storage.PersonLink{ProfileID: "default", PersonA: "pa", PersonB: "px",
		Relation: storage.PersonLinkSame, Status: storage.PersonLinkConfirmed, Confidence: 1}); err != nil {
		t.Fatal(err)
	}
	for _, c := range confirmedLinks(db, "default", "pa") {
		if c.FullName == "Secret Name" || c.PlatformUsername == "secret-handle" {
			t.Fatalf("leaked another profile's person: %+v", c)
		}
	}
}
