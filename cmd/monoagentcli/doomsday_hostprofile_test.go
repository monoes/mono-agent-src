package main

import "testing"

// A coder host started by hand with `--profile NAME` is wired for the profile's id: the gate compares it with the id every turn
// carries. Revert proof: with the name left in place the gate refuses every turn ("wired for profile NAME, not ID").
func TestTheHostGateIsWiredForTheProfileIDWhenTheHostWasStartedByName(t *testing.T) {
	cfg := sendEnv(t)
	db, err := initDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.DB.Exec(`INSERT INTO profiles (id, name, created_at) VALUES ('p-id-1', 'Smoke Name', '2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	byName := *cfg
	byName.ProfileID = "Smoke Name"
	if got := hostConfigWithProfileID(&byName, db.DB); got.ProfileID != "p-id-1" || byName.ProfileID != "Smoke Name" {
		t.Errorf("by name: wired for %q (the caller's config is now %q)", got.ProfileID, byName.ProfileID)
	}
	byID := *cfg
	byID.ProfileID = "p-id-1"
	if got := hostConfigWithProfileID(&byID, db.DB); got.ProfileID != "p-id-1" {
		t.Errorf("by id: %q", got.ProfileID)
	}
	unknown := *cfg
	unknown.ProfileID = "no such profile"
	if got := hostConfigWithProfileID(&unknown, db.DB); got.ProfileID != "no such profile" {
		t.Errorf("an unknown name was changed to %q", got.ProfileID)
	}
	none := *cfg
	none.ProfileID = ""
	if got := hostConfigWithProfileID(&none, db.DB); got.ProfileID != "" {
		t.Errorf("no profile became %q", got.ProfileID)
	}
}
