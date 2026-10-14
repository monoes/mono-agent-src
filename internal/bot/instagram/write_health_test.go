//go:build !nosocial

package instagram

// Offline tests of the Instagram post, comment, follow and story paths:
// state detection, refusals, verification and their error paths.

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

const (
	hsFindLike     = "if (!svg) return { state: 'not_found', count };"
	hsLikeVerify   = "if (viaJev) return false;"
	hsBox          = "if (box) { mark(box, m); return 'marked'; }"
	hsBaseline     = ".includes(want)).length; }"
	hsVerifyCmt    = "error: errorToast()"
	hsReplyPrefix  = "!el.hasAttribute(MARK)"
	hsTypeFocus    = "r.collapse(false)"
	hsBoxValue     = "(m) => boxValue(document"
	hsPostBtn      = "postButtonFor(box)"
	hsEnterOn      = "if (el) el.focus(); return !!el;"
	hsLikeOne      = "if (!c.like) return 'no_button';"
	hsLikeOneOK    = "return !!c && c.liked; }"
	hsReplyTarget  = "mark(c.reply, m);"
	hsFollowBtn    = "profileButton(/^follow( back)?$/i)"
	hsFollowVerify = "if (s === 'following' || s === 'requested') return s;"
	hsFollowingBtn = "profileButton(/^(following|requested)$/i)"
	hsUnfollowOpt  = "byText(d, /^unfollow$/i)"
	hsRing         = "View profile story"
	hsViewer       = "/^view story$/i"
	hsPath         = "() => location.pathname"
	hsNext         = `button[aria-label="Next"], [role="button"][aria-label="Next"]'); if (b) mark`
	hsClose        = `svg[aria-label="Close"]`
)

const healthPostURL = "https://www.instagram.com/p/AAA/"

func postPage(t *testing.T) *healthPage {
	return newHealthPage(t).scriptOpen().onValue(hsGone, false)
}

// --- scrape / list comments --------------------------------------------------

