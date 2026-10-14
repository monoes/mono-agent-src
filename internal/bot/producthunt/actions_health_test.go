//go:build !nosocial

package producthunt

// Offline tests of the Product Hunt actions (list_comments with comment
// expansion, get_launch_metrics, comment_on_launch) driven by healthPage
// with canned page-script results.

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

const healthLaunch = "https://www.producthunt.com/products/synth-app/launches/synth-app"

func exp(count int, found bool) map[string]interface{} {
	return map[string]interface{}{"count": count, "found": found}
}

func commentPage(t *testing.T) *healthPage {
	return newHealthPage(t).
		onValue(hsScrollSection, true).
		onValue(hsComments, healthFixture(t, "comments", "comments")).
		onValue(hsGiveUp, true)
}

// --- list_comments ---------------------------------------------------------------

func TestHealthListCommentsExpandsThenReads(t *testing.T) {
	healthFast(t)
	p := commentPage(t).
		onSeq(hsNextExp, exp(2, true), exp(4, true), exp(4, false)).
		onValue(hsExpState, map[string]interface{}{"count": 4, "present": true})
	rows, err := (&ProductHuntBot{}).ListComments(context.Background(), p, "/products/synth-app/launches/synth-app", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.navigations(); !reflect.DeepEqual(got, []string{healthLaunch}) {
		t.Errorf("navigations = %v", got)
	}
	if len(rows) != 4 {
		t.Fatalf("rows = %d, want 4", len(rows))
	}
	want := map[string]interface{}{"id": "1002", "author": "Maker", "username": "maker", "text": "Thank you!", "depth": 1, "parentId": "1001", "upvotes": 3}
	if !reflect.DeepEqual(rows[1], want) {
		t.Errorf("reply = %#v", rows[1])
	}
	// Two clicks; the second loaded nothing and was given up on.
	if p.clickCount() != 2 || p.count(hsGiveUp) != 1 {
		t.Errorf("clicks %d, give-ups %d; want 2 and 1", p.clickCount(), p.count(hsGiveUp))
	}
}

func TestHealthListCommentsStopsExpandingAtMax(t *testing.T) {
	healthFast(t)
	p := commentPage(t).onValue(hsNextExp, exp(2, true))
	rows, err := (&ProductHuntBot{}).ListComments(context.Background(), p, healthLaunch, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0]["id"] != "1001" || rows[1]["id"] != "1002" {
		t.Errorf("rows = %v", rows)
	}
	if p.clickCount() != 0 {
		t.Errorf("clicks = %d, want 0 (enough comments loaded)", p.clickCount())
	}
}

func TestHealthListCommentsClickCap(t *testing.T) {
	healthFast(t)
	expandClicks = 3
	p := commentPage(t).
		on(hsNextExp, func(c healthCall) interface{} { return exp(c.N*10, true) }).
		on(hsExpState, func(c healthCall) interface{} { return map[string]interface{}{"count": 1000} })
	if _, err := (&ProductHuntBot{}).ListComments(context.Background(), p, healthLaunch, 0); err != nil {
		t.Fatal(err)
	}
	if p.clickCount() != 3 {
		t.Errorf("clicks = %d, want the cap of 3", p.clickCount())
	}
}

func TestHealthListCommentsExpansionIsBestEffort(t *testing.T) {
	healthFast(t)
	// The tagged control cannot be found: given up, not clicked.
	p := commentPage(t).onSeq(hsNextExp, exp(1, true), exp(1, false)).raceMiss(expandMark)
	rows, err := (&ProductHuntBot{}).ListComments(context.Background(), p, healthLaunch, 0)
	if err != nil || len(rows) != 4 {
		t.Fatalf("= %d rows, %v", len(rows), err)
	}
	if p.clickCount() != 0 || p.count(hsGiveUp) != 1 {
		t.Errorf("clicks %d, give-ups %d; want 0 and 1", p.clickCount(), p.count(hsGiveUp))
	}
	// The expander script throws: reading goes ahead.
	p = commentPage(t).onValue(hsNextExp, errors.New("threw"))
	if rows, err = (&ProductHuntBot{}).ListComments(context.Background(), p, healthLaunch, 0); err != nil || len(rows) != 4 {
		t.Errorf("expander failure = %d rows, %v", len(rows), err)
	}
	// Cancelled while expanding.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p = commentPage(t)
	if _, err = (&ProductHuntBot{}).ListComments(ctx, p, healthLaunch, 0); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: err = %v", err)
	}
}

