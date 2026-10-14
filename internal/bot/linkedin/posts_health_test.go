//go:build !nosocial

package linkedin

// Offline tests of the LinkedIn post readers (a member's or company's posts,
// content search, a post's comments) and the messaging inbox list, driven
// by healthPage with canned page-script results.

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func healthStrings(rows []map[string]interface{}, key string) []string {
	var out []string
	for _, r := range rows {
		s, _ := r[key].(string)
		out = append(out, s)
	}
	return out
}

var healthOK = map[string]interface{}{"ok": true}

// --- list_user_posts -------------------------------------------------------------

func TestHealthListUserPostsCompanyDedupesAndMaps(t *testing.T) {
	healthTimings(t)
	p := newHealthPage(t).scriptCommon().onValue(hsWaitOK, healthOK).onValue(hsPosts, healthFixture(t, "posts"))
	rows, err := (&LinkedInBot{}).ListUserPosts(context.Background(), p, "https://www.linkedin.com/company/acme-test/about/", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if navs := p.navigations(); len(navs) != 1 || navs[0] != "https://www.linkedin.com/company/acme-test/posts/?feedView=all" {
		t.Errorf("navigations = %v", navs)
	}
	if got := healthStrings(rows, "activity_id"); !reflect.DeepEqual(got, []string{"7123456789012345678", "7123456789012345679", "7123456789012345680"}) {
		t.Fatalf("ids = %v (duplicates and id-less entries dropped)", got)
	}
	first := rows[0]
	want := map[string]interface{}{
		"urn": "urn:li:activity:7123456789012345678", "author": "Acme", "text_preview": "We are hiring.",
		"author_url": "https://www.linkedin.com/company/acme-test", "likes_count": 29, "comments_count": 4, "reposts_count": 1,
		"shortcode": "7123456789012345678",
	}
	for k, v := range want {
		if !reflect.DeepEqual(first[k], v) {
			t.Errorf("%s = %#v, want %#v", k, first[k], v)
		}
	}
	if !strings.Contains(first["url"].(string), "urn:li:activity:7123456789012345678") {
		t.Errorf("url = %v", first["url"])
	}
	if rows[1]["author_url"] != "https://www.linkedin.com/in/ann-lee-test" {
		t.Errorf("member author_url = %v", rows[1]["author_url"])
	}
	// One productive read, then three that add nothing.
	if got := p.hitCount(hsPosts); got != 4 {
		t.Errorf("read %d times, want 4", got)
	}
}

func TestHealthListUserPostsMemberFeedsAndMax(t *testing.T) {
	healthTimings(t)
	b := &LinkedInBot{}
	for activity, want := range map[string]string{
		"shares": "https://www.linkedin.com/in/ann-lee-test/recent-activity/shares/",
		"":       "https://www.linkedin.com/in/ann-lee-test/recent-activity/all/",
	} {
		p := newHealthPage(t).scriptCommon().onValue(hsWaitOK, healthOK).onValue(hsPosts, healthFixture(t, "posts"))
		rows, err := b.ListUserPosts(context.Background(), p, "ann-lee-test", 2, activity)
		if err != nil {
			t.Fatal(err)
		}
		if navs := p.navigations(); len(navs) != 1 || navs[0] != want {
			t.Errorf("%q: navigations = %v", activity, navs)
		}
		if len(rows) != 2 {
			t.Errorf("%q: rows = %d, want the max of 2", activity, len(rows))
		}
		if got := p.hitCount(hsPosts); got != 1 {
			t.Errorf("%q: read %d times, want 1 (max reached)", activity, got)
		}
	}
}

func TestHealthListUserPostsErrorsAndPartialResults(t *testing.T) {
	healthTimings(t)
	b := &LinkedInBot{}
	ctx := context.Background()
	if _, err := b.ListUserPosts(ctx, newHealthPage(t), " ", 5, ""); err == nil {
		t.Error("no profile: want error")
	}
	for _, u := range []string{"https://www.linkedin.com/feed/", "https://www.linkedin.com/jobs/view/1/"} {
		if _, err := b.ListUserPosts(ctx, newHealthPage(t), u, 5, ""); err == nil || !strings.Contains(err.Error(), "is not a profile") {
			t.Errorf("%s: err = %v", u, err)
		}
	}
	p := newHealthPage(t).scriptCommon().onValue(hsWaitOK, healthOK).onValue(hsPosts, errors.New("detached"))
	if _, err := b.ListUserPosts(ctx, p, "https://www.linkedin.com/in/x/", 5, ""); err == nil || !strings.Contains(err.Error(), "reading posts") {
		t.Errorf("read failure, nothing read: err = %v", err)
	}
	// A failure after a productive read keeps what was read.
	p = newHealthPage(t).scriptCommon().onValue(hsWaitOK, healthOK).
		on(hsPosts, func(c healthCall) interface{} {
			if c.N == 0 {
				return healthFixture(t, "posts")
			}
			return errors.New("detached")
		})
	rows, err := b.ListUserPosts(ctx, p, "https://www.linkedin.com/in/x/", 10, "")
	if err != nil || len(rows) != 3 {
		t.Errorf("partial = %d rows, %v; want 3 and no error", len(rows), err)
	}
	// The feed never renders a post: an empty list.
	p = newHealthPage(t).scriptCommon().onValue(hsWaitOK, map[string]interface{}{"ok": false}).onValue(hsPosts, []interface{}{})
	if rows, err = b.ListUserPosts(ctx, p, "https://www.linkedin.com/in/x/", 5, ""); err != nil || len(rows) != 0 {
		t.Errorf("empty feed = %v, %v", rows, err)
	}
	p = newHealthPage(t)
	p.navErr = errors.New("offline")
	if _, err = b.ListUserPosts(ctx, p, "https://www.linkedin.com/in/x/", 5, ""); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Errorf("navigation: err = %v", err)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = b.ListUserPosts(cctx, newHealthPage(t), "https://www.linkedin.com/in/x/", 5, ""); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: err = %v", err)
	}
}

