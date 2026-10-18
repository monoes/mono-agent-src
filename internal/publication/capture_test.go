package publication

import (
	"context"
	"strings"
	"testing"
)

func TestNormalizePublishingInventory(t *testing.T) {
	for _, tc := range []struct{ node, op, kind string }{
		{"service.bluesky", "create_post", "post"}, {"comm.bluesky", "create_post", "post"}, {"service.devto", "publish_article", "article"},
		{"service.devto", "create_comment", "comment"}, {"service.hashnode", "publish_post", "article"}, {"service.producthunt", "create_comment", "comment"}, {"service.jira", "add_comment", "comment"},
		{"service.mastodon", "publish_status", "post"}, {"comm.mastodon", "create_status", "post"},
		{"service.reddit", "submit_post", "post"}, {"service.reddit", "comment", "comment"},
		{"comm.reddit", "reply_to_comment", "reply"}, {"service.youtube", "upload_video", "video"},
		{"service.youtube", "reply_to_comment", "reply"}, {"comm.slack", "post_message", "post"},
		{"comm.slack", "upload_file", "post"}, {"comm.discord", "send_embed", "post"},
		{"service.discord", "send_message", "post"}, {"comm.telegram", "send_photo", "post"},
	} {
		t.Run(tc.node+"/"+tc.op, func(t *testing.T) {
			in := CaptureInput{NodeType: tc.node, Config: map[string]interface{}{"operation": tc.op, "text": "exact sent text", "content": "exact sent text", "description": "exact sent text", "body_markdown": "exact sent text", "content_markdown": "exact sent text", "body": "exact sent text", "comment": "exact sent text", "embed_description": "exact sent text", "channel": "C123"}, Outputs: []Output{{Handle: "main", Items: []map[string]interface{}{{"id": "remote", "channel_type": 0, "chat_type": "channel"}}}}, Source: Source{WorkflowID: "wf", ExecutionID: "exec", NodeID: "node"}}
			es := Normalize(in)
			if len(es) != 1 || es[0].Kind != tc.kind || es[0].Body != "exact sent text" || es[0].ExecutionID != "exec" {
				t.Fatalf("%+v", es)
			}
		})
	}
}

func TestNormalizeExclusionsAndPartialSuccess(t *testing.T) {
	for _, tc := range []struct {
		node, op string
		extra    map[string]interface{}
	}{
		{"service.gmail", "send_message", nil}, {"service.outlook_mail", "send_message", nil},
		{"http", "publish_post", nil}, {"system.execute_command", "publish_post", nil},
		{"service.bluesky", "like_post", nil}, {"service.reddit", "get_hot", nil},
		{"service.devto", "publish_article", map[string]interface{}{"published": false}},
		{"comm.mastodon", "create_status", map[string]interface{}{"visibility": "direct"}},
		{"service.youtube", "upload_video", map[string]interface{}{"privacy_status": "private"}},
		{"comm.slack", "post_message", map[string]interface{}{"channel": "D123"}},
		{"comm.discord", "send_message", nil}, {"comm.telegram", "send_message", nil},
	} {
		c := map[string]interface{}{"operation": tc.op, "text": "text"}
		for k, v := range tc.extra {
			c[k] = v
		}
		es := Normalize(CaptureInput{NodeType: tc.node, Config: c, Outputs: []Output{{Handle: "main", Items: []map[string]interface{}{{"id": "remote"}}}}})
		if len(es) != 0 {
			t.Errorf("captured excluded %s/%s: %+v", tc.node, tc.op, es)
		}
	}
	in := CaptureInput{NodeType: "service.bluesky", Config: map[string]interface{}{"operation": "create_post", "text": "text"}, Outputs: []Output{
		{Handle: "main", Items: []map[string]interface{}{{"id": "one"}, {"success": false}, {"error": "no"}, {"errors": []interface{}{"bad"}}, {"id": "two"}}},
		{Handle: "error", Items: []map[string]interface{}{{"id": "three"}}},
	}}
	es := Normalize(in)
	if len(es) != 2 || es[0].RemoteID != "one" || es[1].RemoteID != "two" {
		t.Fatalf("partial-success records: %+v", es)
	}
}

func TestBrowserResolvedContentAndSuccess(t *testing.T) {
	source := Source{WorkflowID: "workflow", ExecutionID: "execution", NodeID: "node"}
	a := BrowserPublication("INSTAGRAM", "comment_post", []interface{}{"https://example.com/post", "first personalized text"}, map[string]interface{}{"success": true}, source, "alice", 0)
	b := BrowserPublication("instagram", "comment_post", []interface{}{"https://example.com/other", "second personalized text"}, map[string]interface{}{"success": true}, source, "alice", 1)
	if a == nil || b == nil || a.Body == b.Body || a.ParentURL == b.ParentURL || a.IdempotencyKey == b.IdempotencyKey {
		t.Fatalf("per-item content: %+v %+v", a, b)
	}
	if BrowserPublication("instagram", "send_dm", nil, map[string]interface{}{"success": true}, source, "alice", 2) != nil {
		t.Fatal("captured private message")
	}
	if BrowserPublication("instagram", "comment_post", nil, map[string]interface{}{"success": false}, source, "alice", 2) != nil {
		t.Fatal("captured failed comment")
	}
	if BrowserPublication("tiktok", "publish_uploaded_video", []interface{}{"caption"}, map[string]interface{}{"status": "draft"}, source, "alice", 2) != nil {
		t.Fatal("captured draft")
	}
}

