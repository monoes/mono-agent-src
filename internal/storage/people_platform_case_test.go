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
