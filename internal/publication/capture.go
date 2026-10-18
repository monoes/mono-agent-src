package publication

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Source identifies the operation that actually published an item. Profile scope
// belongs to the Store, never to a node's config or a publisher's result.
type Source struct {
	WorkflowID, ExecutionID, NodeID, AgentID, OrgID, RoleID string
}

type Output struct {
	Handle string
	Items  []map[string]interface{}
}
type CaptureInput struct {
	NodeType  string
	Config    map[string]interface{}
	Outputs   []Output
	Source    Source
	AttemptID string
}

// Normalize only recognizes explicitly inventoried publishing operations. It
// intentionally does not inspect HTTP, code, shell, mail, or direct-message nodes.
func Normalize(in CaptureInput) []Entry {
	c := in.Config
	op := field(c, "operation")
	kind, platform := "", ""
	switch in.NodeType {
	case "service.bluesky", "comm.bluesky":
		if op == "create_post" {
			kind, platform = "post", "bluesky"
		}
	case "service.devto":
		if op == "publish_article" && c["published"] != false {
			kind, platform = "article", "devto"
		}
		if op == "create_comment" {
			kind, platform = "comment", "devto"
		}
	case "service.producthunt":
		if op == "create_comment" {
			kind, platform = "comment", "producthunt"
		}
	case "service.github":
		if op == "create_release" {
			kind, platform = "other", "github"
		}
		if op == "create_issue" || op == "create_pr" {
			kind, platform = "other", "github"
		}
	case "service.linear":
		if op == "create_issue" {
			kind, platform = "other", "linear"
		}
	case "service.notion":
		if op == "create_page" {
			kind, platform = "article", "notion"
		}
	case "service.jira":
		if op == "create_issue" {
			kind, platform = "other", "jira"
		}
		if op == "add_comment" {
			kind, platform = "comment", "jira"
		}
	case "service.hashnode":
		if op == "publish_post" {
			kind, platform = "article", "hashnode"
		}
	case "service.mastodon", "comm.mastodon":
		if (op == "publish_status" || op == "create_status") && field(c, "visibility") != "direct" {
			kind, platform = "post", "mastodon"
			if field(c, "in_reply_to_id") != "" {
				kind = "reply"
			}
		}
	case "service.reddit", "comm.reddit":
		if op == "submit_post" {
			kind, platform = "post", "reddit"
		}
		if op == "comment" || op == "reply_to_comment" {
			kind, platform = "comment", "reddit"
			if op == "reply_to_comment" {
				kind = "reply"
			}
		}
	case "service.youtube":
		if op == "upload_video" && field(c, "privacy_status") != "private" && field(c, "publish_at") == "" {
			kind, platform = "video", "youtube"
		}
		if op == "reply_to_comment" {
			kind, platform = "reply", "youtube"
		}
	case "comm.slack":
		channel := field(c, "channel")
		if channel != "" && slackShared(channel) {
			if op == "post_message" {
				kind, platform = "post", "slack"
			}
			if op == "upload_file" {
				kind, platform = "post", "slack"
			}
		}
	case "comm.discord", "service.discord":
		if op == "send_message" || op == "send_embed" || (op == "" && in.NodeType == "comm.discord") {
			kind, platform = "post", "discord"
		}
	case "comm.telegram":
		if op == "send_message" || op == "send_photo" || op == "" {
			kind, platform = "post", "telegram"
		}
	case "hackernews.submit_post":
		kind, platform = "post", "hackernews"
	case "hackernews.reply_to_comment":
		kind, platform = "reply", "hackernews"
	}
	if kind == "" {
		return nil
	}
	entries := []Entry{}
	for _, out := range in.Outputs {
		if out.Handle == "error" {
			continue
		}
		for i, result := range out.Items {
			if rejected(result) || (platform == "mastodon" && field(result, "visibility") == "direct") || (platform == "github" && result["draft"] == true) {
				continue
			}
			if platform == "youtube" && op == "upload_video" {
				if status, ok := result["status"].(map[string]interface{}); ok && (field(status, "privacyStatus") == "private" || field(status, "publishAt") != "") {
					continue
				}
			}
			if platform == "slack" {
				channel := field(result, "channel")
				if channel == "" {
					channel = field(c, "channel")
				}
				if !slackShared(channel) {
					continue
				}
			}
			if platform == "discord" && !discordShared(c, result) {
				continue
			}
			if platform == "telegram" && field(result, "chat_type") != "channel" && field(result, "chat_type") != "supergroup" && field(result, "chat_type") != "group" {
				continue
			}
			if platform == "hackernews" && field(result, "id", "comment_id") == "" && field(result, "url") == "" {
				continue
			}
			e := base(in.Source, platform, kind)
			e.Title = field(c, "title", "embed_title")
			// Read the exact fields each publisher sends, including precedence and
			// operation-specific fields; retained editor config may contain stale
			// fields from another operation.
			switch in.NodeType {
			case "service.devto":
				e.Body = field(c, "body_markdown")
			case "service.hashnode":
				e.Body = field(c, "content_markdown")
			case "service.producthunt":
				e.Body = field(c, "body")
			case "service.jira":
				if op == "create_issue" {
					e.Title = field(c, "summary")
					e.Body = field(c, "description")
				} else {
					e.Body = field(c, "comment")
				}
			case "service.github":
				e.Body = field(c, "body")
				if op == "create_release" {
					e.Title = field(c, "release_name", "tag_name")
				}
			case "service.linear":
				e.Body = field(c, "description")
			case "service.notion":
				content := map[string]interface{}{"properties": c["properties"], "children": c["children"]}
				if encoded, err := json.Marshal(content); err == nil {
					e.Body = string(encoded)
				}
			case "comm.discord":
				if op == "send_embed" {
					e.Title = field(c, "embed_title")
					e.Body = field(c, "embed_description")
				} else {
					e.Title = ""
					e.Body = field(c, "content")
				}
			case "comm.telegram":
				if message, ok := c["message"].(string); ok {
					e.Body = message
				} else {
					e.Body = field(c, "text")
				}
			case "comm.slack":
				e.Body = field(c, "text")
				if blocks, ok := c["blocks"].([]interface{}); ok && len(blocks) > 0 {
					if encoded, err := json.Marshal(blocks); err == nil {
						if e.Body != "" {
							e.Body += "\n"
						}
						e.Body += string(encoded)
					}
				}
			case "service.youtube":
				if op == "upload_video" {
					e.Body = field(c, "description")
				} else {
					e.Body = field(c, "text")
				}
			default:
				e.Body = field(c, "text")
			}
			if kind == "comment" || kind == "reply" {
				e.Title = ""
			}
			e.URL = field(result, "html_url", "web_url", "post_url", "permalink", "url")
			e.RemoteID = field(result, "id", "message_id", "file_id", "uri", "comment_id", "commentID", "id_code")
			e.ParentURL = field(c, "parent_url", "thing_id", "parent_id", "comment_id", "in_reply_to_id", "commentable_id", "post_id", "issue_key", "itemID")
			e.Account = field(c, "account", "username", "identifier", "channel", "channel_id", "chat_id", "instance", "instance_url")
			if platform == "jira" {
				e.Account = strings.TrimRight(field(c, "base_url", "domain"), "/")
				if e.Account != "" && !strings.Contains(e.Account, "://") {
					e.Account = "https://" + e.Account
				}
			}
			if platform == "github" {
				e.Account = field(c, "owner") + "/" + field(c, "repo")
				if e.Account == "/" {
					e.Account = ""
				}
				// GitHub releases, issues and pull requests expose separate numeric
				// identifier namespaces while sharing the publication kind "other".
				if e.RemoteID != "" {
					e.RemoteID = strings.TrimPrefix(op, "create_") + ":" + e.RemoteID
				}
			}
			// Resolve channel-local IDs from the returned destination when aliases
			// were used in config (two aliases must still deduplicate one result).
			if platform == "slack" || platform == "discord" || platform == "telegram" {
				if destination := field(result, "channel", "channel_id", "chat_id"); destination != "" {
					e.Account = destination
				}
			}
			switch in.NodeType {
			case "service.mastodon":
				e.Media = media(c["media_ids"])
			case "service.youtube":
				if op == "upload_video" {
					e.Media = media(c["video_file_path"])
				}
			case "comm.telegram":
				if op == "send_photo" {
					e.Media = media(c["photo_url"])
				}
			case "comm.slack":
				if op == "upload_file" {
					e.Media = media(c["file_path"])
				}
			}
			// Discord, Telegram and Slack identities are local to a channel/chat.
			if platform == "slack" && e.RemoteID == "" {
				e.RemoteID = field(result, "timestamp")
			}
			if platform == "reddit" {
				if things, ok := result["things"].([]interface{}); ok && len(things) > 0 {
					if thing, ok := things[0].(map[string]interface{}); ok {
						if d, ok := thing["data"].(map[string]interface{}); ok {
							e.RemoteID = field(d, "name", "id")
							e.URL = field(d, "permalink")
							if strings.HasPrefix(e.URL, "/") {
								e.URL = "https://www.reddit.com" + e.URL
							}
						}
					}
				}
			}
			if platform == "reddit" && strings.HasPrefix(e.URL, "/") {
				e.URL = "https://www.reddit.com" + e.URL
			}
			if platform == "youtube" && e.RemoteID != "" && kind == "video" {
				e.URL = "https://www.youtube.com/watch?v=" + e.RemoteID
			}
			e.IdempotencyKey = fmt.Sprintf("publication:%s:%s:%s:%s:%s:%s:%d:%s:%s", e.Platform, e.Account, e.Kind, in.Source.ExecutionID, in.Source.NodeID, out.Handle, i, e.RemoteID, e.URL)
			if e.RemoteID == "" && e.URL == "" && in.AttemptID != "" {
				e.IdempotencyKey += ":" + in.AttemptID
			}
			entries = append(entries, e)
		}
	}
	return entries
}

