package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/monoes/mono-agent/internal/apipkg"
	"github.com/monoes/mono-agent/internal/workflow"
)

// RedditNode submits posts/comments, reads hot listings, and casts votes via
// the Reddit OAuth2 REST API.
// Type: "service.reddit"
//
// Config fields:
//
//	"operation"     (string, required): "submit_post" | "get_hot" | "comment" | "upvote" |
//	                "list_comments" | "get_post_metrics"
//	"access_token"  (string, required): OAuth2 bearer token
//	"user_agent"    (string, optional, default "mono-agent/1.0"): Reddit requires a descriptive User-Agent
//	"subreddit"     (string, required for submit_post and get_hot)
//	"title"         (string, required for submit_post)
//	"kind"          (string, optional): "auto" | "self" | "link" for submit_post; "auto" (or empty) is
//	                "link" when "url" is set, else "self"
//	"text"          (string, optional self-post body for submit_post; required for comment)
//	"url"           (string, required for submit_post when kind="link")
//	"limit"         (int, optional, default 25): get_hot listing size
//	"thing_id"      (string, required for comment (parent fullname), upvote (target fullname),
//	                list_comments and get_post_metrics (post fullname "t3_..." or bare ID))
//
// It is also the runtime for the legacy "comm.reddit" type (registered as an
// alias): its "reply_to_comment" operation runs as comment, with a bare ID
// taken as a comment ("t1_") fullname as it always was there.
type RedditNode struct{}

func (n *RedditNode) Type() string { return "service.reddit" }

// redditBaseURL is a var (not const) so tests can point it at an httptest server.
var redditBaseURL = "https://oauth.reddit.com"

func (n *RedditNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	if extensionOwns("service.reddit", config) {
		return apipkg.ExecuteExtension(ctx, "service.reddit", input, config)
	}
	accessToken := strVal(config, "access_token")
	if accessToken == "" {
		return nil, fmt.Errorf("service.reddit: 'access_token' is required")
	}
	userAgent := strVal(config, "user_agent")
	if userAgent == "" {
		userAgent = "mono-agent/1.0"
	}
	operation := strVal(config, "operation")

	switch operation {
	case "submit_post":
		return n.submitPost(ctx, accessToken, userAgent, config)
	case "get_hot":
		return n.getHot(ctx, accessToken, userAgent, config)
	case "comment":
		return n.comment(ctx, accessToken, userAgent, config)
	case "upvote":
		return n.upvote(ctx, accessToken, userAgent, config)
	case "reply_to_comment": // legacy comm.reddit name
		if id := strVal(config, "thing_id"); id != "" {
			config = withDefault(config, "thing_id", redditEnsureFullname(id, "t1"))
		}
		return n.comment(ctx, accessToken, userAgent, config)
	case "list_comments":
		return n.listComments(ctx, accessToken, userAgent, config)
	case "get_post_metrics":
		return n.getPostMetrics(ctx, accessToken, userAgent, config)
	default:
		return nil, fmt.Errorf("service.reddit: unknown operation %q", operation)
	}
}

func (n *RedditNode) submitPost(ctx context.Context, accessToken, userAgent string, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	subreddit := strVal(config, "subreddit")
	title := strVal(config, "title")
	if subreddit == "" || title == "" {
		return nil, fmt.Errorf("service.reddit: 'subreddit' and 'title' are required for submit_post")
	}
	kind := strVal(config, "kind")
	if kind == "" || kind == "auto" {
		kind = "self"
		if strVal(config, "url") != "" {
			kind = "link"
		}
	}

	form := url.Values{}
	form.Set("kind", kind)
	form.Set("sr", subreddit)
	form.Set("title", title)

	if kind == "link" {
		linkURL := strVal(config, "url")
		if linkURL == "" {
			return nil, fmt.Errorf("service.reddit: 'url' is required for submit_post with kind=link")
		}
		form.Set("url", linkURL)
	} else {
		// A self post may be title-only, so an empty body is sent as-is.
		form.Set("text", strVal(config, "text"))
	}

	result, err := redditPostForm(ctx, redditBaseURL+"/api/submit", accessToken, userAgent, form)
	if err != nil {
		return nil, fmt.Errorf("service.reddit submit_post: %w", err)
	}
	return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{workflow.NewItem(result)}}}, nil
}

func (n *RedditNode) getHot(ctx context.Context, accessToken, userAgent string, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	subreddit := strVal(config, "subreddit")
	if subreddit == "" {
		return nil, fmt.Errorf("service.reddit: 'subreddit' is required for get_hot")
	}
	limit := intVal(config, "limit")
	if limit <= 0 {
		limit = 25
	}
	endpoint := fmt.Sprintf("%s/r/%s/hot?limit=%d", redditBaseURL, url.PathEscape(subreddit), limit)
	result, err := redditRequest(ctx, "GET", endpoint, accessToken, userAgent, nil)
	if err != nil {
		return nil, fmt.Errorf("service.reddit get_hot: %w", err)
	}

	items := []workflow.Item{}
	if data, ok := result["data"].(map[string]interface{}); ok {
		if children, ok := data["children"].([]interface{}); ok {
			for _, c := range children {
				if m, ok := c.(map[string]interface{}); ok {
					items = append(items, workflow.NewItem(m))
				}
			}
		}
	}
	return []workflow.NodeOutput{{Handle: "main", Items: items}}, nil
}