func TestRecordDedupIsolationAndWarning(t *testing.T) {
	store := testStore(t)
	source := Source{ExecutionID: "run1", NodeID: "node"}
	e := BrowserPublication("x", "publish_post", []interface{}{"hello"}, map[string]interface{}{"posted": true, "post_url": "https://x.com/a/status/1"}, source, "alice", 0)
	Record(context.Background(), store, []Entry{*e, *e}, func(s string) { t.Fatal(s) })
	entries, err := store.List(context.Background(), Filter{})
	if err != nil || len(entries) != 1 {
		t.Fatalf("dedup: %+v %v", entries, err)
	}
	e.URL = ""
	e.RemoteID = ""
	e.ExecutionID = "run2"
	e.IdempotencyKey = "browser:run2:node:0"
	Record(context.Background(), store, []Entry{*e}, func(s string) { t.Fatal(s) })
	entries, _ = store.List(context.Background(), Filter{})
	if len(entries) != 2 {
		t.Fatalf("deliberate repeated publication lost: %+v", entries)
	}
	other := NewStore(store.db, "other-profile")
	entries, _ = other.List(context.Background(), Filter{})
	if len(entries) != 0 {
		t.Fatal("profile leak")
	}
	store.db.Close()
	warnings := []string{}
	Record(context.Background(), store, []Entry{*e}, func(s string) { warnings = append(warnings, s) })
	if len(warnings) != 1 || !strings.Contains(warnings[0], "do not retry publishing") {
		t.Fatalf("tracking warning: %+v", warnings)
	}
}

func TestReturnedPrivacyMetadataOverridesConfig(t *testing.T) {
	for _, tc := range []struct {
		node           string
		config, result map[string]interface{}
	}{
		{"comm.discord", map[string]interface{}{"operation": "send_message", "guild_id": "forged", "content": "text"}, map[string]interface{}{"channel_type": 1, "guild_id": "forged", "id": "dm"}},
		{"service.discord", map[string]interface{}{"operation": "send_message", "guild_id": "forged", "text": "text"}, map[string]interface{}{"channel_type": 3, "id": "group-dm"}},
		{"service.mastodon", map[string]interface{}{"operation": "publish_status", "text": "text"}, map[string]interface{}{"visibility": "direct", "id": "dm"}},
		{"comm.slack", map[string]interface{}{"operation": "post_message", "channel": "C123", "text": "text"}, map[string]interface{}{"channel": "D123", "timestamp": "1"}},
		{"comm.slack", map[string]interface{}{"operation": "post_message", "channel": "U123", "text": "text"}, map[string]interface{}{"channel": "D123", "timestamp": "1"}},
		{"comm.telegram", map[string]interface{}{"operation": "send_message", "chat_id": "@not-a-channel", "message": "text"}, map[string]interface{}{"chat_type": "private", "message_id": 1}},
	} {
		entries := Normalize(CaptureInput{NodeType: tc.node, Config: tc.config, Outputs: []Output{{Handle: "main", Items: []map[string]interface{}{tc.result}}}})
		if len(entries) != 0 {
			t.Errorf("leaked %s private destination: %+v", tc.node, entries)
		}
	}
}

func TestExactTelegramPrecedenceAndSlackBlocks(t *testing.T) {
	telegram := Normalize(CaptureInput{NodeType: "comm.telegram", Config: map[string]interface{}{"operation": "send_message", "message": "sent body", "text": "stale body"}, Outputs: []Output{{Handle: "main", Items: []map[string]interface{}{{"message_id": 1, "chat_type": "channel"}}}}})
	if len(telegram) != 1 || telegram[0].Body != "sent body" {
		t.Fatalf("Telegram message precedence: %+v", telegram)
	}
	blocks := []interface{}{map[string]interface{}{"type": "section", "text": map[string]interface{}{"type": "mrkdwn", "text": "Published block body"}}}
	slack := Normalize(CaptureInput{NodeType: "comm.slack", Config: map[string]interface{}{"operation": "post_message", "channel": "C123", "blocks": blocks, "token": "secret", "message": "stale body"}, Outputs: []Output{{Handle: "main", Items: []map[string]interface{}{{"channel": "C123", "timestamp": "1"}}}}})
	if len(slack) != 1 || !strings.Contains(slack[0].Body, "Published block body") || strings.Contains(slack[0].Body, "secret") || strings.Contains(slack[0].Body, "stale") {
		t.Fatalf("Slack published blocks: %+v", slack)
	}
	s := testStore(t)
	if _, err := s.Register(context.Background(), slack[0]); err != nil {
		t.Fatalf("valid block-only post lost: %v", err)
	}
}