func slackShared(channel string) bool {
	return strings.HasPrefix(channel, "C") || strings.HasPrefix(channel, "G") || strings.HasPrefix(channel, "#")
}

func discordShared(c, r map[string]interface{}) bool {
	// Returned channel metadata is authoritative; a supplied guild_id cannot
	// turn a private channel into a shared one.
	switch field(r, "channel_type") {
	case "1", "3":
		return false
	case "0", "2", "5", "10", "11", "12", "13", "15", "16":
		return true
	}
	return field(r, "guild_id") != ""
}

// BrowserPublication normalizes one confirmed native publishing call with its
// fully resolved arguments. Engagement actions may publish multiple different
// comments, so final merged node output cannot supply their original content.
func BrowserPublication(platform, method string, args []interface{}, result map[string]interface{}, source Source, account string, sequence int) *Entry {
	if rejected(result) {
		return nil
	}
	platform = strings.ToLower(platform)
	kind, bodyIndex, mediaIndex := "", -1, -1
	switch platform + "." + method {
	case "instagram.publish_content":
		kind, bodyIndex, mediaIndex = "post", 1, 0
	case "linkedin.publish_post", "x.publish_post":
		kind, bodyIndex, mediaIndex = "post", 0, 1
	case "tiktok.publish_uploaded_video":
		kind, bodyIndex = "video", 0
	case "instagram.comment_post", "linkedin.comment_on_post", "tiktok.comment_on_video", "producthunt.comment_on_launch":
		kind, bodyIndex = "comment", 1
	case "x.reply_post":
		kind, bodyIndex = "reply", 1
	case "instagram.reply_comment":
		kind, bodyIndex = "reply", 2
	default:
		return nil
	}
	e := base(source, platform, kind)
	e.Account = account
	if bodyIndex < len(args) && bodyIndex >= 0 {
		e.Body = scalar(args[bodyIndex])
	}
	if mediaIndex < len(args) && mediaIndex >= 0 {
		e.Media = media(args[mediaIndex])
	}
	e.RemoteID = field(result, "comment_id", "commentID", "id", "post_id", "reply_id")
	e.URL = field(result, "comment_url", "reply_url", "post_url")
	if kind == "post" || kind == "video" {
		if e.URL == "" {
			e.URL = field(result, "url")
		}
	}
	if body := field(result, "post_text", "reply_text", "text"); body != "" {
		e.Body = body
	}
	if kind == "comment" || kind == "reply" {
		if len(args) > 0 {
			e.ParentURL = scalar(args[0])
		}
		if platform == "linkedin" && len(args) > 2 && scalar(args[2]) != "" {
			e.Kind = "reply"
			e.ParentURL = scalar(args[2])
		}
	}
	e.IdempotencyKey = fmt.Sprintf("browser:%s:%s:%s:%s:%s:%d:%s:%s", e.Platform, e.Account, e.Kind, source.ExecutionID, source.NodeID, sequence, e.RemoteID, e.URL)
	return &e
}

