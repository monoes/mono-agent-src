package storage

import "testing"

// A write under another spelling of the platform updates the stored row and
// keeps its spelling; it never adds a twin.
func TestUpsertPersonResolvesPlatformCaseInsensitively(t *testing.T) {
	db := newUpsertPersonTestDB(t)
	if _, err := db.DB.Exec(`INSERT INTO people (id, platform_username, platform, profile_id, full_name) VALUES ('p1', 'ada', 'LINKEDIN', 'pr1', 'Old')`); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertPerson(&Person{PlatformUsername: "ada", Platform: "linkedin", ProfileID: "pr1", FullName: "New"}); err != nil {
		t.Fatal(err)
	}
	var n int
	var name, plat string
	db.DB.QueryRow(`SELECT COUNT(*), MAX(full_name), MAX(platform) FROM people`).Scan(&n, &name, &plat)
	if n != 1 || name != "New" || plat != "LINKEDIN" {
		t.Fatalf("rows=%d name=%q platform=%q, want 1 New LINKEDIN", n, name, plat)
	}
}

func TestResolvePlatform(t *testing.T) {
	db := newUpsertPersonTestDB(t)
	if _, err := db.DB.Exec(`INSERT INTO people (id, platform_username, platform, profile_id) VALUES ('p1', 'ada', 'linkedin', 'pr1')`); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ profile, plat, user, want string }{
		{"pr1", "LINKEDIN", "ada", "linkedin"}, // existing row keeps its spelling
		{"pr1", "linkedin", "ada", "linkedin"},
		{"pr1", "linkedin", "Ada", "LINKEDIN"}, // username is compared as the unique key does: exactly
		{"pr2", "linkedin", "ada", "LINKEDIN"}, // other profile: canonical
		{"pr1", "x", "ada", "X"},
	}
	for _, c := range cases {
		got, err := ResolvePlatform(db.DB, c.profile, c.plat, c.user)
		if err != nil || got != c.want {
			t.Errorf("ResolvePlatform(%v) = %q, %v; want %q", c, got, err, c.want)
		}
	}
}

// The same username in two profiles is two people: a write in one profile
// never resolves to, or updates, the other's row.
func TestResolvePlatformIsPerProfile(t *testing.T) {
	db := newUpsertPersonTestDB(t)
	if _, err := db.DB.Exec(`INSERT INTO people (id, platform_username, platform, profile_id, full_name) VALUES ('a', 'ada', 'linkedin', 'pr1', 'One')`); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertPerson(&Person{PlatformUsername: "ada", Platform: "LINKEDIN", ProfileID: "pr2", FullName: "Two"}); err != nil {
		t.Fatal(err)
	}
	var n int
	var one, two string
	db.DB.QueryRow(`SELECT COUNT(*) FROM people`).Scan(&n)
	db.DB.QueryRow(`SELECT full_name FROM people WHERE profile_id = 'pr1'`).Scan(&one)
	db.DB.QueryRow(`SELECT full_name || '/' || platform FROM people WHERE profile_id = 'pr2'`).Scan(&two)
	if n != 2 || one != "One" || two != "Two/LINKEDIN" {
		t.Fatalf("rows=%d pr1=%q pr2=%q", n, one, two)
	}
}

// Twins that already exist: the choice is deterministic (an exact spelling
// first, then the most recently updated), so the write and the guard agree.
func TestResolvePlatformPicksATwinDeterministically(t *testing.T) {
	db := newUpsertPersonTestDB(t)
	for _, r := range [][]string{{"a", "linkedin", "2026-01-01 00:00:00"}, {"b", "LINKEDIN", "2026-02-01 00:00:00"}} {
		if _, err := db.DB.Exec(`INSERT INTO people (id, platform_username, platform, profile_id, updated_at) VALUES (?, 'ada', ?, 'pr1', ?)`, r[0], r[1], r[2]); err != nil {
			t.Fatal(err)
		}
	}
	for ask, want := range map[string]string{"linkedin": "linkedin", "LINKEDIN": "LINKEDIN", "LinkedIn": "LINKEDIN"} {
		got, err := ResolvePlatform(db.DB, "pr1", ask, "ada")
		if err != nil || got != want {
			t.Errorf("ResolvePlatform(%q) = %q, %v; want %q", ask, got, err, want)
		}
	}
}