func TestHealthScrapePostData(t *testing.T) {
	healthFast(t)
	p := postPage(t).
		onSeq(hsPostData, healthFixture(t, "lists", "post_unrendered"), healthFixture(t, "lists", "post")).
		onValue(hsCommentLs, healthFixture(t, "lists", "comment_list")).
		onValue(hsLoadMore, false).
		onValue(hsComments, healthFixture(t, "lists", "comments"))
	data, err := New().ScrapePostData(context.Background(), p, "/p/AAA/", 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.navigations(); !reflect.DeepEqual(got, []string{healthPostURL}) {
		t.Errorf("navigations = %v (relative URL resolved)", got)
	}
	want := map[string]interface{}{
		"platform":        "INSTAGRAM",
		"author_username": "synth.ada",
		"author_url":      "https://www.instagram.com/synth.ada/",
		"likes_count":     "1234",
		"comments_count":  "1500",
		"post_type":       "carousel",
		"caption":         "Sunset #sky with @friend.one",
	}
	for k, v := range want {
		if !reflect.DeepEqual(data[k], v) {
			t.Errorf("%s = %#v, want %#v", k, data[k], v)
		}
	}
	if _, has := data["rendered"]; has {
		t.Error("internal 'rendered' flag leaked into the result")
	}
	cs, _ := data["comments"].([]interface{})
	if len(cs) != 2 {
		t.Fatalf("comments = %d, want 2 (maxComments)", len(cs))
	}
	c0, _ := cs[0].(map[string]interface{})
	if c0["author"] != "c.alice" || c0["likes_count"] != 12 || c0["reply_count"] != 2 || c0["post_url"] != healthPostURL {
		t.Errorf("comment 0 = %v", c0)
	}
}

func TestHealthScrapePostDataErrors(t *testing.T) {
	healthFast(t)
	b := New()
	ctx := context.Background()
	if _, err := b.ScrapePostData(ctx, newHealthPage(t), "https://www.instagram.com/synth.ada/", 0); err == nil || !strings.Contains(err.Error(), "not a post URL") {
		t.Errorf("profile URL: err = %v", err)
	}
	if _, err := b.ScrapePostData(ctx, newHealthPage(t), "  ", 0); err == nil {
		t.Error("empty URL: want error")
	}
	p := newHealthPage(t).scriptOpen().onValue(hsGone, true)
	if _, err := b.ScrapePostData(ctx, p, healthPostURL, 0); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Errorf("deleted post: err = %v", err)
	}
	p = postPage(t).onValue(hsPostData, healthFixture(t, "lists", "post_unrendered"))
	if _, err := b.ScrapePostData(ctx, p, healthPostURL, 0); err == nil || !strings.Contains(err.Error(), "did not render") {
		t.Errorf("never renders: err = %v", err)
	}
	// A failing comment read keeps the post data, without comments.
	p = postPage(t).onValue(hsPostData, healthFixture(t, "lists", "post")).onValue(hsCommentLs, errors.New("detached"))
	data, err := b.ScrapePostData(ctx, p, healthPostURL, 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, has := data["comments"]; has || data["author_username"] != "synth.ada" {
		t.Errorf("data = %v", data)
	}
}

func TestHealthListPostCommentsLoadsMore(t *testing.T) {
	healthFast(t)
	list := healthFixture(t, "lists", "comment_list").([]interface{})
	cs := healthFixture(t, "lists", "comments").([]interface{})
	// First read: one comment; "Load more" then reveals all of them.
	p := postPage(t).
		onSeq(hsComments, cs[:1], cs[:3]).
		onValue(hsBar, true).
		onSeq(hsCommentLs, list[:1], list).
		onSeq(hsLoadMore, true, false)
	rows, err := New().ListPostComments(context.Background(), p, healthPostURL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := healthUsernames(rows, "author"); !reflect.DeepEqual(got, []string{"c.alice", "c.bob", "c.cara"}) {
		t.Errorf("authors = %v", got)
	}
	if rows[1]["is_reply"] != true || rows[2]["author_url"] != "https://www.instagram.com/c.cara/" {
		t.Errorf("rows = %v", rows)
	}
	if p.clickCount() != 1 {
		t.Errorf("clicks = %d, want 1 (Load more)", p.clickCount())
	}

	p = postPage(t).onValue(hsComments, []interface{}{}).onValue(hsBar, true).onValue(hsCommentLs, errors.New("boom"))
	if _, err := New().ListPostComments(context.Background(), p, healthPostURL, 5); err == nil || !strings.Contains(err.Error(), "read comments") {
		t.Errorf("read failure: err = %v", err)
	}
}

// --- like a post ---------------------------------------------------------------

func TestHealthLikePost(t *testing.T) {
	healthFast(t)
	ctx := context.Background()

	p := postPage(t).onValue(hsFindLike, map[string]interface{}{"state": "liked", "count": 1})
	res, err := New().LikePost(ctx, p, healthPostURL)
	if err != nil || res["status"] != "already_liked" || p.clickCount() != 0 {
		t.Errorf("already liked = %v, %v, %d clicks", res, err, p.clickCount())
	}

	p = postPage(t).
		onSeq(hsFindLike, map[string]interface{}{"state": "not_found", "count": 0}, map[string]interface{}{"state": "marked", "count": 1}).
		onSeq(hsLikeVerify, false, true)
	res, err = New().LikePost(ctx, p, healthPostURL)
	if err != nil || res["status"] != "liked" || p.clickCount() != 1 {
		t.Errorf("like = %v, %v, %d clicks", res, err, p.clickCount())
	}

	// Several action bars and no Jev: the first bar's heart is still used.
	p = postPage(t).onValue(hsFindLike, map[string]interface{}{"state": "marked", "count": 2}).onValue(hsLikeVerify, true)
	if res, err = New().LikePost(ctx, p, healthPostURL); err != nil || res["status"] != "liked" {
		t.Errorf("two bars = %v, %v", res, err)
	}

	p = postPage(t).onValue(hsFindLike, map[string]interface{}{"state": "marked", "count": 1}).onValue(hsLikeVerify, false)
	if _, err = New().LikePost(ctx, p, healthPostURL); err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Errorf("unverified: err = %v", err)
	}

	p = postPage(t).onValue(hsFindLike, map[string]interface{}{"state": "not_found", "count": 0})
	if _, err = New().LikePost(ctx, p, healthPostURL); err == nil || !strings.Contains(err.Error(), "could not find") {
		t.Errorf("no heart, no Jev: err = %v", err)
	}
	if p.clickCount() != 0 {
		t.Error("clicked without a heart")
	}

	p = postPage(t).onValue(hsFindLike, map[string]interface{}{"state": "marked", "count": 1})
	p.elemErr = errors.New("gone")
	if _, err = New().LikePost(ctx, p, healthPostURL); err == nil || !strings.Contains(err.Error(), "click Like") {
		t.Errorf("vanished heart: err = %v", err)
	}
}

// --- comment / reply -------------------------------------------------------------

// commentPage scripts a post whose comment box accepts typing and whose
// Post button publishes; rendered[i] is the matching-comment count the
// verifier sees on its i-th read.
func commentPage(t *testing.T, rendered ...interface{}) *healthPage {
	return postPage(t).
		onValue(hsBox, "marked").
		onValue(hsBaseline, 0).
		onValue(hsTypeFocus, "").
		onValue(hsBoxValue, "Nice photo").
		onValue(hsPostBtn, map[string]interface{}{"found": true, "disabled": false}).
		onSeq(hsVerifyCmt, rendered...)
}

func TestHealthCommentPost(t *testing.T) {
	healthFast(t)
	ctx := context.Background()
	p := commentPage(t,
		map[string]interface{}{"count": 0, "box": "Nice photo", "error": ""},
		map[string]interface{}{"count": 1, "box": "", "error": ""})
	res, err := New().CommentPost(ctx, p, healthPostURL, "Nice photo")
	if err != nil {
		t.Fatal(err)
	}
	if res["status"] != "commented" || res["verified_by"] != "comment_rendered" {
		t.Errorf("res = %v", res)
	}
	if !reflect.DeepEqual(p.inputs, []string{"Nice photo"}) {
		t.Errorf("typed %v", p.inputs)
	}

	// The composer clears but the comment never renders: accepted as posted.
	p = commentPage(t, map[string]interface{}{"count": 0, "box": "", "error": ""})
	if res, err = New().CommentPost(ctx, p, healthPostURL, "Nice photo"); err != nil || res["verified_by"] != "composer_cleared" {
		t.Errorf("cleared = %v, %v", res, err)
	}

	// Instagram's error toast stops at once.
	p = commentPage(t, map[string]interface{}{"count": 0, "box": "Nice photo", "error": "Couldn't post comment"})
	if _, err = New().CommentPost(ctx, p, healthPostURL, "Nice photo"); err == nil || !strings.Contains(err.Error(), "Couldn't post comment") {
		t.Errorf("toast: err = %v", err)
	}
	if got := p.count(hsVerifyCmt); got != 1 {
		t.Errorf("verifier ran %d times after an error toast, want 1", got)
	}

	// Nothing changes: not confirmed.
	p = commentPage(t, map[string]interface{}{"count": 0, "box": "Nice photo", "error": ""})
	if _, err = New().CommentPost(ctx, p, healthPostURL, "Nice photo"); err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Errorf("unconfirmed: err = %v", err)
	}
}