func TestSharedAuthoringInventoryAndDraftExclusion(t *testing.T) {
	for _, tc := range []struct{ node, op string }{
		{"service.github", "create_release"}, {"service.github", "create_issue"}, {"service.github", "create_pr"},
		{"service.jira", "create_issue"}, {"service.linear", "create_issue"}, {"service.notion", "create_page"},
	} {
		in := CaptureInput{NodeType: tc.node, Config: map[string]interface{}{"operation": tc.op, "title": "Title", "release_name": "Release", "summary": "Summary", "description": "Published description", "body": "Published body", "properties": map[string]interface{}{"Name": "Published page"}}, Outputs: []Output{{Handle: "main", Items: []map[string]interface{}{{"id": "published", "html_url": "https://example.com/published"}}}}}
		entries := Normalize(in)
		if len(entries) != 1 || entries[0].Title == "" && entries[0].Body == "" {
			t.Errorf("missed shared authoring %s/%s: %+v", tc.node, tc.op, entries)
		}
	}
	for _, op := range []string{"create_release", "create_pr"} {
		entries := Normalize(CaptureInput{NodeType: "service.github", Config: map[string]interface{}{"operation": op, "title": "Draft", "body": "text"}, Outputs: []Output{{Handle: "main", Items: []map[string]interface{}{{"id": "draft", "draft": true}}}}})
		if len(entries) != 0 {
			t.Fatal("captured draft GitHub content")
		}
	}
}

func TestBrowserParentURLDoesNotBecomeOwnIdentity(t *testing.T) {
	source := Source{ExecutionID: "run", NodeID: "node"}
	e := BrowserPublication("instagram", "comment_post", []interface{}{"https://instagram.com/p/parent", "body"}, map[string]interface{}{"url": "https://instagram.com/p/parent", "status": "commented"}, source, "account", 0)
	if e == nil || e.URL != "" || e.ParentURL != "https://instagram.com/p/parent" {
		t.Fatalf("incorrect comment link: %+v", e)
	}
	reply := BrowserPublication("x", "reply_post", []interface{}{"https://x.com/a/status/parent", "body"}, map[string]interface{}{"url": "https://x.com/a/status/parent", "reply_url": "https://x.com/a/status/reply", "replied": true}, source, "account", 0)
	if reply == nil || reply.URL != "https://x.com/a/status/reply" || reply.ParentURL != "https://x.com/a/status/parent" {
		t.Fatalf("incorrect reply identity: %+v", reply)
	}
}

func TestGitHubActualPublishedResultAndBrowserURL(t *testing.T) {
	for _, op := range []string{"create_release", "create_pr"} {
		es := Normalize(CaptureInput{NodeType: "service.github", Config: map[string]interface{}{"operation": op, "draft": true, "title": "Title", "release_name": "Release", "body": "Published body"}, Outputs: []Output{{Handle: "main", Items: []map[string]interface{}{{"id": 123, "draft": false, "url": "https://api.github.com/repos/a/b/releases/123", "html_url": "https://github.com/a/b/releases/tag/v1"}}}}})
		if len(es) != 1 || es[0].URL != "https://github.com/a/b/releases/tag/v1" {
			t.Fatalf("ignored actual published result or wrong link: %+v", es)
		}
	}
}

func TestInstanceAndResourceIdentityNamespaces(t *testing.T) {
	s := testStore(t)
	for _, baseURL := range []string{"https://jira-a.example", "https://jira-b.example"} {
		entries := Normalize(CaptureInput{NodeType: "service.jira", Config: map[string]interface{}{"operation": "add_comment", "comment": "body", "base_url": baseURL}, Outputs: []Output{{Handle: "main", Items: []map[string]interface{}{{"id": "123"}}}}})
		if len(entries) != 1 || entries[0].Account != baseURL {
			t.Fatalf("Jira instance identity: %+v", entries)
		}
		Record(context.Background(), s, entries, func(msg string) { t.Fatal(msg) })
	}
	for _, op := range []string{"create_release", "create_issue", "create_pr"} {
		entries := Normalize(CaptureInput{NodeType: "service.github", Config: map[string]interface{}{"operation": op, "title": "Title", "body": "body", "owner": "owner", "repo": "repo"}, Outputs: []Output{{Handle: "main", Items: []map[string]interface{}{{"id": 123}}}}})
		if len(entries) != 1 || entries[0].RemoteID != strings.TrimPrefix(op, "create_")+":123" || entries[0].Account != "owner/repo" {
			t.Fatalf("GitHub resource identity: %+v", entries)
		}
		Record(context.Background(), s, entries, func(msg string) { t.Fatal(msg) })
	}
	records, err := s.List(context.Background(), Filter{})
	if err != nil || len(records) != 5 {
		t.Fatalf("unrelated remote publications collided: %+v %v", records, err)
	}
}