func (n *RedditNode) comment(ctx context.Context, accessToken, userAgent string, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	thingID := strVal(config, "thing_id")
	text := strVal(config, "text")
	if thingID == "" || text == "" {
		return nil, fmt.Errorf("service.reddit: 'thing_id' and 'text' are required for comment")
	}

	form := url.Values{}
	form.Set("thing_id", thingID)
	form.Set("text", text)

	result, err := redditPostForm(ctx, redditBaseURL+"/api/comment", accessToken, userAgent, form)
	if err != nil {
		return nil, fmt.Errorf("service.reddit comment: %w", err)
	}
	return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{workflow.NewItem(result)}}}, nil
}

func (n *RedditNode) upvote(ctx context.Context, accessToken, userAgent string, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	thingID := strVal(config, "thing_id")
	if thingID == "" {
		return nil, fmt.Errorf("service.reddit: 'thing_id' is required for upvote")
	}

	form := url.Values{}
	form.Set("id", thingID)
	form.Set("dir", "1")

	result, err := redditPostForm(ctx, redditBaseURL+"/api/vote", accessToken, userAgent, form)
	if err != nil {
		return nil, fmt.Errorf("service.reddit upvote: %w", err)
	}
	return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{workflow.NewItem(result)}}}, nil
}

// redditRequest wraps buildRequest to also set the User-Agent header Reddit
// requires (it blocks default/generic user agents), executes the request,
// and returns a parsed JSON object response.
func redditRequest(ctx context.Context, method, endpoint, accessToken, userAgent string, body interface{}) (map[string]interface{}, error) {
	req, err := buildRequest(ctx, method, endpoint, accessToken, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)

	respBytes, err := redditDo(req, method, endpoint)
	if err != nil {
		return nil, err
	}
	if len(respBytes) == 0 {
		return map[string]interface{}{}, nil
	}
	var result map[string]interface{}
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return nil, fmt.Errorf("parsing JSON response: %w (body: %s)", err, string(respBytes))
	}
	return result, nil
}

// redditPostForm builds an application/x-www-form-urlencoded POST request
// (Reddit's write endpoints do not accept JSON bodies), sets the
// Authorization and User-Agent headers, executes it, and returns the
// response parsed via redditParseAPIResponse.
//
// api_type=json is forced on every call: Reddit's write endpoints only
// return the structured {"json": {"errors": [...], "data": {...}}} envelope
// (and thus only report logical failures like rate limits or validation
// errors) when that field is present in the form. Without it, Reddit
// returns HTTP 200 with an undocumented legacy body shape that has no
// reliable error signal.
func redditPostForm(ctx context.Context, endpoint, accessToken, userAgent string, form url.Values) (map[string]interface{}, error) {
	form.Set("api_type", "json")
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	if accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}

	respBytes, err := redditDo(req, "POST", endpoint)
	if err != nil {
		return nil, err
	}
	return redditParseAPIResponse(respBytes)
}

// redditParseAPIResponse parses Reddit's api_type=json envelope, returning
// the flattened "data" object, or an error built from the "errors" array if
// Reddit reports any. Reddit returns HTTP 200 even on logical errors like
// rate limits or validation failures, so the errors array must be checked
// explicitly instead of relying on the HTTP status code alone.
func redditParseAPIResponse(respBytes []byte) (map[string]interface{}, error) {
	if len(respBytes) == 0 {
		return map[string]interface{}{}, nil
	}
	var envelope struct {
		JSON struct {
			Errors [][]string             `json:"errors"`
			Data   map[string]interface{} `json:"data"`
		} `json:"json"`
	}
	if err := json.Unmarshal(respBytes, &envelope); err != nil {
		return nil, fmt.Errorf("parsing JSON response: %w (body: %s)", err, string(respBytes))
	}
	if len(envelope.JSON.Errors) > 0 {
		parts := make([]string, 0, len(envelope.JSON.Errors))
		for _, e := range envelope.JSON.Errors {
			parts = append(parts, strings.Join(e, ": "))
		}
		return nil, fmt.Errorf("reddit API error: %s", strings.Join(parts, "; "))
	}
	return envelope.JSON.Data, nil
}

// redditDo executes req via httpClient and returns the raw response body for
// a successful (2xx) response.
func redditDo(req *http.Request, method, endpoint string) ([]byte, error) {
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP %s %s: %w", method, endpoint, err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBytes))
	}
	return respBytes, nil
}