func TestHealthCommentPostRefusals(t *testing.T) {
	healthFast(t)
	ctx := context.Background()
	if _, err := New().CommentPost(ctx, newHealthPage(t), healthPostURL, "  "); err == nil {
		t.Error("empty comment: want error")
	}
	p := postPage(t).onValue(hsBox, "disabled")
	if _, err := New().CommentPost(ctx, p, healthPostURL, "hi"); err == nil || !strings.Contains(err.Error(), "turned off") {
		t.Errorf("comments off: err = %v", err)
	}
	p = postPage(t).onValue(hsBox, "not_found")
	if _, err := New().CommentPost(ctx, p, healthPostURL, "hi"); err == nil || !strings.Contains(err.Error(), "comment box not found") {
		t.Errorf("no box: err = %v", err)
	}
	// The input ignores the typed text.
	p = commentPage(t)
	p.rules = append([]healthRule{{hsBoxValue, func(healthCall) interface{} { return "" }}}, p.rules...)
	if _, err := New().CommentPost(ctx, p, healthPostURL, "hi"); err == nil || !strings.Contains(err.Error(), "type comment") {
		t.Errorf("typing ignored: err = %v", err)
	}
	// The Post button stays disabled.
	p = commentPage(t, map[string]interface{}{"count": 1})
	p.rules = append([]healthRule{{hsPostBtn, func(healthCall) interface{} {
		return map[string]interface{}{"found": true, "disabled": true}
	}}}, p.rules...)
	if _, err := New().CommentPost(ctx, p, healthPostURL, "Nice photo"); err == nil || !strings.Contains(err.Error(), "stayed disabled") {
		t.Errorf("disabled Post: err = %v", err)
	}
	// No Post button and no Jev: Enter submits.
	p = commentPage(t, map[string]interface{}{"count": 1, "box": "", "error": ""}).onValue(hsEnterOn, true)
	p.rules = append([]healthRule{{hsPostBtn, func(healthCall) interface{} { return map[string]interface{}{"found": false} }}}, p.rules...)
	if _, err := New().CommentPost(ctx, p, healthPostURL, "Nice photo"); err != nil {
		t.Errorf("Enter fallback: %v", err)
	}
	if len(p.keys) != 1 || p.keys[0] != '\r' {
		t.Errorf("keys = %q, want Enter", p.keys)
	}
}

