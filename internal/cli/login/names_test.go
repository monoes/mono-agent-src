package login

import (
	"context"
	"testing"
)

// Only sessions saved under the placeholder name are looked up, and a row
// with no platform is never one of them.
func TestNamelessListsPlaceholderSessions(t *testing.T) {
	t.Parallel()
	db, _ := newTestDB(t)
	for _, r := range [][2]string{{"x", "unknown"}, {"instagram", "mortezanoes"}, {"tiktok", "unknown"}, {"", "unknown"}} {
		if _, err := db.DB.Exec(`INSERT INTO crawler_sessions (username, platform, cookies_json, expiry, profile_id) VALUES (?, ?, '', '2099-01-01', 'p1')`, r[1], r[0]); err != nil {
			t.Fatal(err)
		}
	}
	got, err := nameless(context.Background(), db.DB, "p1", "")
	if err != nil || len(got) != 2 || got[0] != "tiktok" || got[1] != "x" {
		t.Fatalf("nameless = %v, %v; want [tiktok x]", got, err)
	}
	if got, _ := nameless(context.Background(), db.DB, "p1", "X"); len(got) != 1 || got[0] != "x" {
		t.Fatalf("nameless(--platform X) = %v", got)
	}
}
