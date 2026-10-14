package people

import (
	"encoding/json"
	"testing"
)

func TestPeopleDeleteCLI(t *testing.T) {
	cfg, db := newReviewCLITestDB(t)
	if _, err := db.DB.Exec(`INSERT INTO people (id, platform_username, platform, profile_id) VALUES ('p-extra', 'extra-two', 'X', ?)`, cfg.ProfileID); err != nil {
		t.Fatal(err)
	}

	out, err := runPeople(t, cfg, "delete", "p1", "p-extra")
	var res struct{ Deleted int }
	if err != nil || json.Unmarshal([]byte(out), &res) != nil || res.Deleted != 2 {
		t.Fatalf("delete p1 p2 = %q, %v", out, err)
	}
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM people WHERE id IN ('p1','p-extra')`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d of the deleted people remain (%v)", n, err)
	}
	if _, err := runPeople(t, cfg, "delete", "p1"); err == nil {
		t.Fatal("deleting a person that is gone should fail")
	}
}