func TestHealthReplyToComment(t *testing.T) {
	healthFast(t)
	ctx := context.Background()
	cs := healthFixture(t, "lists", "comments")
	page := func(prefix string) *healthPage {
		p := postPage(t).
			onValue(hsComments, cs).onValue(hsBar, true).
			onValue(hsReplyTarget, map[string]interface{}{"state": "marked", "author": "c.bob"}).
			onValue(hsReplyPrefix, prefix).
			onValue(hsBox, "marked").
			onValue(hsBaseline, 0).
			onValue(hsTypeFocus, prefix).
			onValue(hsBoxValue, prefix+"thanks!").
			onValue(hsPostBtn, map[string]interface{}{"found": true}).
			onValue(hsVerifyCmt, map[string]interface{}{"count": 1})
		return p
	}
	p := page("@c.bob ")
	res, err := New().ReplyToComment(ctx, p, healthPostURL, "@c.bob", "@c.bob thanks!")
	if err != nil {
		t.Fatal(err)
	}
	if res["status"] != "replied" || res["replied_to"] != "c.bob" {
		t.Errorf("res = %v", res)
	}
	// The @prefix Instagram filled in is not typed a second time.
	if !reflect.DeepEqual(p.inputs, []string{"@c.bob thanks!"}) {
		t.Errorf("typed %q", p.inputs)
	}

	// Reply did not address the author: refuse to post an unthreaded comment.
	p = page("")
	if _, err = New().ReplyToComment(ctx, p, healthPostURL, "c.bob", "thanks!"); err == nil || !strings.Contains(err.Error(), "not posting an unthreaded comment") {
		t.Errorf("no prefix: err = %v", err)
	}
	if len(p.inputs) != 0 {
		t.Errorf("typed %q into an unaddressed composer", p.inputs)
	}
}

func TestHealthReplyToCommentRefusals(t *testing.T) {
	healthFast(t)
	ctx := context.Background()
	if _, err := New().ReplyToComment(ctx, newHealthPage(t), healthPostURL, "", " "); err == nil {
		t.Error("empty reply: want error")
	}
	for state, want := range map[string]string{
		"not_found":        "no comments",
		"author_not_found": "refusing to reply to a different comment",
		"weird":            "unexpected comment lookup state",
	} {
		p := postPage(t).
			onValue(hsComments, []interface{}{}).onValue(hsBar, true).onValue(hsLoadMore, false).
			onValue(hsReplyTarget, map[string]interface{}{"state": state})
		if _, err := New().ReplyToComment(ctx, p, healthPostURL, "ghost", "hi"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v", state, err)
		}
		if p.clickCount() != 0 {
			t.Errorf("%s: clicked", state)
		}
	}
}

// --- like comments -----------------------------------------------------------------

