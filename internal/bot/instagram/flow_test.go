//go:build !nosocial

package instagram

// The shipped action JSONs (data/actions/instagram) run through the real
// action executor against the fake instagram.com: what a workflow node or
// `monoagentcli run` does, minus the extension.

import (
	"context"
	"github.com/monoes/mono-agent/internal/testhome"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/bot/bottest"
)

type flow struct {
	actionType string
	message    string
	keywords   string
	items      []interface{}
	params     map[string]interface{}
}

// run executes the action and returns its result and error.
func (f flow) run(t *testing.T, page *bottest.Page) (*action.ExecutionResult, error) {
	t.Helper()
	testhome.SetHome(t, t.TempDir()) // the loader's user-template fallback never sees a real home
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	ae := action.NewActionExecutor(ctx, page, nil, nil, nil, New(), zerolog.Nop())
	if f.items != nil {
		ae.SetVariable("selectedListItems", f.items)
	}
	params := map[string]interface{}{}
	// Every delay the JSONs know, shortened (0 would mean the 10s default).
	for _, k := range []string{"delayBetweenLikes", "delayBetweenComments", "delayBetweenReplies", "delayBetweenFollows",
		"delayBetweenUnfollows", "delayBetweenProfiles", "delayBetweenMessages", "delayBeforeReply", "delayBetweenPosts",
		"delayBetweenRequests", "sessionDelay", "delayBetweenStories"} {
		params[k] = 0.05
	}
	for k, v := range f.params {
		params[k] = v
	}
	res, err := ae.Execute(&action.StorageAction{
		ID: "test-" + f.actionType, Type: f.actionType, TargetPlatform: "instagram",
		ContentMessage: f.message, Keywords: f.keywords, Params: params,
	})
	if err == nil {
		assertOutputsDeclared(t, f.actionType, res)
	}
	return res, err
}

func items(urls ...string) []interface{} {
	out := make([]interface{}, len(urls))
	for i, u := range urls {
		out[i] = map[string]interface{}{"url": u}
	}
	return out
}

func extracted(res *action.ExecutionResult, key string) []interface{} {
	var out []interface{}
	if res == nil {
		return nil
	}
	for _, it := range res.ExtractedItems {
		if v, ok := it[key]; ok {
			out = append(out, v)
		}
	}
	return out
}

func TestFlowLikePosts(t *testing.T) {
	s := newSite()
	page, rec := s.open(t)
	res, err := flow{actionType: "like_posts", items: items(postURL, "https://www.instagram.com/p/FXPOST02/")}.run(t, page)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(posts(rec, "*/likes/FXMEDIA1/like/")); n != 2 {
		t.Errorf("likes = %v", apiPosts(rec))
	}
	if st := extracted(res, "status"); len(st) != 2 || st[0] != "liked" {
		t.Errorf("statuses = %v", st)
	}
}

func TestFlowLikePostsFailureIsReportedAndNotRetried(t *testing.T) {
	s := newSite()
	s.post["noconfirm"] = true
	page, rec := s.open(t)
	_, err := flow{actionType: "like_posts", items: items(postURL)}.run(t, page)
	if err == nil {
		t.Fatal("an unconfirmed like must fail the action")
	}
	if n := len(posts(rec, "*/likes/*")); n != 1 {
		t.Errorf("one click only, no fallback tier: %v", apiPosts(rec))
	}
}

func TestFlowCommentOnPosts(t *testing.T) {
	s := newSite()
	page, rec := s.open(t)
	if _, err := (flow{actionType: "comment_on_posts", message: "flow comment", items: items(postURL)}).run(t, page); err != nil {
		t.Fatal(err)
	}
	got := posts(rec, "*/comments/FXMEDIA1/add/")
	if len(got) != 1 || form(t, got[0]).Get("comment_text") != "flow comment" {
		t.Errorf("comments = %v", apiPosts(rec))
	}
}

func TestFlowLikeCommentsOnPosts(t *testing.T) {
	s := newSite()
	page, rec := s.open(t)
	if _, err := (flow{actionType: "like_comments_on_posts", items: items(postURL), params: map[string]interface{}{"commentAuthor": "fake.c1"}}).run(t, page); err != nil {
		t.Fatal(err)
	}
	if n := len(rec.Matching("POST", "*/comments/*like/*")); n != 1 || len(posts(rec, "*/comments/like/101/")) != 1 {
		t.Errorf("comment likes = %v", apiPosts(rec))
	}
	// A missing author fails the action and touches nothing.
	rec.Reset()
	if _, err := (flow{actionType: "like_comments_on_posts", items: items(postURL), params: map[string]interface{}{"commentAuthor": "fake.nobody"}}).run(t, page); err == nil {
		t.Fatal("missing author must fail")
	}
	if p := apiPosts(rec); len(p) != 0 {
		t.Errorf("clicked: %v", p)
	}
}

