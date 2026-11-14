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
	for _, p := range [][2]string{{"p-id-1", "Smoke Name"}, {"p-id-2", "p-id-1"}, {"p-id-3", "Dup"}, {"p-id-4", "dup"}} {
		if _, err := db.DB.Exec(`INSERT INTO profiles (id, name, created_at) VALUES (?, ?, '2026-01-01')`, p[0], p[1]); err != nil {
			t.Fatal(err)
		}
	}
	for name, c := range map[string]struct{ in, want string }{
		"by name":               {"Smoke Name", "p-id-1"},
		"by name, another case": {"smoke name", "p-id-1"},
		"by id":                 {"p-id-1", "p-id-1"},
		"a name equal to another profile's id: id wins": {"p-id-2", "p-id-2"},
		"no profile": {"", ""},
	} {
		in := *cfg
		in.ProfileID = c.in
		got, err := hostConfigWithProfileID(&in, db.DB)
		if err != nil || got.ProfileID != c.want || in.ProfileID != c.in {
			t.Errorf("%s: wired for %q, %v (the caller's config is now %q)", name, got.ProfileID, err, in.ProfileID)
		}
	}
	// an unknown name and an ambiguous one are errors that name the flag; the config comes back unchanged and is never written to
	for name, in := range map[string]string{"unknown": "no such profile", "ambiguous": "DUP"} {
		c := *cfg
		c.ProfileID = in
		got, err := hostConfigWithProfileID(&c, db.DB)
		if err == nil || got.ProfileID != in || c.ProfileID != in {
			t.Errorf("%s: got %q, %v", name, got.ProfileID, err)
		}
	}
}

// The chat paths' cheap check opens its own handle, and that open writes the resolved id into the config it is given: it must be given
// a copy, because the shared one is read by other goroutines of the host. Revert proof: without the copy the shared config changes.
func TestTheCheapOpenModeCheckDoesNotWriteTheSharedConfig(t *testing.T) {
	cfg := sendEnv(t)
	db, err := initDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO profiles (id, name, created_at) VALUES ('p-id-9', 'Cheap Name', '2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	shared := *cfg
	shared.ProfileID = "Cheap Name"
	cheapOpenModeCheck(&shared)
	if shared.ProfileID != "Cheap Name" {
		t.Fatalf("the shared config was rewritten to %q", shared.ProfileID)
	}
}
