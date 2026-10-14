package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

// withRedditServer starts an httptest server, points redditBaseURL at it for
// the duration of the test, and restores the original value on cleanup.
func withRedditServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	orig := redditBaseURL
	redditBaseURL = srv.URL
	t.Cleanup(func() { redditBaseURL = orig })
	return srv
}

func redditExecute(t *testing.T, config map[string]interface{}) []workflow.NodeOutput {
	t.Helper()
	n := &RedditNode{}
	out, err := n.Execute(context.Background(), workflow.NodeInput{}, config)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	return out
}

func redditMainItems(out []workflow.NodeOutput) []workflow.Item {
	for _, o := range out {
		if o.Handle == "main" {
			return o.Items
		}
	}
	return nil
}

func TestRedditNode_Type(t *testing.T) {
	n := &RedditNode{}
	if got := n.Type(); got != "service.reddit" {
		t.Errorf("Type() = %q, want service.reddit", got)
	}
}

func TestRedditNode_RequiresAccessToken(t *testing.T) {
	n := &RedditNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation": "get_hot",
		"subreddit": "golang",
	})
	if err == nil {
		t.Fatal("expected error when access_token is missing")
	}
}

func TestRedditNode_UnknownOperation(t *testing.T) {
	n := &RedditNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation":    "bogus",
		"access_token": "test-token",
	})
	if err == nil {
		t.Fatal("expected error for unknown operation")
	}
}

func TestRedditNode_SubmitPostSelf(t *testing.T) {
	var gotAuth, gotUA, gotPath, gotContentType string
	var gotForm map[string][]string

	withRedditServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotUA = r.Header.Get("User-Agent")
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		gotForm = map[string][]string(r.PostForm)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"json": map[string]interface{}{"data": map[string]interface{}{"id": "abc123"}},
		})
	})

	out := redditExecute(t, map[string]interface{}{
		"operation":    "submit_post",
		"access_token": "test-token",
		"subreddit":    "golang",
		"title":        "Hello World",
		"text":         "This is the body",
	})

	if gotAuth != "Bearer test-token" {
		t.Errorf("Authorization = %q, want Bearer test-token", gotAuth)
	}
	if gotUA != "mono-agent/1.0" {
		t.Errorf("User-Agent = %q, want mono-agent/1.0 (default)", gotUA)
	}
	if gotPath != "/api/submit" {
		t.Errorf("path = %q, want /api/submit", gotPath)
	}
	if gotContentType != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type = %q, want application/x-www-form-urlencoded", gotContentType)
	}
	if gotForm["kind"][0] != "self" {
		t.Errorf("kind = %v, want self", gotForm["kind"])
	}
	if gotForm["sr"][0] != "golang" {
		t.Errorf("sr = %v, want golang", gotForm["sr"])
	}
	if gotForm["title"][0] != "Hello World" {
		t.Errorf("title = %v, want Hello World", gotForm["title"])
	}
	if gotForm["text"][0] != "This is the body" {
		t.Errorf("text = %v, want This is the body", gotForm["text"])
	}

	items := redditMainItems(out)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
}

func TestRedditNode_SubmitPostLink(t *testing.T) {
	var gotForm map[string][]string
	withRedditServer(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		gotForm = map[string][]string(r.PostForm)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"json": map[string]interface{}{}})
	})

	redditExecute(t, map[string]interface{}{
		"operation":    "submit_post",
		"access_token": "test-token",
		"subreddit":    "golang",
		"title":        "Check this out",
		"kind":         "link",
		"url":          "https://example.com",
	})

	if gotForm["kind"][0] != "link" {
		t.Errorf("kind = %v, want link", gotForm["kind"])
	}
	if gotForm["url"][0] != "https://example.com" {
		t.Errorf("url = %v, want https://example.com", gotForm["url"])
	}
	if _, ok := gotForm["text"]; ok {
		t.Errorf("text should not be set for link posts, got %v", gotForm["text"])
	}
}

func TestRedditNode_SubmitPostRequiresSubredditAndTitle(t *testing.T) {
	n := &RedditNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation":    "submit_post",
		"access_token": "test-token",
	})
	if err == nil {
		t.Fatal("expected error when subreddit/title are missing")
	}
}