func TestFlowReplyToComments(t *testing.T) {
	s := newSite()
	page, rec := s.open(t)
	if _, err := (flow{actionType: "reply_to_comments", message: "flow reply", items: items(postURL), params: map[string]interface{}{"commentAuthor": "fake.c2"}}).run(t, page); err != nil {
		t.Fatal(err)
	}
	got := posts(rec, "*/comments/FXMEDIA1/add/")
	if len(got) != 1 || form(t, got[0]).Get("replied_to_comment_id") != "102" {
		t.Errorf("replies = %v", apiPosts(rec))
	}
}

func TestFlowFollowAndUnfollowUsers(t *testing.T) {
	s := newSite()
	s.profiles["fake.following"] = fx{"state": "following"}
	page, rec := s.open(t)
	if _, err := (flow{actionType: "follow_users", items: items("https://www.instagram.com/fake.ada/", "https://www.instagram.com/fake.following/")}).run(t, page); err != nil {
		t.Fatal(err)
	}
	if n := len(posts(rec, "*/friendships/create/*")); n != 1 {
		t.Errorf("follows = %v", apiPosts(rec))
	}
	rec.Reset()
	if _, err := (flow{actionType: "unfollow_users", items: []interface{}{"https://www.instagram.com/fake.following/"}}).run(t, page); err != nil {
		t.Fatal(err)
	}
	if n := len(posts(rec, "*/friendships/destroy/9001/")); n != 1 {
		t.Errorf("unfollows = %v", apiPosts(rec))
	}
}

func TestFlowWatchStories(t *testing.T) {
	s := newSite()
	s.profiles["fake.story"] = fx{"state": "following", "story": true}
	page, rec := s.open(t)
	res, err := flow{actionType: "watch_stories", items: items("https://www.instagram.com/fake.story/"), params: map[string]interface{}{"maxStoriesPerUser": 2}}.run(t, page)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(posts(rec, "*seen/?owner=fake.story*")); n != 2 {
		t.Errorf("seen = %v", apiPosts(rec))
	}
	if v := extracted(res, "stories_viewed"); len(v) != 1 || v[0] != float64(2) && v[0] != 2 {
		t.Errorf("stories_viewed = %v", v)
	}
}

func TestFlowSendDms(t *testing.T) {
	s := newSite()
	s.profiles["fake.bea"] = fx{"state": "following", "message": true}
	page, rec := s.open(t)
	if _, err := (flow{actionType: "send_dms", message: "flow dm", items: []interface{}{map[string]interface{}{"username": "fake.bea"}}}).run(t, page); err != nil {
		t.Fatal(err)
	}
	got := posts(rec, "*/broadcast/text/")
	if len(got) != 1 || form(t, got[0]).Get("text") != "flow dm" {
		t.Errorf("sends = %v", apiPosts(rec))
	}
}

func TestFlowAutoReplyDms(t *testing.T) {
	s := newSite()
	page, rec := s.open(t)
	if _, err := (flow{actionType: "auto_reply_dms", message: "auto", items: []interface{}{"https://www.instagram.com/direct/t/5002/"},
		params: map[string]interface{}{"startDate": "2026-01-01", "endDate": "2026-12-31", "pollInterval": 30}}).run(t, page); err != nil {
		t.Fatal(err)
	}
	if n := len(posts(rec, "*/broadcast/text/")); n != 1 {
		t.Errorf("sends = %v", apiPosts(rec))
	}
	_, err := flow{actionType: "auto_reply_dms", message: "auto",
		params: map[string]interface{}{"startDate": "2026-01-01", "endDate": "2026-12-31", "pollInterval": 30}}.run(t, page)
	if err == nil || !strings.Contains(err.Error(), "selectedListItems") {
		t.Fatalf("no items: err = %v", err)
	}
}

func TestFlowScrapeProfileInfo(t *testing.T) {
	s := newSite()
	page, _ := s.open(t)
	res, err := flow{actionType: "scrape_profile_info", items: items("https://www.instagram.com/fake.ada/")}.run(t, page)
	if err != nil {
		t.Fatal(err)
	}
	if v := extracted(res, "follower_count"); len(v) != 1 || v[0] != "1234" {
		t.Errorf("follower_count = %v", v)
	}
}

func TestFlowExportFollowers(t *testing.T) {
	s := newSite()
	page, _ := s.open(t)
	res, err := flow{actionType: "export_followers", params: map[string]interface{}{"target_url": "https://www.instagram.com/fake.ada/", "sourceType": "followers", "maxResultsCount": 12}}.run(t, page)
	if err != nil {
		t.Fatal(err)
	}
	if v := extracted(res, "username"); len(v) != 12 || v[0] != "fake.f01" {
		t.Errorf("usernames = %v", v)
	}
}

