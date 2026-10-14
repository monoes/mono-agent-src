package node

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/monoes/mono-agent/internal/bot"
	"github.com/monoes/mono-agent/internal/personitem"
	"github.com/monoes/mono-agent/internal/workflow"
)

// savePostsToDB upserts scraped post items into the posts table.
// Returns (saved, skipped, failed) counts.
func savePostsToDB(ctx context.Context, db *sql.DB, items []workflow.Item, nodeType string, config map[string]interface{}, profileID string) (int, int, int) {
	if profileID == "" {
		profileID = "default"
	}
	// Derive platform from nodeType prefix e.g. "instagram.list_user_posts" → "INSTAGRAM"
	platform := strings.ToUpper(strings.SplitN(nodeType, ".", 2)[0])

	// Resolve person_id: find username from config targets, look up people table scoped to active profile.
	personID := ""
	if postURL := firstTargetURL(config); postURL != "" {
		username := ""
		if factory, ok := bot.PlatformRegistry[platform]; ok {
			username = factory().ExtractUsername(postURL)
		}
		if username != "" {
			_ = db.QueryRowContext(ctx,
				"SELECT id FROM people WHERE platform_username = ? AND UPPER(platform) = ? AND profile_id = ?",
				username, platform, profileID,
			).Scan(&personID)
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	saved, skipped, failed := 0, 0, 0

	for _, item := range items {
		data := item.JSON
		shortcode, _ := data["shortcode"].(string)
		postURL, _ := data["url"].(string)

		// Fallback: extract shortcode from URL if not present as a field.
		if shortcode == "" && postURL != "" {
			shortcode = extractPostShortcode(postURL)
		}
		if shortcode == "" {
			skipped++
			continue
		}
		if postURL == "" {
			skipped++
			continue
		}

		thumbnail, _ := data["thumbnail_src"].(string)
		caption, _ := data["alt_text"].(string)

		var personIDArg interface{}
		if personID != "" {
			personIDArg = personID
		} else if ref, ok := personitem.Resolve(platform, data); ok {
			// No target person: link the post to whoever its URLs name —
			// the owner in its own URL (instagram.com/<user>/p/<code>/) or
			// its author_url — when that person is already saved.
			var authorID string
			if db.QueryRowContext(ctx,
				"SELECT id FROM people WHERE platform_username = ? AND UPPER(platform) = ? AND profile_id = ?",
				ref.Username, platform, profileID,
			).Scan(&authorID) == nil {
				personIDArg = authorID
			}
		}

		_, err := db.ExecContext(ctx,
			`INSERT INTO posts (id, person_id, platform, shortcode, url, thumbnail_url, like_count, comment_count, caption, scraped_at)
             VALUES (?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?)
             ON CONFLICT(platform, shortcode)
             DO UPDATE SET
               thumbnail_url = COALESCE(excluded.thumbnail_url, posts.thumbnail_url),
               caption       = COALESCE(excluded.caption,       posts.caption),
               person_id     = COALESCE(excluded.person_id,     posts.person_id),
               scraped_at    = excluded.scraped_at`,
			uuid.New().String(), personIDArg, platform, shortcode, postURL,
			nullableStrCLI(thumbnail), nullableStrCLI(caption), now,
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Warning: failed to save post %s: %v\n", shortcode, err)
			failed++
		} else {
			saved++
		}
	}
	return saved, skipped, failed
}

// extractPostShortcode extracts the platform shortcode from a post URL.
// Instagram: https://www.instagram.com/p/CD61bhxKOQh/ → "CD61bhxKOQh"
// LinkedIn:  https://www.linkedin.com/posts/user-activity-7123456789/ → "7123456789"
func extractPostShortcode(postURL string) string {
	// Instagram: /p/{shortcode}/ or /reel/{shortcode}/
	parts := strings.Split(strings.Trim(postURL, "/"), "/")
	for i, p := range parts {
		if (p == "p" || p == "reel") && i+1 < len(parts) {
			return parts[i+1]
		}
	}

	// LinkedIn: activity-NNNNNNNN (posts URL) or activity:NNNNNNNN (feed/update URL)
	if strings.Contains(postURL, "linkedin.com") {
		if m := reLinkedInActivity.FindStringSubmatch(postURL); len(m) > 1 {
			return m[1]
		}
	}

	return ""
}

func nullableStrCLI(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// saveCommentsToDB upserts scraped comment items into the post_comments table.
// Returns (saved, skipped, failed) counts.
func saveCommentsToDB(ctx context.Context, db *sql.DB, items []workflow.Item, postID string) (int, int, int) {
	now := time.Now().UTC().Format(time.RFC3339)
	saved, skipped, failed := 0, 0, 0

	for _, item := range items {
		data := item.JSON
		author, _ := data["author"].(string)
		if author == "" {
			skipped++
			continue
		}
		text, _ := data["text"].(string)
		timestamp, _ := data["timestamp"].(string)
		// Leave timestamp as "" if not provided — this is the stable dedup key.
		// Do NOT substitute current time here, as that defeats UNIQUE(post_id, author, timestamp).

		likesCount := int64(0)
		switch v := data["likes_count"].(type) {
		case float64:
			likesCount = int64(v)
		case int64:
			likesCount = v
		}
		replyCount := int64(0)
		switch v := data["reply_count"].(type) {
		case float64:
			replyCount = int64(v)
		case int64:
			replyCount = v
		}

		_, err := db.ExecContext(ctx,
			`INSERT INTO post_comments (id, post_id, author, text, timestamp, likes_count, reply_count, scraped_at)
             VALUES (?, ?, ?, ?, ?, ?, ?, ?)
             ON CONFLICT(post_id, author, timestamp)
             DO UPDATE SET
               text        = COALESCE(excluded.text,        post_comments.text),
               likes_count = excluded.likes_count,
               reply_count = excluded.reply_count,
               scraped_at  = excluded.scraped_at`,
			uuid.New().String(), postID, author,
			nullableStrCLI(text), timestamp, likesCount, replyCount, now,
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Warning: failed to save comment by %s: %v\n", author, err)
			failed++
		} else {
			saved++
		}
	}
	return saved, skipped, failed
}