// A self post may be title-only (comm.reddit always allowed that), so an
// empty body is sent rather than rejected; see TestLegacyRedditConfigs.

func TestRedditNode_SubmitPostLinkRequiresURL(t *testing.T) {
	n := &RedditNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation":    "submit_post",
		"access_token": "test-token",
		"subreddit":    "golang",
		"title":        "Hello",
		"kind":         "link",
	})
	if err == nil {
		t.Fatal("expected error when url is missing for a link post")
	}
}

func TestRedditNode_GetHot(t *testing.T) {
	var gotPath, gotQuery, gotUA string
	withRedditServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotUA = r.Header.Get("User-Agent")
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"children": []map[string]interface{}{
					{"data": map[string]interface{}{"id": "p1", "title": "Post 1"}},
					{"data": map[string]interface{}{"id": "p2", "title": "Post 2"}},
				},
			},
		})
	})

	out := redditExecute(t, map[string]interface{}{
		"operation":    "get_hot",
		"access_token": "test-token",
		"user_agent":   "my-app/2.0",
		"subreddit":    "golang",
		"limit":        float64(10),
	})

	if gotPath != "/r/golang/hot" {
		t.Errorf("path = %q, want /r/golang/hot", gotPath)
	}
	if gotQuery != "limit=10" {
		t.Errorf("query = %q, want limit=10", gotQuery)
	}
	if gotUA != "my-app/2.0" {
		t.Errorf("User-Agent = %q, want my-app/2.0", gotUA)
	}

	items := redditMainItems(out)
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
}

func TestRedditNode_GetHotDefaultLimit(t *testing.T) {
	var gotQuery string
	withRedditServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{"children": []interface{}{}},
		})
	})

	redditExecute(t, map[string]interface{}{
		"operation":    "get_hot",
		"access_token": "test-token",
		"subreddit":    "golang",
	})

	if gotQuery != "limit=25" {
		t.Errorf("query = %q, want limit=25 (default)", gotQuery)
	}
}

func TestRedditNode_GetHotRequiresSubreddit(t *testing.T) {
	n := &RedditNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation":    "get_hot",
		"access_token": "test-token",
	})
	if err == nil {
		t.Fatal("expected error when subreddit is missing")
	}
}

func TestRedditNode_Comment(t *testing.T) {
	var gotPath string
	var gotForm map[string][]string
	withRedditServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		gotForm = map[string][]string(r.PostForm)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"json": map[string]interface{}{}})
	})

	out := redditExecute(t, map[string]interface{}{
		"operation":    "comment",
		"access_token": "test-token",
		"thing_id":     "t3_abc123",
		"text":         "Nice post!",
	})

	if gotPath != "/api/comment" {
		t.Errorf("path = %q, want /api/comment", gotPath)
	}
	if gotForm["thing_id"][0] != "t3_abc123" {
		t.Errorf("thing_id = %v, want t3_abc123", gotForm["thing_id"])
	}
	if gotForm["text"][0] != "Nice post!" {
		t.Errorf("text = %v, want Nice post!", gotForm["text"])
	}
	items := redditMainItems(out)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
}

func TestRedditNode_CommentRequiresThingIDAndText(t *testing.T) {
	n := &RedditNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation":    "comment",
		"access_token": "test-token",
		"thing_id":     "t3_abc123",
	})
	if err == nil {
		t.Fatal("expected error when text is missing")
	}
}

func TestRedditNode_Upvote(t *testing.T) {
	var gotPath string
	var gotForm map[string][]string
	withRedditServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		gotForm = map[string][]string(r.PostForm)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{})
	})

	out := redditExecute(t, map[string]interface{}{
		"operation":    "upvote",
		"access_token": "test-token",
		"thing_id":     "t3_abc123",
	})

	if gotPath != "/api/vote" {
		t.Errorf("path = %q, want /api/vote", gotPath)
	}
	if gotForm["id"][0] != "t3_abc123" {
		t.Errorf("id = %v, want t3_abc123", gotForm["id"])
	}
	if gotForm["dir"][0] != "1" {
		t.Errorf("dir = %v, want 1", gotForm["dir"])
	}
	items := redditMainItems(out)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
}