func TestHealthLikeComments(t *testing.T) {
	healthFast(t)
	ctx := context.Background()
	cs := healthFixture(t, "lists", "comments")
	page := func() *healthPage {
		return postPage(t).
			onValue(hsComments, cs).onValue(hsBar, true).onValue(hsLoadMore, false).
			onValue(hsLikeOne, "marked").onValue(hsLikeOneOK, true)
	}

	// No author: likes up to max, skipping liked and button-less comments.
	p := page()
	res, err := New().LikeComments(ctx, p, healthPostURL, "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if res["liked_count"] != 2 || res["already_liked_count"] != 1 || res["liked_authors"] != "c.alice,c.dan" {
		t.Errorf("res = %v", res)
	}

	p = page()
	if res, err = New().LikeComments(ctx, p, healthPostURL, "", 0); err != nil || res["liked_count"] != 1 {
		t.Errorf("default max = %v, %v; want 1 like", res, err)
	}

	// By author: only that author's comment.
	p = page()
	if res, err = New().LikeComments(ctx, p, healthPostURL, "@C.Dan", 9); err != nil || res["liked_authors"] != "c.dan" {
		t.Errorf("by author = %v, %v", res, err)
	}
	p = page()
	if res, err = New().LikeComments(ctx, p, healthPostURL, "c.bob", 1); err != nil || res["status"] != "already_liked" || p.clickCount() != 0 {
		t.Errorf("already liked = %v, %v, %d clicks", res, err, p.clickCount())
	}
	p = page()
	if _, err = New().LikeComments(ctx, p, healthPostURL, "c.cara", 1); err == nil || !strings.Contains(err.Error(), "no Like button") {
		t.Errorf("no button: err = %v", err)
	}
	p = page()
	if _, err = New().LikeComments(ctx, p, healthPostURL, "ghost", 1); err == nil || !strings.Contains(err.Error(), "refusing to like a different comment") {
		t.Errorf("missing author: err = %v", err)
	}
	if got := p.count(hsLoadMore); got != 1 {
		t.Errorf("load more tried %d times, want 1 (stops when there is no button)", got)
	}

	// The like does not turn into Unlike.
	p = postPage(t).onValue(hsComments, cs).onValue(hsBar, true).onValue(hsLikeOne, "marked").onValue(hsLikeOneOK, false)
	if _, err = New().LikeComments(ctx, p, healthPostURL, "c.alice", 1); err == nil || !strings.Contains(err.Error(), "like c.alice's comment") {
		t.Errorf("unverified: err = %v", err)
	}
	// The comment vanished between read and click.
	p = postPage(t).onValue(hsComments, cs).onValue(hsBar, true).onValue(hsLikeOne, "gone")
	if _, err = New().LikeComments(ctx, p, healthPostURL, "c.alice", 1); err == nil || !strings.Contains(err.Error(), "comment gone") {
		t.Errorf("gone: err = %v", err)
	}
	// It turned liked meanwhile: counts as liked without a click.
	p = postPage(t).onValue(hsComments, cs).onValue(hsBar, true).onValue(hsLikeOne, "liked")
	if res, err = New().LikeComments(ctx, p, healthPostURL, "c.alice", 1); err != nil || res["liked_count"] != 1 || p.clickCount() != 0 {
		t.Errorf("liked meanwhile = %v, %v", res, err)
	}
	// No comments at all.
	p = postPage(t).onValue(hsComments, []interface{}{}).onValue(hsBar, true)
	if _, err = New().LikeComments(ctx, p, healthPostURL, "", 1); err == nil || !strings.Contains(err.Error(), "no comments") {
		t.Errorf("no comments: err = %v", err)
	}
	// Only liked/button-less comments: nothing likeable.
	only := []interface{}{healthFixture(t, "lists", "comments").([]interface{})[2]}
	p = postPage(t).onValue(hsComments, only).onValue(hsBar, true)
	if _, err = New().LikeComments(ctx, p, healthPostURL, "", 3); err == nil || !strings.Contains(err.Error(), "no likeable comment") {
		t.Errorf("nothing likeable: err = %v", err)
	}
}

// --- follow / unfollow ----------------------------------------------------------