func base(s Source, platform, kind string) Entry {
	return Entry{Platform: platform, Kind: kind, WorkflowID: s.WorkflowID, ExecutionID: s.ExecutionID, NodeID: s.NodeID, AgentID: s.AgentID, OrgID: s.OrgID, RoleID: s.RoleID, Media: []string{}}
}
func scalar(v interface{}) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case int, int64, float64:
		return fmt.Sprint(x)
	}
	return ""
}
func field(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if s := scalar(m[k]); s != "" {
			return s
		}
	}
	return ""
}
func media(values ...interface{}) []string {
	r := []string{}
	for _, v := range values {
		switch x := v.(type) {
		case string:
			if x != "" {
				for _, s := range strings.Split(x, ",") {
					if s = strings.TrimSpace(s); s != "" {
						r = append(r, s)
					}
				}
			}
		case []string:
			r = append(r, x...)
		case []interface{}:
			for _, e := range x {
				if s := scalar(e); s != "" {
					r = append(r, s)
				}
			}
		}
	}
	return r
}
func rejected(r map[string]interface{}) bool {
	if r == nil {
		return true
	}
	for _, k := range []string{"error", "errors"} {
		if v := r[k]; v != nil && v != "" {
			if a, ok := v.([]interface{}); ok && len(a) == 0 {
				continue
			}
			return true
		}
	}
	for _, k := range []string{"success", "posted", "published"} {
		if r[k] == false {
			return true
		}
	}
	switch strings.ToLower(field(r, "status")) {
	case "failed", "error", "rejected", "draft", "scheduled":
		return true
	}
	return false
}

// Record never reports a tracking problem as a publishing failure. The caller
// logs the warning and keeps the successful remote action's original outcome.
func Record(ctx context.Context, store *Store, entries []Entry, warn func(string)) {
	trackingCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	for _, e := range entries {
		if _, err := store.Register(trackingCtx, e); err != nil && warn != nil {
			warn(fmt.Sprintf("publication tracking failed (platform=%s remote_id=%s url=%s): %v; the remote publication succeeded; do not retry publishing", e.Platform, e.RemoteID, e.URL, err))
		}
	}
}
