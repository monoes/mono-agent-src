package peoplelinks

import (
	"context"
	"testing"
)

// R16-D4: the bio comes from `about` (not the DM draft in `introduction`)
// and the review state (`category`) is not sent.
func TestBioReadsAboutAndStateOmitsCategory(t *testing.T) {
	db := newDB(t)
	seed(t, db, person{id: "p1", platform: "LINKEDIN", user: "ann", name: "Ann Lee"})
	if _, err := db.DB.Exec(`UPDATE people SET introduction = 'DM draft text', about = 'Real bio text', category = 'pending_approval' WHERE id = 'p1'`); err != nil {
		t.Fatal(err)
	}
	ps, err := loadPeople(context.Background(), db.DB, "default")
	if err != nil || len(ps) != 1 {
		t.Fatalf("ps = %v, err = %v", ps, err)
	}
	st := personState(ps[0])
	if st["untrusted_bio"] != "Real bio text" {
		t.Errorf("bio = %q", st["untrusted_bio"])
	}
	if _, has := st["category"]; has {
		t.Errorf("category must not be sent: %v", st)
	}
}

func TestDefaultLimitIsFifty(t *testing.T) {
	if DefaultLimit != 50 {
		t.Fatalf("DefaultLimit = %d", DefaultLimit)
	}
}