func TestRedditNode_UpvoteRequiresThingID(t *testing.T) {
	n := &RedditNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation":    "upvote",
		"access_token": "test-token",
	})
	if err == nil {
		t.Fatal("expected error when thing_id is missing")
	}
}

func TestRedditNode_SubmitPostSetsAPIType(t *testing.T) {
	var gotForm map[string][]string
	withRedditServer(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		gotForm = map[string][]string(r.PostForm)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"json": map[string]interface{}{"data": map[string]interface{}{"id": "abc123"}},
		})
	})

	redditExecute(t, map[string]interface{}{
		"operation":    "submit_post",
		"access_token": "test-token",
		"subreddit":    "golang",
		"title":        "Hello World",
		"text":         "This is the body",
	})

	if gotForm["api_type"] == nil || gotForm["api_type"][0] != "json" {
		t.Errorf("api_type = %v, want [json] — Reddit only returns the structured errors envelope when this is set", gotForm["api_type"])
	}
}

// TestRedditNode_SubmitPostRateLimitedReturnsError verifies that a
// rate-limit/validation failure reported in the response body's "errors"
// array causes Execute to return an error, even though Reddit responds with
// HTTP 200 for this failure mode.
func TestRedditNode_SubmitPostRateLimitedReturnsError(t *testing.T) {
	withRedditServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"json": map[string]interface{}{
				"errors": [][]string{
					{"RATELIMITED", "you are doing that too much, try again in 9 minutes", "ratelimit"},
				},
				"data": map[string]interface{}{},
			},
		})
	})

	n := &RedditNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation":    "submit_post",
		"access_token": "test-token",
		"subreddit":    "golang",
		"title":        "Hello World",
		"text":         "This is the body",
	})
	if err == nil {
		t.Fatal("expected error for HTTP 200 response with a populated errors array")
	}
}

func TestRedditNode_CommentRateLimitedReturnsError(t *testing.T) {
	withRedditServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"json": map[string]interface{}{
				"errors": [][]string{
					{"RATELIMITED", "you are doing that too much, try again in 9 minutes", "ratelimit"},
				},
				"data": map[string]interface{}{},
			},
		})
	})

	n := &RedditNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation":    "comment",
		"access_token": "test-token",
		"thing_id":     "t3_abc123",
		"text":         "Nice post!",
	})
	if err == nil {
		t.Fatal("expected error for HTTP 200 response with a populated errors array")
	}
}

func TestRedditNode_UpvoteRateLimitedReturnsError(t *testing.T) {
	withRedditServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"json": map[string]interface{}{
				"errors": [][]string{
					{"RATELIMITED", "you are doing that too much, try again in 9 minutes", "ratelimit"},
				},
				"data": map[string]interface{}{},
			},
		})
	})

	n := &RedditNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation":    "upvote",
		"access_token": "test-token",
		"thing_id":     "t3_abc123",
	})
	if err == nil {
		t.Fatal("expected error for HTTP 200 response with a populated errors array")
	}
}

func TestRedditNode_ErrorResponsePropagates(t *testing.T) {
	withRedditServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_token"}`))
	})

	n := &RedditNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation":    "get_hot",
		"access_token": "bad-token",
		"subreddit":    "golang",
	})
	if err == nil {
		t.Fatal("expected error on 401 response")
	}
}

// kind "auto" (the form's default) picks link when a URL is set.
func TestRedditNode_SubmitPostAutoKind(t *testing.T) {
	var gotForm map[string][]string
	withRedditServer(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		gotForm = map[string][]string(r.PostForm)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"json": map[string]interface{}{}})
	})
	redditExecute(t, map[string]interface{}{
		"operation": "submit_post", "access_token": "test-token", "subreddit": "golang",
		"title": "Look", "kind": "auto", "url": "https://example.com",
	})
	if gotForm["kind"][0] != "link" {
		t.Errorf("kind = %v, want link", gotForm["kind"])
	}
}