func TestHealthListCommentsErrors(t *testing.T) {
	healthFast(t)
	b := &ProductHuntBot{}
	ctx := context.Background()
	for _, u := range []string{"", "http://www.producthunt.com/posts/x", "https://evil.example/posts/x", "https://producthunt.com.evil.example/x", "://bad"} {
		if _, err := b.ListComments(ctx, newHealthPage(t), u, 0); err == nil {
			t.Errorf("URL %q: want error", u)
		}
	}
	p := newHealthPage(t)
	p.navErr = errors.New("offline")
	if _, err := b.ListComments(ctx, p, healthLaunch, 0); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Errorf("navigation: err = %v", err)
	}
	p = newHealthPage(t)
	p.loadErr = errors.New("timeout")
	if _, err := b.ListComments(ctx, p, healthLaunch, 0); err == nil || !strings.Contains(err.Error(), "did not load") {
		t.Errorf("load: err = %v", err)
	}
	p = commentPage(t).raceMiss("comments-feed")
	if _, err := b.ListComments(ctx, p, healthLaunch, 0); err == nil || !strings.Contains(err.Error(), "no comment section") {
		t.Errorf("no section: err = %v", err)
	}
	p = newHealthPage(t).onValue(hsScrollSection, true).onValue(hsNextExp, exp(0, false)).onValue(hsComments, errors.New("threw"))
	if _, err := b.ListComments(ctx, p, healthLaunch, 0); err == nil || !strings.Contains(err.Error(), "reading comments") {
		t.Errorf("read: err = %v", err)
	}
}

func TestHealthListCommentsMethodArgs(t *testing.T) {
	healthFast(t)
	fn, ok := (&ProductHuntBot{}).GetMethodByName("list_comments")
	if !ok {
		t.Fatal("list_comments missing")
	}
	for _, bad := range []string{"abc", "-1", "1.5"} {
		if _, err := fn(context.Background(), commentPage(t), healthLaunch, bad); err == nil || !strings.Contains(err.Error(), "whole number") {
			t.Errorf("maxComments %q: err = %v", bad, err)
		}
	}
	p := commentPage(t).onValue(hsNextExp, exp(4, false))
	res, err := fn(context.Background(), p, healthLaunch, " 3 ")
	if err != nil {
		t.Fatal(err)
	}
	if rows := res.([]map[string]interface{}); len(rows) != 3 {
		t.Errorf("rows = %d, want 3", len(rows))
	}
	p = commentPage(t).onValue(hsNextExp, exp(4, false))
	if res, err = fn(context.Background(), p, healthLaunch); err != nil || len(res.([]map[string]interface{})) != 4 {
		t.Errorf("default max = %v, %v", res, err)
	}
	if _, err = fn(context.Background()); err == nil {
		t.Error("no page: want error")
	}
}

// --- get_launch_metrics ------------------------------------------------------------

func TestHealthGetLaunchMetrics(t *testing.T) {
	healthFast(t)
	b := &ProductHuntBot{}
	ctx := context.Background()
	p := newHealthPage(t).onValue(hsScrollSection, true).onValue(hsMetrics, healthFixture(t, "comments", "metrics"))
	fn, _ := b.GetMethodByName("get_launch_metrics")
	res, err := fn(ctx, p, healthLaunch)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]interface{}{"launchURL": healthLaunch, "name": "Synth App", "upvotes": 431, "upvotesHidden": false, "comments": 57, "commentsSource": "label"}
	if !reflect.DeepEqual(res, want) {
		t.Errorf("metrics = %#v", res)
	}

	p = newHealthPage(t).onValue(hsScrollSection, true).onValue(hsMetrics, healthFixture(t, "comments", "metrics_hidden"))
	m, err := b.GetLaunchMetrics(ctx, p, healthLaunch)
	if err != nil || m["upvotes"] != nil || m["upvotesHidden"] != true || m["commentsSource"] != "loaded" {
		t.Errorf("hidden = %v, %v", m, err)
	}
	// No comment section is fine for metrics.
	p = newHealthPage(t).onValue(hsScrollSection, true).onValue(hsMetrics, healthFixture(t, "comments", "metrics")).raceMiss("comments-feed")
	if _, err = b.GetLaunchMetrics(ctx, p, healthLaunch); err != nil {
		t.Errorf("no comment section: %v", err)
	}

	for name, c := range map[string]struct {
		v    interface{}
		want string
	}{
		"ambiguous": {healthFixture(t, "comments", "metrics_ambiguous"), "other 6 vote buttons"},
		"missing":   {healthFixture(t, "comments", "metrics_missing"), "was not found"},
		"script":    {errors.New("threw"), "reading metrics"},
	} {
		p = newHealthPage(t).onValue(hsScrollSection, true).onValue(hsMetrics, c.v)
		if _, err = b.GetLaunchMetrics(ctx, p, healthLaunch); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	p = newHealthPage(t).raceMiss("vote-button")
	if _, err = b.GetLaunchMetrics(ctx, p, healthLaunch); err == nil || !strings.Contains(err.Error(), "no upvote button") {
		t.Errorf("no vote button: err = %v", err)
	}
	if _, err = b.GetLaunchMetrics(ctx, p, "https://example.com/"); err == nil {
		t.Error("foreign URL: want error")
	}
	p = newHealthPage(t)
	p.navErr = errors.New("offline")
	if _, err = b.GetLaunchMetrics(ctx, p, healthLaunch); err == nil {
		t.Error("navigation failure: want error")
	}
}

