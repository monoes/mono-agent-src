package node

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"strings"

	"github.com/monoes/mono-agent/internal/workflow"
)

// firstTargetURL returns the URL of the first list target a node run was
// given, reading the keys the browser adapter reads and in the same order
// (internal/nodes/browser_adapter.go): "targets", else the legacy
// "selectedListItems", else a single "target_url". A target is a string (the URL itself) or an object;
// for an object the adapter's normalized fields are tried: url, href, then
// username. "" when there is none.
func firstTargetURL(config map[string]interface{}) string {
	raw, ok := config["targets"]
	if !ok {
		raw = config["selectedListItems"]
	}
	if raw == nil {
		// Single-target nodes (instagram.list_user_posts) name it target_url.
		if s, _ := config["target_url"].(string); s != "" {
			return s
		}
	}
	items, ok := raw.([]interface{})
	if !ok || len(items) == 0 {
		return ""
	}
	switch v := items[0].(type) {
	case string:
		return v
	case map[string]interface{}:
		for _, k := range []string{"url", "href", "username"} {
			if s, ok := v[k].(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// autoSaveComments stores list_post_comments results in post_comments under
// the post named by the run's first target (it must already be in posts,
// saved by list_user_posts). Progress goes to w.
func autoSaveComments(ctx context.Context, db *sql.DB, nodeType string, config map[string]interface{}, items []workflow.Item, w io.Writer) {
	platform := strings.ToUpper(strings.SplitN(nodeType, ".", 2)[0])
	postID := ""
	if shortcode := extractPostShortcode(firstTargetURL(config)); shortcode != "" {
		_ = db.QueryRowContext(ctx,
			"SELECT id FROM posts WHERE platform = ? AND shortcode = ?",
			platform, shortcode,
		).Scan(&postID)
	}
	if postID == "" {
		fmt.Fprintf(w, "  Warning: post not found in DB — run list_user_posts first\n")
		return
	}
	saved, skipped, failed := saveCommentsToDB(ctx, db, items, postID)
	fmt.Fprintf(w, "  Saved %d comment(s) to post_comments table (%d skipped, %d failed)\n", saved, skipped, failed)
}