func TestHealthFollowUser(t *testing.T) {
	healthFast(t)
	ctx := context.Background()
	page := func(state string) *healthPage {
		return newHealthPage(t).scriptOpen().onValue(hsFollowSt, state).onValue(hsUnavail, false)
	}
	for _, st := range []string{"following", "requested"} {
		p := page(st)
		res, err := New().FollowUser(ctx, p, "https://www.instagram.com/synth.ada/")
		if err != nil || res["status"] != "already_"+st || p.clickCount() != 0 {
			t.Errorf("%s: %v, %v, %d clicks", st, res, err, p.clickCount())
		}
	}
	for verify, want := range map[string]string{"following": "followed", "requested": "requested"} {
		p := page("not_following").onValue(hsFollowBtn, true).onValue(hsFollowVerify, verify)
		res, err := New().FollowUser(ctx, p, "synth.ada")
		if err != nil || res["status"] != want || res["username"] != "synth.ada" || p.clickCount() != 1 {
			t.Errorf("verify %s: %v, %v, %d clicks", verify, res, err, p.clickCount())
		}
	}
	p := page("not_following").onValue(hsFollowBtn, true).onValue(hsFollowVerify, "not_following")
	if _, err := New().FollowUser(ctx, p, "synth.ada"); err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Errorf("unconfirmed: err = %v", err)
	}
	p = page("not_following").onValue(hsFollowBtn, false)
	if _, err := New().FollowUser(ctx, p, "synth.ada"); err == nil || !strings.Contains(err.Error(), "vanished") {
		t.Errorf("vanished: err = %v", err)
	}
	p = page("")
	if _, err := New().FollowUser(ctx, p, "synth.ada"); err == nil || !strings.Contains(err.Error(), "no Follow button") {
		t.Errorf("no button, no Jev: err = %v", err)
	}
	p = newHealthPage(t).scriptOpen().onValue(hsFollowSt, "").onValue(hsUnavail, true)
	if _, err := New().FollowUser(ctx, p, "gone.user"); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Errorf("unavailable: err = %v", err)
	}
	if _, err := New().FollowUser(ctx, newHealthPage(t), "https://www.instagram.com/explore/"); err == nil {
		t.Error("no username: want error")
	}
}

func TestHealthUnfollowUser(t *testing.T) {
	healthFast(t)
	ctx := context.Background()
	p := newHealthPage(t).scriptOpen().onValue(hsFollowSt, "not_following").onValue(hsUnavail, false)
	if res, err := New().UnfollowUser(ctx, p, "synth.ada"); err != nil || res["status"] != "not_following" || p.clickCount() != 0 {
		t.Errorf("not following = %v, %v", res, err)
	}

	p = newHealthPage(t).scriptOpen().
		onSeq(hsFollowSt, "following", "following", "not_following").onValue(hsUnavail, false).
		onValue(hsFollowingBtn, true).
		onSeq(hsUnfollowOpt, false, true)
	res, err := New().UnfollowUser(ctx, p, "synth.ada")
	if err != nil || res["status"] != "unfollowed" || p.clickCount() != 2 {
		t.Errorf("unfollow = %v, %v, %d clicks", res, err, p.clickCount())
	}

	p = newHealthPage(t).scriptOpen().onValue(hsFollowSt, "requested").onValue(hsUnavail, false).
		onValue(hsFollowingBtn, true).onValue(hsUnfollowOpt, true)
	if _, err := New().UnfollowUser(ctx, p, "synth.ada"); err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Errorf("unconfirmed: err = %v", err)
	}
	p = newHealthPage(t).scriptOpen().onValue(hsFollowSt, "following").onValue(hsUnavail, false).
		onValue(hsFollowingBtn, true).onValue(hsUnfollowOpt, false)
	if _, err := New().UnfollowUser(ctx, p, "synth.ada"); err == nil || !strings.Contains(err.Error(), "Unfollow confirmation not found") {
		t.Errorf("no dialog option: err = %v", err)
	}
	if p.clickCount() != 1 {
		t.Errorf("clicks = %d, want only the Following button", p.clickCount())
	}
	p = newHealthPage(t).scriptOpen().onValue(hsFollowSt, "following").onValue(hsUnavail, false).onValue(hsFollowingBtn, false)
	if _, err := New().UnfollowUser(ctx, p, "synth.ada"); err == nil || !strings.Contains(err.Error(), "vanished") {
		t.Errorf("vanished: err = %v", err)
	}
	p = newHealthPage(t).scriptOpen().onValue(hsFollowSt, "").onValue(hsUnavail, false)
	if _, err := New().UnfollowUser(ctx, p, "synth.ada"); err == nil || !strings.Contains(err.Error(), "no follow button") {
		t.Errorf("no state: err = %v", err)
	}
}

// --- stories ----------------------------------------------------------------------