// --- comment_on_launch -----------------------------------------------------------------

func composerPage(t *testing.T, state map[string]interface{}, newComment ...interface{}) *healthPage {
	return newHealthPage(t).
		onValue(hsScrollSection, true).
		onValue(hsComposer, state).
		onValue(hsComments, healthFixture(t, "comments", "comments")).
		onSeq(hsNewComment, newComment...)
}

var healthComposer = map[string]interface{}{"found": true, "signIn": false, "button": "Comment", "editor": true}

func TestHealthCommentOnLaunch(t *testing.T) {
	healthFast(t)
	b := &ProductHuntBot{}
	p := composerPage(t, healthComposer, map[string]interface{}{"id": ""}, map[string]interface{}{"id": "2001"})
	fn, _ := b.GetMethodByName("comment_on_launch")
	res, err := fn(context.Background(), p, healthLaunch, "Great launch, congrats!")
	if err != nil {
		t.Fatal(err)
	}
	if m := res.(map[string]interface{}); m["commentID"] != "2001" || m["success"] != true {
		t.Errorf("res = %v", m)
	}
	if len(p.inputs) != 1 || p.inputs[0] != "Great launch, congrats!" {
		t.Errorf("typed %q", p.inputs)
	}
	// The poll skips the comments that existed before and looks for the
	// typed text without whitespace.
	c := p.count(hsNewComment)
	if c != 2 {
		t.Errorf("confirmation polled %d times, want 2", c)
	}
	if len(p.clicks) != 1 || !strings.Contains(p.clicks[0], "form-submit-button") {
		t.Errorf("clicks = %v, want the form's submit button", p.clicks)
	}
}

func TestHealthCommentOnLaunchPlaceholderClick(t *testing.T) {
	healthFast(t)
	state := map[string]interface{}{"found": true, "button": "Comment", "editor": false}
	p := composerPage(t, state, map[string]interface{}{"id": "2002"})
	if _, err := (&ProductHuntBot{}).CommentOnLaunch(context.Background(), p, healthLaunch, "hi"); err != nil {
		t.Fatal(err)
	}
	// The placeholder that mounts the editor, then the submit button.
	if len(p.clicks) != 2 || !strings.HasSuffix(p.clicks[0], " p") {
		t.Errorf("clicks = %v", p.clicks)
	}
}