func TestHealthListUserPostsClicksShowMore(t *testing.T) {
	healthTimings(t)
	posts := healthFixture(t, "posts")
	p := newHealthPage(t).onValue(hsScrollPage, true).onValue(hsUnmark, true).onValue(hsWaitOK, healthOK).
		onValue(hsMarkedBox, map[string]interface{}{"X": 1, "Y": 1, "W": 10, "H": 10, "Hit": true}).
		onValue(hsDOMClick, true).
		onValue(hsShowMore, true).
		on(hsPosts, func(c healthCall) interface{} {
			if c.N == 0 {
				return []interface{}{}
			}
			return posts
		})
	rows, err := (&LinkedInBot{}).ListUserPosts(context.Background(), p, "https://www.linkedin.com/in/x/", 3, "")
	if err != nil || len(rows) != 3 {
		t.Fatalf("= %d rows, %v", len(rows), err)
	}
	if p.hitCount(hsDOMClick) != 1 {
		t.Errorf("show-more clicks = %d, want 1", p.hitCount(hsDOMClick))
	}
}

// --- search_posts ----------------------------------------------------------------

func TestHealthSearchPosts(t *testing.T) {
	healthTimings(t)
	b := &LinkedInBot{}
	ctx := context.Background()
	p := newHealthPage(t).scriptCommon().onValue(hsWaitOK, healthOK).onValue(hsPosts, healthFixture(t, "posts"))
	rows, err := b.SearchPosts(ctx, p, "go jobs", 0)
	if err != nil || len(rows) != 3 {
		t.Fatalf("= %d rows, %v", len(rows), err)
	}
	if navs := p.navigations(); len(navs) != 1 || !strings.HasPrefix(navs[0], "https://www.linkedin.com/search/results/content/?keywords=go+jobs&origin=") {
		t.Errorf("navigations = %v", navs)
	}
	const direct = "https://www.linkedin.com/search/results/content/?keywords=x&sortBy=date_posted"
	p = newHealthPage(t).scriptCommon().onValue(hsWaitOK, healthOK).onValue(hsPosts, healthFixture(t, "posts"))
	if _, err = b.SearchPosts(ctx, p, direct, 1); err != nil {
		t.Fatal(err)
	}
	if navs := p.navigations(); navs[0] != direct {
		t.Errorf("a search URL must be used as is: %v", navs)
	}
	p = newHealthPage(t).scriptCommon().onValue(hsWaitOK, healthOK).onValue(hsPosts, []interface{}{})
	if _, err = b.SearchPosts(ctx, p, "nothing", 5); err == nil || !strings.Contains(err.Error(), "no posts found") {
		t.Errorf("no results: err = %v", err)
	}
	p = newHealthPage(t).scriptCommon().onValue(hsWaitOK, healthOK).onValue(hsPosts, errors.New("threw"))
	if _, err = b.SearchPosts(ctx, p, "x", 5); err == nil || !strings.Contains(err.Error(), "reading posts") {
		t.Errorf("read: err = %v", err)
	}
	if _, err = b.SearchPosts(ctx, p, "  ", 5); err == nil {
		t.Error("empty keyword: want error")
	}
	p = newHealthPage(t)
	p.navErr = errors.New("offline")
	if _, err = b.SearchPosts(ctx, p, "x", 5); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Errorf("navigation: err = %v", err)
	}
}

