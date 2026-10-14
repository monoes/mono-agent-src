package node

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

func TestFirstTargetURL(t *testing.T) {
	t.Parallel()
	const post = "https://www.instagram.com/p/ABC123/"
	for _, tc := range []struct {
		name   string
		config map[string]interface{}
		want   string
	}{
		{"form-style targets, string", map[string]interface{}{"targets": []interface{}{post}}, post},
		{"targets object with url", map[string]interface{}{"targets": []interface{}{map[string]interface{}{"url": post}}}, post},
		{"targets object with href only", map[string]interface{}{"targets": []interface{}{map[string]interface{}{"href": post}}}, post},
		{"targets object with username only", map[string]interface{}{"targets": []interface{}{map[string]interface{}{"username": post}}}, post},
		{"legacy selectedListItems string", map[string]interface{}{"selectedListItems": []interface{}{post}}, post},
		{"legacy selectedListItems object", map[string]interface{}{"selectedListItems": []interface{}{map[string]interface{}{"url": post}}}, post},
		{"targets wins over selectedListItems", map[string]interface{}{
			"targets": []interface{}{post}, "selectedListItems": []interface{}{"https://other/"}}, post},
		{"single target_url", map[string]interface{}{"target_url": post}, post},
		{"targets wins over target_url", map[string]interface{}{
			"targets": []interface{}{post}, "target_url": "https://other/"}, post},
		{"none", map[string]interface{}{}, ""},
		{"empty targets", map[string]interface{}{"targets": []interface{}{}}, ""},
	} {
		if got := firstTargetURL(tc.config); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	if sc := extractPostShortcode(firstTargetURL(map[string]interface{}{"targets": []interface{}{post}})); sc != "ABC123" {
		t.Errorf("shortcode from targets = %q", sc)
	}
}

// TestAutoSaveCommentsBothKeys: comments are saved under the post named by
// either "targets" (form-style) or the legacy "selectedListItems".
func TestAutoSaveCommentsBothKeys(t *testing.T) {
	t.Parallel()
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, err := db.DB.Exec(`INSERT INTO posts (id, platform, shortcode, url, scraped_at) VALUES ('p1', 'INSTAGRAM', 'ABC123', 'https://www.instagram.com/p/ABC123/', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	const post = "https://www.instagram.com/p/ABC123/"
	for i, config := range []map[string]interface{}{
		{"targets": []interface{}{post}},
		{"targets": []interface{}{map[string]interface{}{"url": post, "href": post, "username": post}}},
		{"selectedListItems": []interface{}{post}},
	} {
		items := []workflow.Item{{JSON: map[string]interface{}{"author": fmt.Sprintf("user%d", i), "text": "nice", "timestamp": "t"}}}
		var out bytes.Buffer
		autoSaveComments(ctx, db.DB, "instagram.list_post_comments", config, items, &out)
		if !strings.Contains(out.String(), "Saved 1 comment(s)") {
			t.Fatalf("config %v: %s", config, out.String())
		}
	}
	var n int
	db.DB.QueryRow(`SELECT COUNT(*) FROM post_comments WHERE post_id = 'p1'`).Scan(&n)
	if n != 3 {
		t.Fatalf("post_comments rows = %d, want 3", n)
	}

	var out bytes.Buffer
	autoSaveComments(ctx, db.DB, "instagram.list_post_comments", map[string]interface{}{"targets": []interface{}{"https://www.instagram.com/p/NOPE/"}},
		[]workflow.Item{{JSON: map[string]interface{}{"author": "x"}}}, &out)
	if !strings.Contains(out.String(), "post not found in DB") {
		t.Fatalf("unknown post: %s", out.String())
	}
}