func TestHealthCommentOnLaunchRefusals(t *testing.T) {
	healthFast(t)
	b := &ProductHuntBot{}
	ctx := context.Background()
	if _, err := b.CommentOnLaunch(ctx, newHealthPage(t), healthLaunch, "  "); err == nil {
		t.Error("empty text: want error")
	}
	if _, err := b.CommentOnLaunch(ctx, newHealthPage(t), "https://example.com/x", "hi"); err == nil {
		t.Error("foreign URL: want error")
	}
	for name, state := range map[string]map[string]interface{}{
		"login button": {"found": true, "button": "Log in to comment", "editor": true},
		"sign-in only": {"found": false, "signIn": true},
	} {
		p := composerPage(t, state)
		if _, err := b.CommentOnLaunch(ctx, p, healthLaunch, "hi"); err == nil || !strings.Contains(err.Error(), "not logged in") {
			t.Errorf("%s: err = %v", name, err)
		}
		if len(p.inputs) != 0 || p.clickCount() != 0 {
			t.Errorf("%s: typed or clicked", name)
		}
	}
	p := composerPage(t, map[string]interface{}{"found": false})
	if _, err := b.CommentOnLaunch(ctx, p, healthLaunch, "hi"); err == nil || !strings.Contains(err.Error(), "comment box not found") {
		t.Errorf("no form: err = %v", err)
	}
	p = composerPage(t, healthComposer).raceMiss("contenteditable")
	if _, err := b.CommentOnLaunch(ctx, p, healthLaunch, "hi"); err == nil || !strings.Contains(err.Error(), "comment box not found") {
		t.Errorf("no editor: err = %v", err)
	}
	p = composerPage(t, healthComposer).raceMiss("form-submit-button")
	if _, err := b.CommentOnLaunch(ctx, p, healthLaunch, "hi"); err == nil || !strings.Contains(err.Error(), "submit button not found") {
		t.Errorf("no submit: err = %v", err)
	}
	p = composerPage(t, healthComposer)
	p.text["[data-monoagent-ph-form] [data-test='form-submit-button']"] = "Sign in"
	if _, err := b.CommentOnLaunch(ctx, p, healthLaunch, "hi"); err == nil || !strings.Contains(err.Error(), "submit button reads") {
		t.Errorf("sign-in submit: err = %v", err)
	}
	if p.clickCount() != 0 {
		t.Error("clicked a sign-in button")
	}
	p = composerPage(t, healthComposer, map[string]interface{}{"id": ""})
	if _, err := b.CommentOnLaunch(ctx, p, healthLaunch, "hi"); err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Errorf("unconfirmed: err = %v", err)
	}
	p = composerPage(t, healthComposer).onValue(hsComments, errors.New("threw"))
	p.rules = append([]healthRule{{hsComments, func(healthCall) interface{} { return errors.New("threw") }}}, p.rules...)
	if _, err := b.CommentOnLaunch(ctx, p, healthLaunch, "hi"); err == nil || !strings.Contains(err.Error(), "reading comments") {
		t.Errorf("read before: err = %v", err)
	}
	p = newHealthPage(t).onValue(hsScrollSection, true).onValue(hsComposer, errors.New("threw"))
	if _, err := b.CommentOnLaunch(ctx, p, healthLaunch, "hi"); err == nil || !strings.Contains(err.Error(), "reading the comment form") {
		t.Errorf("form script: err = %v", err)
	}
}

func TestHealthCommentOnLaunchTextDidNotTake(t *testing.T) {
	healthFast(t)
	// An editor that ignores input and still holds an old draft.
	p := composerPage(t, healthComposer, map[string]interface{}{"id": "1"})
	p.inputs = append(p.inputs, "stale draft")
	p.ignoreInput = true
	if _, err := (&ProductHuntBot{}).CommentOnLaunch(context.Background(), p, healthLaunch, "hi"); err == nil || !strings.Contains(err.Error(), "not submitting") {
		t.Errorf("err = %v", err)
	}
	if p.clickCount() != 0 {
		t.Error("submitted text that did not take")
	}
}

func TestHealthBasics(t *testing.T) {
	b := &ProductHuntBot{}
	p := newHealthPage(t)
	if ok, err := b.IsLoggedIn(p); ok || err != nil {
		t.Errorf("logged out = %v, %v", ok, err)
	}
	p.has["[aria-label='User menu']"] = true
	if ok, _ := b.IsLoggedIn(p); !ok {
		t.Error("user menu: want logged in")
	}
	for in, want := range map[string]string{
		"/posts/x":                      "https://www.producthunt.com/posts/x",
		"posts/x":                       "https://www.producthunt.com/posts/x",
		"https://www.producthunt.com/a": "https://www.producthunt.com/a",
	} {
		if got := b.ResolveURL(in); got != want {
			t.Errorf("ResolveURL(%q) = %q", in, got)
		}
	}
	if b.ExtractUsername("https://www.producthunt.com/@x") != "" || b.SearchURL("x") != "https://www.producthunt.com/" || b.LoginURL() == "" {
		t.Error("basics")
	}
	if err := b.SendMessage(context.Background(), p, "x", "y"); err == nil {
		t.Error("SendMessage: want not supported")
	}
	if _, err := b.GetProfileData(context.Background(), p); err == nil {
		t.Error("GetProfileData: want not implemented")
	}
	if _, ok := b.GetMethodByName("nope"); ok {
		t.Error("unknown method resolved")
	}
	for _, name := range []string{"comment_on_launch", "get_launch_metrics"} {
		fn, _ := b.GetMethodByName(name)
		if _, err := fn(context.Background()); err == nil {
			t.Errorf("%s without a page: want error", name)
		}
	}
	if needle(strings.Repeat("ab ", 60)) != strings.Repeat("ab", 40) {
		t.Error("needle is capped at 80 runes without whitespace")
	}
}