// --- list_post_comments ------------------------------------------------------------

const healthPost = "https://www.linkedin.com/feed/update/urn:li:activity:7123456789012345678/"

func TestHealthListPostComments(t *testing.T) {
	healthTimings(t)
	b := &LinkedInBot{}
	ctx := context.Background()
	p := newHealthPage(t).scriptCommon().onValue(hsWaitOK, healthOK).
		onValue(hsCommentCount, 3).onValue(hsLoadMore, false).
		onValue(hsComments, healthFixture(t, "comments"))
	rows, err := b.ListPostComments(ctx, p, healthPost, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := healthStrings(rows, "author"); !reflect.DeepEqual(got, []string{"Bo Kim", "Acme"}) {
		t.Errorf("top-level authors = %v (replies excluded)", got)
	}
	if rows[0]["author_url"] != "https://www.linkedin.com/in/bo-kim-test" || rows[1]["author_url"] != "https://www.linkedin.com/company/acme-test" {
		t.Errorf("author urls = %v, %v", rows[0]["author_url"], rows[1]["author_url"])
	}
	if rows[0]["liked"] != true || rows[0]["parent_id"] != nil || rows[0]["post_url"] != healthPost || rows[0]["reply_count"] != 1 {
		t.Errorf("row 0 = %v", rows[0])
	}

	p = newHealthPage(t).scriptCommon().onValue(hsWaitOK, healthOK).
		onValue(hsCommentCount, 3).onValue(hsLoadMore, false).
		onValue(hsComments, healthFixture(t, "comments"))
	fn, _ := b.GetMethodByName("list_post_comments")
	res, err := fn(ctx, p, healthPost, "2", "true")
	if err != nil {
		t.Fatal(err)
	}
	all := res.([]map[string]interface{})
	if len(all) != 2 || all[1]["is_reply"] != true || all[1]["parent_id"] != "urn:li:comment:(activity:7123456789012345678,1)" {
		t.Errorf("with replies, max 2 = %v", all)
	}
}

func TestHealthListPostCommentsLoadsMore(t *testing.T) {
	healthTimings(t)
	// Each "load more" click renders more comments, until the button is gone.
	n := 1
	p := newHealthPage(t).scriptCommon().onValue(hsWaitOK, healthOK).
		onValue(hsMarkedBox, map[string]interface{}{"X": 1, "Y": 1, "W": 10, "H": 10, "Hit": true}).
		on(hsDOMClick, func(healthCall) interface{} { n++; return true }).
		on(hsCommentCount, func(healthCall) interface{} { return n }).
		on(hsLoadMore, func(c healthCall) interface{} {
			kind, _ := c.Args[0].(string)
			return (kind == "comments" && n < 3) || (kind == "replies" && n < 4)
		}).
		onValue(hsComments, healthFixture(t, "comments"))
	if _, err := (&LinkedInBot{}).ListPostComments(context.Background(), p, healthPost, 50, true); err != nil {
		t.Fatal(err)
	}
	if got := p.hitCount(hsDOMClick); got != 3 {
		t.Errorf("load-more clicks = %d, want 3 (2 comments, 1 replies)", got)
	}

	// Enough comments loaded: no click.
	p = newHealthPage(t).scriptCommon().onValue(hsWaitOK, healthOK).
		onValue(hsCommentCount, 10).onValue(hsLoadMore, true).
		onValue(hsComments, healthFixture(t, "comments"))
	if _, err := (&LinkedInBot{}).ListPostComments(context.Background(), p, healthPost, 5, false); err != nil {
		t.Fatal(err)
	}
	if p.hitCount(hsLoadMore) != 0 {
		t.Error("looked for load-more with enough comments loaded")
	}
}

func TestHealthListPostCommentsErrors(t *testing.T) {
	healthTimings(t)
	b := &LinkedInBot{}
	ctx := context.Background()
	if _, err := b.ListPostComments(ctx, newHealthPage(t), "", 5, false); err == nil {
		t.Error("no URL: want error")
	}
	p := newHealthPage(t).scriptCommon().onValue(hsWaitOK, map[string]interface{}{"ok": false})
	if _, err := b.ListPostComments(ctx, p, healthPost, 5, false); err == nil || !strings.Contains(err.Error(), "no post rendered") {
		t.Errorf("no post: err = %v", err)
	}
	p = newHealthPage(t).scriptCommon().onValue(hsWaitOK, healthOK).onValue(hsCommentCount, 0).onValue(hsLoadMore, false).
		onValue(hsComments, errors.New("threw"))
	if _, err := b.ListPostComments(ctx, p, healthPost, 5, false); err == nil || !strings.Contains(err.Error(), "reading comments") {
		t.Errorf("read: err = %v", err)
	}
	p = newHealthPage(t).scriptCommon().onValue(hsWaitOK, healthOK).onValue(hsCommentCount, 0).onValue(hsLoadMore, false).
		onValue(hsComments, []interface{}{})
	rows, err := b.ListPostComments(ctx, p, healthPost, 5, false)
	if err != nil || rows == nil || len(rows) != 0 {
		t.Errorf("no comments = %#v, %v", rows, err)
	}
	p = newHealthPage(t)
	p.navErr = errors.New("offline")
	if _, err = b.ListPostComments(ctx, p, healthPost, 5, false); err == nil {
		t.Error("navigation failure: want error")
	}
}

// --- list_conversations ------------------------------------------------------------

func TestHealthListConversations(t *testing.T) {
	healthTimings(t)
	b := &LinkedInBot{}
	ctx := context.Background()
	p := newHealthPage(t).onValue(hsConvos, healthFixture(t, "conversations"))
	rows, err := b.ListConversations(ctx, p, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := healthStrings(rows, "name"); !reflect.DeepEqual(got, []string{"Ann Lee", "Di Eze"}) {
		t.Errorf("unread, not from me = %v", got)
	}
	if navs := p.navigations(); len(navs) != 1 || navs[0] != "https://www.linkedin.com/messaging/" {
		t.Errorf("navigations = %v", navs)
	}
	rows, err = b.ListConversations(ctx, p, 3, false)
	if err != nil || !reflect.DeepEqual(healthStrings(rows, "name"), []string{"Ann Lee", "Bo Kim", "Cy Diaz"}) {
		t.Errorf("all, max 3 = %v, %v", rows, err)
	}
	if rows[1]["last_from_me"] != true || rows[2]["unread"] != false {
		t.Errorf("flags = %v", rows)
	}
	// The inbox renders slowly: polled until it has rows.
	p = newHealthPage(t).on(hsConvos, func(c healthCall) interface{} {
		if c.N < 2 {
			return []interface{}{}
		}
		return healthFixture(t, "conversations")
	})
	if rows, err = b.ListConversations(ctx, p, 1, false); err != nil || len(rows) != 1 {
		t.Errorf("slow inbox = %v, %v", rows, err)
	}
	p = newHealthPage(t).onValue(hsConvos, []interface{}{})
	if _, err = b.ListConversations(ctx, p, 5, false); err == nil || !strings.Contains(err.Error(), "no conversations found") {
		t.Errorf("empty inbox: err = %v", err)
	}
	p = newHealthPage(t)
	p.navErr = errors.New("offline")
	if _, err = b.ListConversations(ctx, p, 5, false); err == nil {
		t.Error("navigation failure: want error")
	}
}

func TestHealthCanonicalEntityURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://www.linkedin.com/company/acme/posts/": "https://www.linkedin.com/company/acme",
		"https://www.linkedin.com/company/acme/":       "https://www.linkedin.com/company/acme/",
		"https://www.linkedin.com/in/ann":              "https://www.linkedin.com/in/ann",
		"https://www.linkedin.com/school/uni/life":     "https://www.linkedin.com/school/uni",
		"https://example.com/x":                        "https://example.com/x",
		"":                                             "",
	} {
		if got := canonicalEntityURL(in); got != want {
			t.Errorf("canonicalEntityURL(%q) = %q, want %q", in, got, want)
		}
	}
}
