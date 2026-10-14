//go:build !nosocial

package node

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// TestSavePostsLinksOwner: saved posts are linked to the person who owns
// them when that person is already saved — through the run's target
// (instagram's single target_url), through the owner in the post's own URL
// (instagram.com/<user>/p/<code>/), or through author_url (LinkedIn's
// /feed/update/ permalinks name nobody). No post ever becomes a person.
func TestSavePostsLinksOwner(t *testing.T) {
	t.Parallel()
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const profile = "prof-1"
	for _, p := range [][3]string{
		{"ig-1", "cafe_test", "INSTAGRAM"},
		{"li-1", "jane-doe-test", "LINKEDIN"},
	} {
		if _, err := db.DB.Exec(`INSERT INTO people (id, platform_username, platform, profile_id) VALUES (?, ?, ?, ?)`, p[0], p[1], p[2], profile); err != nil {
			t.Fatal(err)
		}
	}
	item := func(m map[string]interface{}) workflow.Item { return workflow.Item{JSON: m} }
	ctx := context.Background()

	cases := []struct {
		name      string
		nodeType  string
		config    map[string]interface{}
		items     []workflow.Item
		wantOwner string
	}{
		{"instagram target_url", "instagram.list_user_posts",
			map[string]interface{}{"target_url": "https://www.instagram.com/cafe_test/", "maxCount": 3},
			[]workflow.Item{item(map[string]interface{}{"url": "https://www.instagram.com/p/AAA111/", "shortcode": "AAA111"})},
			"ig-1"},
		{"instagram owner in post URL", "instagram.list_user_posts",
			map[string]interface{}{},
			[]workflow.Item{item(map[string]interface{}{"url": "https://www.instagram.com/cafe_test/p/BBB222/", "shortcode": "BBB222"})},
			"ig-1"},
		{"linkedin bare username target, author_url", "linkedin.list_user_posts",
			map[string]interface{}{"targets": []interface{}{"jane-doe-test"}},
			[]workflow.Item{item(map[string]interface{}{
				"url":        "https://www.linkedin.com/feed/update/urn:li:activity:7000000000000000001/",
				"shortcode":  "7000000000000000001",
				"author_url": "https://www.linkedin.com/in/jane-doe-test/",
			})},
			"li-1"},
	}
	for _, tc := range cases {
		saved, skipped, failed := savePostsToDB(ctx, db.DB, tc.items, tc.nodeType, tc.config, profile)
		if saved != 1 || skipped != 0 || failed != 0 {
			t.Fatalf("%s: saved/skipped/failed = %d/%d/%d", tc.name, saved, skipped, failed)
		}
		sc, _ := tc.items[0].JSON["shortcode"].(string)
		var owner string
		if err := db.DB.QueryRow(`SELECT COALESCE(person_id, '') FROM posts WHERE shortcode = ?`, sc).Scan(&owner); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if owner != tc.wantOwner {
			t.Errorf("%s: post linked to %q, want %q", tc.name, owner, tc.wantOwner)
		}
	}
	var people int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM people`).Scan(&people); err != nil {
		t.Fatal(err)
	}
	if people != 2 {
		t.Errorf("people = %d, want 2 (saving posts must not create people)", people)
	}
}