func storyPage(t *testing.T, viewer ...interface{}) *healthPage {
	return newHealthPage(t).scriptOpen().
		onValue(hsHeader, true).
		onValue(hsRing, "marked").
		onSeq(hsViewer, viewer...).
		on(hsPath, func(c healthCall) interface{} { return fmt.Sprintf("/stories/synth.ada/%d/", c.N) }).
		onValue(hsNext, true).
		onValue(hsClose, true)
}

func TestHealthViewStories(t *testing.T) {
	healthFast(t)
	ctx := context.Background()
	open := map[string]interface{}{"open": true, "owner": "synth.ada"}
	p := storyPage(t, open)
	res, err := New().ViewStories(ctx, p, "synth.ada", 3)
	if err != nil {
		t.Fatal(err)
	}
	if res["stories_viewed"] != 3 || res["status"] != "viewed" {
		t.Errorf("res = %v", res)
	}
	// Ring, two Next clicks, and Close.
	if p.clickCount() != 4 {
		t.Errorf("clicks = %d, want 4", p.clickCount())
	}

	// The viewer moves on to another account after the first story.
	p = storyPage(t, open, map[string]interface{}{"open": true, "owner": "someone.else"})
	if res, err = New().ViewStories(ctx, p, "synth.ada", 0); err != nil || res["stories_viewed"] != 1 {
		t.Errorf("moved on = %v, %v", res, err)
	}

	p = newHealthPage(t).scriptOpen().onValue(hsHeader, true).onValue(hsRing, "none")
	if res, err = New().ViewStories(ctx, p, "synth.ada", 2); err != nil || res["status"] != "no_active_story" || res["stories_viewed"] != 0 {
		t.Errorf("no story = %v, %v", res, err)
	}

	p = storyPage(t, map[string]interface{}{"open": false})
	if _, err = New().ViewStories(ctx, p, "synth.ada", 2); err == nil || !strings.Contains(err.Error(), "did not open") {
		t.Errorf("viewer never opens: err = %v", err)
	}
	p = newHealthPage(t).scriptOpen().onValue(hsHeader, false)
	if _, err = New().ViewStories(ctx, p, "synth.ada", 2); err == nil || !strings.Contains(err.Error(), "did not render") {
		t.Errorf("no header: err = %v", err)
	}
	p = newHealthPage(t).scriptOpen().onValue(hsHeader, true).onValue(hsRing, errors.New("threw"))
	if _, err = New().ViewStories(ctx, p, "synth.ada", 2); err == nil {
		t.Error("ring script failure: want error")
	}
}

func TestHealthWriteMethodsThroughGetMethodByName(t *testing.T) {
	healthFast(t)
	b := New()
	for _, name := range []string{"like_post", "comment_post", "like_comment", "reply_comment", "follow_user", "unfollow_user", "view_stories", "send_message", "reply_to_conversation", "publish_content", "extract_username_from_metadata"} {
		fn, ok := b.GetMethodByName(name)
		if !ok {
			t.Fatalf("%s missing", name)
		}
		if _, err := fn(context.Background()); err == nil {
			t.Errorf("%s without a page: want error", name)
		}
	}
	fn, _ := b.GetMethodByName("follow_user")
	p := newHealthPage(t).scriptOpen().onValue(hsFollowSt, "following").onValue(hsUnavail, false)
	if res, err := fn(context.Background(), p, "synth.ada"); err != nil || res.(map[string]interface{})["status"] != "already_following" {
		t.Errorf("follow_user = %v, %v", res, err)
	}
	fn, _ = b.GetMethodByName("send_message")
	if _, err := fn(context.Background(), newHealthPage(t), "https://www.instagram.com/explore/", "hi"); err == nil {
		t.Error("send_message without a username: want error")
	}
	fn, _ = b.GetMethodByName("scrape_post_data")
	p = postPage(t).onValue(hsPostData, healthFixture(t, "lists", "post")).
		onValue(hsCommentLs, healthFixture(t, "lists", "comment_list")).onValue(hsLoadMore, false).
		onValue(hsComments, healthFixture(t, "lists", "comments"))
	res, err := fn(context.Background(), p, healthPostURL, "true", "")
	if err != nil {
		t.Fatal(err)
	}
	if cs, _ := res.(map[string]interface{})["comments"].([]interface{}); len(cs) != 3 {
		t.Errorf("scrape_post_data with comments = %d comments, want 3", len(cs))
	}
}