func TestFlowListUserPostsAndComments(t *testing.T) {
	s := newSite()
	page, _ := s.open(t)
	res, err := flow{actionType: "list_user_posts", params: map[string]interface{}{"username": "fake.me", "targetUsername": "fake.ada", "maxCount": 5}}.run(t, page)
	if err != nil {
		t.Fatal(err)
	}
	if v := extracted(res, "shortcode"); len(v) != 5 || v[0] != "FXPOST01" {
		t.Errorf("shortcodes = %v", v)
	}
	res, err = flow{actionType: "list_post_comments", items: items(postURL)}.run(t, page)
	if err != nil {
		t.Fatal(err)
	}
	if v := extracted(res, "author"); len(v) != 5 {
		t.Errorf("comment authors = %v", v)
	}
}

func TestFlowExtractPostData(t *testing.T) {
	s := newSite()
	page, _ := s.open(t)
	res, err := flow{actionType: "extract_post_data", items: items(postURL)}.run(t, page)
	if err != nil {
		t.Fatal(err)
	}
	if v := extracted(res, "author_username"); len(v) != 1 || v[0] != "fake.author" {
		t.Errorf("authors = %v", v)
	}
}

func TestFlowFindByKeyword(t *testing.T) {
	s := newSite()
	s.profileAPI = true
	page, _ := s.open(t)
	res, err := flow{actionType: "find_by_keyword", keywords: "fixture", params: map[string]interface{}{"maxResultsCount": 2}}.run(t, page)
	if err != nil {
		t.Fatal(err)
	}
	v := extracted(res, "source_post")
	users := extracted(res, "username")
	if len(v) != 2 || len(users) != 2 || users[0] != "fake.author" || users[1] != "fake.tagger3" {
		t.Errorf("profiles = %v (from %v)", users, v)
	}
}

func TestFlowEngageWithPosts(t *testing.T) {
	s := newSite()
	page, rec := s.open(t)
	_, err := flow{actionType: "engage_with_posts", params: map[string]interface{}{
		"searches": []interface{}{map[string]interface{}{"keyword": "fixture"}}, "maxContentCount": 2,
		"commentText": "engaged", "engagementActions": []interface{}{"like", "comment"},
	}}.run(t, page)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(posts(rec, "*/likes/*/like/")); n != 2 {
		t.Errorf("likes = %v", apiPosts(rec))
	}
	if n := len(posts(rec, "*/comments/FXMEDIA1/add/")); n != 2 {
		t.Errorf("comments = %v", apiPosts(rec))
	}
	if n := len(posts(rec, "*/unlike/*")); n != 0 {
		t.Errorf("UNLIKED something: %v", apiPosts(rec))
	}
}

func TestFlowEngageUserPostsLikeOnlyAlreadyLikedSkipped(t *testing.T) {
	s := newSite()
	s.post["liked"] = true
	page, rec := s.open(t)
	_, err := flow{actionType: "engage_user_posts", params: map[string]interface{}{
		"username": "fake.me", "targetUsername": "fake.ada", "maxContentCount": 3, "engagementActions": []interface{}{"like"},
	}}.run(t, page)
	if err != nil {
		t.Fatal(err)
	}
	// Every post is already liked: nothing may be clicked (an un-like would be).
	if p := apiPosts(rec); len(p) != 0 {
		t.Errorf("clicked: %v", p)
	}
	if n := len(rec.Matching("GET", "https://www.instagram.com/fake.ada/*/FXPOST0*")); n != 3 {
		t.Errorf("posts opened: %d", n)
	}
}

func TestFlowPublishPost(t *testing.T) {
	s := newSite()
	page, rec := s.open(t)
	media := mediaFile(t)
	if _, err := (flow{actionType: "publish_post", params: map[string]interface{}{"text": "flow caption", "media": media}}).run(t, page); err != nil {
		t.Fatal(err)
	}
	got := posts(rec, "*/media/configure/")
	if len(got) != 1 || form(t, got[0]).Get("caption") != "flow caption" {
		t.Errorf("publishes = %v", apiPosts(rec))
	}
	// A failed share fails the action and is not attempted twice.
	s2 := newSite()
	s2.home["fail"] = true
	page2, rec2 := s2.open(t)
	if _, err := (flow{actionType: "publish_post", params: map[string]interface{}{"text": "x", "media": media}}).run(t, page2); err == nil {
		t.Fatal("failed share must fail the action")
	}
	if n := len(posts(rec2, "*/media/configure/")); n != 1 {
		t.Errorf("share attempts = %d", n)
	}
}
