package node

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
	"github.com/monoes/mono-agent/internal/workflow"
)

// Two profiles scraping the same post each get their own row, and comments
// land on (and only on) the scraping profile's post.
func TestPostsAreProfileScoped(t *testing.T) {
	t.Parallel()
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	testdb.AddDefaultProfile(t, db.DB)
	defer db.Close()
	ctx := context.Background()

	const post = "https://www.instagram.com/p/SAME123/"
	items := []workflow.Item{{JSON: map[string]interface{}{"url": post, "shortcode": "SAME123"}}}
	config := map[string]interface{}{}
	for _, profile := range []string{"prof-a", "prof-b"} {
		for i := 0; i < 2; i++ { // second pass exercises the ON CONFLICT upsert
			if saved, _, failed := savePostsToDB(ctx, db.DB, items, "instagram.list_user_posts", config, profile); saved != 1 || failed != 0 {
				t.Fatalf("%s pass %d: saved=%d failed=%d", profile, i, saved, failed)
			}
		}
	}
	var n int
	db.DB.QueryRow(`SELECT COUNT(*) FROM posts WHERE shortcode = 'SAME123'`).Scan(&n)
	if n != 2 {
		t.Fatalf("posts rows = %d, want one per profile (2)", n)
	}

	var out bytes.Buffer
	comment := []workflow.Item{{JSON: map[string]interface{}{"author": "u", "text": "hi", "timestamp": "t"}}}
	autoSaveComments(ctx, db.DB, "instagram.list_post_comments", map[string]interface{}{"targets": []interface{}{post}}, comment, "prof-a", &out)

	var a, b int
	db.DB.QueryRow(`SELECT COUNT(*) FROM post_comments c JOIN posts p ON p.id = c.post_id WHERE p.profile_id = 'prof-a'`).Scan(&a)
	db.DB.QueryRow(`SELECT COUNT(*) FROM post_comments c JOIN posts p ON p.id = c.post_id WHERE p.profile_id = 'prof-b'`).Scan(&b)
	if a != 1 || b != 0 {
		t.Fatalf("comments prof-a=%d prof-b=%d, want 1 and 0", a, b)
	}

	out.Reset()
	autoSaveComments(ctx, db.DB, "instagram.list_post_comments", map[string]interface{}{"targets": []interface{}{post}}, comment, "prof-c", &out)
	if !bytes.Contains(out.Bytes(), []byte("post not found in DB")) {
		t.Fatalf("profile without the post must not see another profile's row: %s", out.String())
	}
}

// A run with no resolved profile stores no posts and no comments: there is no
// implicit "default" profile to put them in.
func TestEmptyProfileSavesNothing(t *testing.T) {
	t.Parallel()
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	testdb.AddDefaultProfile(t, db.DB)
	defer db.Close()
	ctx := context.Background()

	const post = "https://www.instagram.com/p/EMPTY12/"
	items := []workflow.Item{{JSON: map[string]interface{}{"url": post, "shortcode": "EMPTY12"}}}
	if saved, _, failed := savePostsToDB(ctx, db.DB, items, "instagram.list_user_posts", map[string]interface{}{}, ""); saved != 0 || failed != 1 {
		t.Fatalf("saved=%d failed=%d, want 0 saved and 1 failed", saved, failed)
	}
	var out bytes.Buffer
	comment := []workflow.Item{{JSON: map[string]interface{}{"author": "u", "text": "hi", "timestamp": "t"}}}
	autoSaveComments(ctx, db.DB, "instagram.list_post_comments", map[string]interface{}{"targets": []interface{}{post}}, comment, "", &out)
	if !bytes.Contains(out.Bytes(), []byte("no profile selected")) {
		t.Fatalf("want a no-profile warning, got: %s", out.String())
	}
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM posts`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("posts rows = %d (%v), want 0", n, err)
	}
}
