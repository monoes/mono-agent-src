//go:build !nosocial

package tiktok

// Offline tests of the TikTok write paths: like video/comment, follow,
// comment, share/stitch/duet, direct messages and publish — state detection,
// refusals to guess, verification and error paths.

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const healthVideo = "https://www.tiktok.com/@synth.ada/video/111"

func pressed(v interface{}) map[string]interface{} {
	return map[string]interface{}{"count": 1, "pressed": v}
}

// --- like video -----------------------------------------------------------------------

func TestHealthLikeVideo(t *testing.T) {
	healthFast(t)
	ctx := context.Background()
	b := &TikTokBot{}

	p := newHealthPage(t).scriptOpen().onValue(hsFindVideoLike, pressed(false)).onSeq(hsReadPressed, pressed(false), pressed(true))
	res, err := b.LikeVideo(ctx, p, "/@synth.ada/video/111")
	if err != nil || res["status"] != "liked" || res["videoURL"] != healthVideo || p.clickCount() != 1 {
		t.Errorf("like = %v, %v, %d clicks", res, err, p.clickCount())
	}

	p = newHealthPage(t).scriptOpen().onValue(hsFindVideoLike, pressed(true))
	if res, err = b.LikeVideo(ctx, p, healthVideo); err != nil || res["status"] != "already_liked" || p.clickCount() != 0 {
		t.Errorf("already = %v, %v, %d clicks", res, err, p.clickCount())
	}

	for name, c := range map[string]struct {
		find interface{}
		want string
	}{
		"none":      {map[string]interface{}{"count": 0}, "like button not found"},
		"ambiguous": {map[string]interface{}{"count": 2}, "2 candidate like buttons"},
		"no state":  {pressed(nil), "refusing to click a toggle"},
		"script":    {errors.New("threw"), "find like button"},
	} {
		p = newHealthPage(t).scriptOpen().onValue(hsFindVideoLike, c.find)
		if _, err = b.LikeVideo(ctx, p, healthVideo); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v", name, err)
		}
		if p.clickCount() != 0 {
			t.Errorf("%s: clicked", name)
		}
	}

	p = newHealthPage(t).scriptOpen().onValue(hsFindVideoLike, pressed(false)).onValue(hsReadPressed, pressed(false))
	if _, err = b.LikeVideo(ctx, p, healthVideo); err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Errorf("unconfirmed: err = %v", err)
	}
	p = newHealthPage(t).scriptOpen().onValue(hsFindVideoLike, pressed(false))
	p.elemErr = errors.New("gone")
	if _, err = b.LikeVideo(ctx, p, healthVideo); err == nil || !strings.Contains(err.Error(), "click like") {
		t.Errorf("vanished: err = %v", err)
	}
	if _, err = b.LikeVideo(ctx, p, " "); err == nil {
		t.Error("no URL: want error")
	}
}

// --- like comment ---------------------------------------------------------------------

func commentPanel(t *testing.T) *healthPage {
	return newHealthPage(t).scriptOpen().onValue(hsInspectCmts, map[string]interface{}{"open": true})
}

func TestHealthLikeCommentFindsAfterScrolling(t *testing.T) {
	healthFast(t)
	p := commentPanel(t).
		onSeq(hsFindComment, map[string]interface{}{"count": 0}, map[string]interface{}{"count": 0}, pressed(false)).
		onSeq(hsCountComments, 10, 20).
		onValue(hsScrollCmts, true).
		onSeq(hsReadPressed, pressed(true))
	fn, _ := (&TikTokBot{}).GetMethodByName("like_comment")
	res, err := fn(context.Background(), p, healthVideo, "", "@c.alice", "Love   this")
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]interface{})
	if m["status"] != "liked" || m["text"] != "Love this" || m["username"] != "@c.alice" {
		t.Errorf("res = %v", m)
	}
	if got := p.count(hsScrollCmts); got != 2 {
		t.Errorf("scrolled %d times, want 2", got)
	}
}

func TestHealthLikeCommentRefusals(t *testing.T) {
	healthFast(t)
	ctx := context.Background()
	b := &TikTokBot{}
	if _, err := b.LikeComment(ctx, newHealthPage(t), healthVideo, "", "u", "  "); err == nil || !strings.Contains(err.Error(), "needs the comment's text") {
		t.Errorf("no text/id: err = %v", err)
	}
	for name, c := range map[string]struct {
		find interface{}
		want string
	}{
		"ambiguous": {map[string]interface{}{"count": 2}, "refusing to guess"},
		"no button": {map[string]interface{}{"count": 1, "state": "no_button"}, "like button of comment"},
		"no state":  {map[string]interface{}{"count": 1}, "cannot tell whether"},
		"script":    {errors.New("threw"), "find comment"},
	} {
		p := commentPanel(t).onValue(hsFindComment, c.find)
		if _, err := b.LikeComment(ctx, p, healthVideo, "", "", "nice"); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v", name, err)
		}
		if p.clickCount() != 0 {
			t.Errorf("%s: clicked", name)
		}
	}
	// Never found: the list stops growing after three scrolls.
	p := commentPanel(t).onValue(hsFindComment, map[string]interface{}{"count": 0}).onValue(hsCountComments, 5).onValue(hsScrollCmts, true)
	if _, err := b.LikeComment(ctx, p, healthVideo, "7300000000001", "", ""); err == nil || !strings.Contains(err.Error(), "comment 7300000000001 not found") {
		t.Errorf("not found: err = %v", err)
	}
	if got := p.count(hsCountComments); got != 4 {
		t.Errorf("counted %d times, want 4 (first + 3 stale)", got)
	}
	p = commentPanel(t).onValue(hsFindComment, map[string]interface{}{"count": 0}).onValue(hsCountComments, errors.New("threw"))
	if _, err := b.LikeComment(ctx, p, healthVideo, "", "", "x"); err == nil || !strings.Contains(err.Error(), "count comments") {
		t.Errorf("count: err = %v", err)
	}
	// Already liked: no click.
	p = commentPanel(t).onValue(hsFindComment, pressed(true))
	if res, err := b.LikeComment(ctx, p, healthVideo, "", "c.bob", "So good"); err != nil || res["status"] != "already_liked" || p.clickCount() != 0 {
		t.Errorf("already = %v, %v", res, err)
	}
	// Unconfirmed.
	p = commentPanel(t).onValue(hsFindComment, pressed(false)).onValue(hsReadPressed, pressed(false))
	if _, err := b.LikeComment(ctx, p, healthVideo, "", "", "x"); err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Errorf("unconfirmed: err = %v", err)
	}
	// Comments closed.
	p = newHealthPage(t).scriptOpen().onValue(hsInspectCmts, map[string]interface{}{"open": false, "icon": false})
	if _, err := b.LikeComment(ctx, p, healthVideo, "", "", "x"); err == nil || !strings.Contains(err.Error(), "comment button not found") {
		t.Errorf("closed: err = %v", err)
	}
}

// --- follow ----------------------------------------------------------------------------------

func TestHealthFollowUser(t *testing.T) {
	healthFast(t)
	ctx := context.Background()
	b := &TikTokBot{}
	for state, want := range map[string]string{"following": "already_following", "requested": "already_requested"} {
		p := newHealthPage(t).scriptOpen().onValue(hsFindFollow, map[string]interface{}{"count": 1, "state": state})
		res, err := b.FollowUser(ctx, p, "@synth.ada")
		if err != nil || res["status"] != want || p.clickCount() != 0 {
			t.Errorf("%s = %v, %v", state, res, err)
		}
	}
	for after, want := range map[string]string{"following": "followed", "requested": "requested"} {
		p := newHealthPage(t).scriptOpen().
			onValue(hsFindFollow, map[string]interface{}{"count": 1, "state": "not_following"}).
			onSeq(hsReadFollow, map[string]interface{}{"count": 1, "state": "not_following"}, map[string]interface{}{"count": 1, "state": after})
		res, err := b.FollowUser(ctx, p, "https://www.tiktok.com/@synth.ada")
		if err != nil || res["status"] != want || res["username"] != "synth.ada" || p.clickCount() != 1 {
			t.Errorf("-> %s = %v, %v", after, res, err)
		}
	}
	for name, c := range map[string]struct {
		find interface{}
		want string
	}{
		"none":      {map[string]interface{}{"count": 0}, "follow button not found"},
		"ambiguous": {map[string]interface{}{"count": 3}, "3 candidate follow buttons"},
		"unknown":   {map[string]interface{}{"count": 1, "state": "unknown", "text": "Edit profile"}, "refusing to click a toggle"},
		"script":    {errors.New("threw"), "find follow button"},
	} {
		p := newHealthPage(t).scriptOpen().onValue(hsFindFollow, c.find)
		if _, err := b.FollowUser(ctx, p, "synth.ada"); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v", name, err)
		}
		if p.clickCount() != 0 {
			t.Errorf("%s: clicked", name)
		}
	}
	p := newHealthPage(t).scriptOpen().
		onValue(hsFindFollow, map[string]interface{}{"count": 1, "state": "not_following"}).
		onValue(hsReadFollow, map[string]interface{}{"count": 1, "state": "not_following"})
	if _, err := b.FollowUser(ctx, p, "synth.ada"); err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Errorf("unconfirmed: err = %v", err)
	}
	if _, err := b.FollowUser(ctx, p, ""); err == nil {
		t.Error("no target: want error")
	}
}

// --- comment on a video -------------------------------------------------------------------

func commentFlow(t *testing.T, progress ...interface{}) *healthPage {
	return newHealthPage(t).scriptOpen().echoInput().
		onValue(hsFindEditor, map[string]interface{}{"count": 1, "text": ""}).
		onValue(hsFindSubmit, map[string]interface{}{"count": 1, "state": "enabled"}).
		onSeq(hsProgress, progress...)
}

func TestHealthCommentOnVideo(t *testing.T) {
	healthFast(t)
	ctx := context.Background()
	b := &TikTokBot{}
	empty := ""
	p := commentFlow(t,
		map[string]interface{}{"n": 0, "composer": ""},
		map[string]interface{}{"n": 0, "composer": "Great"},
		map[string]interface{}{"n": 1, "composer": "Great"})
	res, err := b.CommentOnVideo(ctx, p, healthVideo, "Great")
	if err != nil || res["verified"] != "comment_rendered" || res["status"] != "commented" {
		t.Errorf("rendered = %v, %v", res, err)
	}
	if len(p.inputs) != 1 || p.inputs[0] != "Great" || p.clickCount() != 1 {
		t.Errorf("inputs %q, clicks %d", p.inputs, p.clickCount())
	}

	p = commentFlow(t, map[string]interface{}{"n": 0, "composer": "x"}, map[string]interface{}{"n": 0, "composer": empty})
	if res, err = b.CommentOnVideo(ctx, p, healthVideo, "Great"); err != nil || res["verified"] != "composer_cleared" {
		t.Errorf("cleared = %v, %v", res, err)
	}

	p = commentFlow(t, map[string]interface{}{"n": 0, "composer": "Great"})
	if _, err = b.CommentOnVideo(ctx, p, healthVideo, "Great"); err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Errorf("unconfirmed: err = %v", err)
	}

	// The comment box only shows after opening the panel.
	p = commentFlow(t, map[string]interface{}{"n": 0}, map[string]interface{}{"n": 1}).
		onValue(hsInspectCmts, map[string]interface{}{"open": false, "icon": true}).onValue(hsPanelOpen, true)
	p.rules = append([]healthRule{{hsFindEditor, func(c healthCall) interface{} {
		if c.N == 0 {
			return map[string]interface{}{"count": 0}
		}
		return map[string]interface{}{"count": 1}
	}}}, p.rules...)
	if res, err = b.CommentOnVideo(ctx, p, healthVideo, "Great"); err != nil || res["verified"] != "comment_rendered" {
		t.Errorf("panel first = %v, %v", res, err)
	}

	// The Post button enables once the text is in.
	p = commentFlow(t, map[string]interface{}{"n": 0}, map[string]interface{}{"n": 1})
	p.rules = append([]healthRule{{hsFindSubmit, func(c healthCall) interface{} {
		if c.N < 2 {
			return map[string]interface{}{"count": 1, "state": "disabled"}
		}
		return map[string]interface{}{"count": 1, "state": "enabled"}
	}}}, p.rules...)
	if _, err = b.CommentOnVideo(ctx, p, healthVideo, "Great"); err != nil {
		t.Errorf("enables late: %v", err)
	}
}

func TestHealthCommentOnVideoRefusals(t *testing.T) {
	healthFast(t)
	ctx := context.Background()
	b := &TikTokBot{}
	if _, err := b.CommentOnVideo(ctx, newHealthPage(t), healthVideo, " "); err == nil {
		t.Error("no text: want error")
	}
	if _, err := b.CommentOnVideo(ctx, newHealthPage(t), "", "x"); err == nil {
		t.Error("no URL: want error")
	}
	p := commentFlow(t, map[string]interface{}{"n": 0})
	p.rules = append([]healthRule{{hsFindEditor, func(healthCall) interface{} { return map[string]interface{}{"count": 2} }}}, p.rules...)
	if _, err := b.CommentOnVideo(ctx, p, healthVideo, "x"); err == nil || !strings.Contains(err.Error(), "2 matching text boxes") {
		t.Errorf("two boxes: err = %v", err)
	}
	p = commentFlow(t, map[string]interface{}{"n": 0})
	p.rules = append([]healthRule{{hsFindSubmit, func(healthCall) interface{} { return map[string]interface{}{"count": 0} }}}, p.rules...)
	if _, err := b.CommentOnVideo(ctx, p, healthVideo, "x"); err == nil || !strings.Contains(err.Error(), "0 matching submit buttons") {
		t.Errorf("no Post: err = %v", err)
	}
	if p.clickCount() != 0 {
		t.Error("clicked without a Post button")
	}
	// Typing does not take.
	p = commentFlow(t, map[string]interface{}{"n": 0})
	p.rules = append([]healthRule{{hsTextOf, func(healthCall) interface{} { return "" }}}, p.rules...)
	if _, err := b.CommentOnVideo(ctx, p, healthVideo, "x"); err == nil || !strings.Contains(err.Error(), "did not appear") {
		t.Errorf("typing ignored: err = %v", err)
	}
	p = commentFlow(t, errors.New("threw"))
	if _, err := b.CommentOnVideo(ctx, p, healthVideo, "x"); err == nil || !strings.Contains(err.Error(), "read comments") {
		t.Errorf("progress script: err = %v", err)
	}
	p = newHealthPage(t).scriptOpen().onValue(hsFindEditor, errors.New("threw"))
	if _, err := b.CommentOnVideo(ctx, p, healthVideo, "x"); err == nil || !strings.Contains(err.Error(), "find comment box") {
		t.Errorf("editor script: err = %v", err)
	}
	p = newHealthPage(t).scriptOpen().onValue(hsFindEditor, map[string]interface{}{"count": 0}).
		onValue(hsInspectCmts, map[string]interface{}{"open": false, "icon": false})
	if _, err := b.CommentOnVideo(ctx, p, healthVideo, "x"); err == nil || !strings.Contains(err.Error(), "comments may be turned off") {
		t.Errorf("comments off: err = %v", err)
	}
}

// --- share / stitch / duet -----------------------------------------------------------------

func TestHealthShareStitchDuet(t *testing.T) {
	healthFast(t)
	ctx := context.Background()
	b := &TikTokBot{}
	option := func(state string) *healthPage {
		return newHealthPage(t).scriptOpen().on(hsFindOption, func(c healthCall) interface{} {
			sels, _ := c.Args[0].([]string)
			if len(sels) > 0 && strings.Contains(sels[0], "share-icon") {
				return map[string]interface{}{"count": 1, "state": "enabled"}
			}
			if state == "" {
				return map[string]interface{}{"count": 0}
			}
			if state == "two" {
				return map[string]interface{}{"count": 2}
			}
			return map[string]interface{}{"count": 1, "state": state}
		})
	}
	type call func(context.Context, *healthPage) (map[string]interface{}, error)
	for name, c := range map[string]struct {
		fn     call
		status string
	}{
		"stitch": {func(ctx context.Context, p *healthPage) (map[string]interface{}, error) {
			return b.StitchVideo(ctx, p, healthVideo)
		}, "opened_stitch_editor"},
		"duet": {func(ctx context.Context, p *healthPage) (map[string]interface{}, error) {
			return b.DuetVideo(ctx, p, healthVideo)
		}, "opened_duet_editor"},
		"share": {func(ctx context.Context, p *healthPage) (map[string]interface{}, error) {
			return b.ShareVideo(ctx, p, healthVideo)
		}, "link_copied"},
	} {
		p := option("enabled")
		res, err := c.fn(ctx, p)
		if err != nil || res["status"] != c.status || res["published"] != false || p.clickCount() != 2 {
			t.Errorf("%s = %v, %v, %d clicks", name, res, err, p.clickCount())
		}
		for state, want := range map[string]string{"disabled": "is disabled", "": "option not found", "two": "2 "} {
			p = option(state)
			if _, err = c.fn(ctx, p); err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("%s/%q: err = %v", name, state, err)
			}
		}
	}
	p := newHealthPage(t).scriptOpen().onValue(hsFindOption, map[string]interface{}{"count": 0})
	if _, err := b.StitchVideo(ctx, p, healthVideo); err == nil || !strings.Contains(err.Error(), "share button not found") {
		t.Errorf("no share button: err = %v", err)
	}
	p = newHealthPage(t).scriptOpen().onValue(hsFindOption, errors.New("threw"))
	if _, err := b.DuetVideo(ctx, p, healthVideo); err == nil || !strings.Contains(err.Error(), "find share button") {
		t.Errorf("script: err = %v", err)
	}
	for _, f := range []func() (map[string]interface{}, error){
		func() (map[string]interface{}, error) { return b.StitchVideo(ctx, p, "") },
		func() (map[string]interface{}, error) { return b.DuetVideo(ctx, p, "") },
		func() (map[string]interface{}, error) { return b.ShareVideo(ctx, p, "") },
	} {
		if _, err := f(); err == nil {
			t.Error("no URL: want error")
		}
	}
}

// --- direct messages ----------------------------------------------------------------------

func chatPage(t *testing.T, states ...interface{}) *healthPage {
	return newHealthPage(t).scriptOpen().echoInput().
		onValue(hsFindOption, map[string]interface{}{"count": 1, "state": "enabled"}).
		onValue(hsFindEditor, map[string]interface{}{"count": 1}).
		onValue(hsFindSubmit, map[string]interface{}{"count": 1, "state": "enabled"}).
		onSeq(hsChatState, states...)
}

func TestHealthSendDM(t *testing.T) {
	healthFast(t)
	ctx := context.Background()
	b := &TikTokBot{}
	p := chatPage(t,
		map[string]interface{}{"uniqueId": "Synth.Ada", "nickname": "Ada", "n": 0, "composer": ""},
		map[string]interface{}{"uniqueId": "Synth.Ada", "nickname": "Ada", "n": 1, "composer": ""})
	res, err := b.SendDM(ctx, p, "@synth.ada", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if res["verified"] != "message_rendered" || res["recipient"] != "Synth.Ada" || res["username"] != "synth.ada" || p.clickCount() != 2 {
		t.Errorf("res = %v, clicks %d", res, p.clickCount())
	}

	// Wrong chat: never types or sends.
	p = chatPage(t, map[string]interface{}{"uniqueId": "someone.else", "n": 0})
	if _, err = b.SendDM(ctx, p, "synth.ada", "hello"); err == nil || !strings.Contains(err.Error(), "not sending") {
		t.Errorf("wrong chat: err = %v", err)
	}
	if len(p.inputs) != 0 {
		t.Errorf("typed %q into the wrong chat", p.inputs)
	}
	// No header to confirm the recipient.
	p = chatPage(t, map[string]interface{}{"n": 0})
	if _, err = b.SendDM(ctx, p, "synth.ada", "hello"); err == nil || !strings.Contains(err.Error(), "read chat") && !strings.Contains(err.Error(), "cannot confirm") {
		t.Errorf("no header: err = %v", err)
	}
	// Disabled messaging.
	p = chatPage(t).onValue(hsFindOption, map[string]interface{}{"count": 1, "state": "disabled"})
	p.rules = append([]healthRule{{hsFindOption, func(healthCall) interface{} { return map[string]interface{}{"count": 1, "state": "disabled"} }}}, p.rules...)
	if _, err = b.SendDM(ctx, p, "synth.ada", "hello"); err == nil || !strings.Contains(err.Error(), "messaging is disabled") {
		t.Errorf("disabled: err = %v", err)
	}
	// No Message button.
	p = newHealthPage(t).scriptOpen().onValue(hsFindOption, map[string]interface{}{"count": 0})
	if _, err = b.SendDM(ctx, p, "synth.ada", "hello"); err == nil || !strings.Contains(err.Error(), "Message button not found") {
		t.Errorf("no button: err = %v", err)
	}
	// Chat never opens.
	p = newHealthPage(t).scriptOpen().onValue(hsFindOption, map[string]interface{}{"count": 1}).onValue(hsFindEditor, map[string]interface{}{"count": 0})
	if _, err = b.SendDM(ctx, p, "synth.ada", "hello"); err == nil || !strings.Contains(err.Error(), "message box not found") {
		t.Errorf("no chat: err = %v", err)
	}
	// Not verified.
	p = chatPage(t, map[string]interface{}{"uniqueId": "synth.ada", "n": 0, "composer": "hello"})
	if _, err = b.SendDM(ctx, p, "synth.ada", "hello"); err == nil || !strings.Contains(err.Error(), "could not be verified") {
		t.Errorf("unverified: err = %v", err)
	}
	if _, err = b.SendDM(ctx, p, "synth.ada", " "); err == nil {
		t.Error("empty message: want error")
	}
	if err = b.SendMessage(ctx, p, "", "x"); err == nil {
		t.Error("no target: want error")
	}
}

func TestHealthSendDMEnterWhenNoSendButton(t *testing.T) {
	healthFast(t)
	empty := ""
	p := chatPage(t,
		map[string]interface{}{"uniqueId": "synth.ada", "n": 0, "composer": "hi"},
		map[string]interface{}{"uniqueId": "synth.ada", "n": 0, "composer": empty})
	p.rules = append([]healthRule{{hsFindSubmit, func(healthCall) interface{} { return map[string]interface{}{"count": 0} }}}, p.rules...)
	res, err := (&TikTokBot{}).SendDM(context.Background(), p, "synth.ada", "hi")
	if err != nil || res["verified"] != "composer_cleared" {
		t.Fatalf("= %v, %v", res, err)
	}
	if len(p.keys) != 1 || p.keys[0] != '\r' {
		t.Errorf("keys = %q, want Enter", p.keys)
	}
}

func TestHealthReplyToConversation(t *testing.T) {
	healthFast(t)
	ctx := context.Background()
	b := &TikTokBot{}
	p := chatPage(t,
		map[string]interface{}{"nickname": "Alice", "n": 0},
		map[string]interface{}{"nickname": "Alice", "n": 1}).onValue(hsFindConv, 1)
	res, err := b.ReplyToConversation(ctx, p, " Alice ", "thanks")
	if err != nil || res["conversation"] != "Alice" || res["nickname"] != "Alice" {
		t.Errorf("reply = %v, %v", res, err)
	}
	for n, want := range map[int]string{0: "no conversation named", 2: "refusing to guess"} {
		p = newHealthPage(t).scriptOpen().onValue(hsFindConv, n)
		if _, err = b.ReplyToConversation(ctx, p, "Alice", "x"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%d hits: err = %v", n, err)
		}
	}
	p = chatPage(t, map[string]interface{}{"nickname": "Mallory", "n": 0}).onValue(hsFindConv, 1)
	if _, err = b.ReplyToConversation(ctx, p, "Alice", "x"); err == nil || !strings.Contains(err.Error(), "not sending") {
		t.Errorf("wrong chat: err = %v", err)
	}
	p = newHealthPage(t)
	p.race = func([]string) (int, error) { return 0, errors.New("timeout") }
	if _, err = b.ReplyToConversation(ctx, p, "Alice", "x"); err == nil || !strings.Contains(err.Error(), "conversation list not found") {
		t.Errorf("no list: err = %v", err)
	}
	if _, err = b.ReplyToConversation(ctx, p, "", "x"); err == nil {
		t.Error("no name: want error")
	}
	if _, err = b.ReplyToConversation(ctx, p, "A", ""); err == nil {
		t.Error("no message: want error")
	}
}

// --- publish ----------------------------------------------------------------------------------

func publishPage(t *testing.T) *healthPage {
	return newHealthPage(t).scriptOpen().echoInput().
		onValue(hsFindEditor, map[string]interface{}{"count": 1}).
		onValue(hsSelectCaption, true).
		onValue(hsFindSubmit, map[string]interface{}{"count": 1, "state": "enabled"})
}

func TestHealthPublishUploadedVideo(t *testing.T) {
	healthFast(t)
	ctx := context.Background()
	b := &TikTokBot{}

	p := publishPage(t)
	p.race = func(sels []string) (int, error) { return 0, nil } // success outcome
	res, err := b.PublishUploadedVideo(ctx, p, "My caption")
	if err != nil || res["verified"] != "success" {
		t.Errorf("success = %v, %v", res, err)
	}
	if len(p.inputs) != 1 || p.inputs[0] != "My caption" {
		t.Errorf("caption typed %q", p.inputs)
	}

	p = publishPage(t)
	p.race = func(sels []string) (int, error) {
		if strings.Contains(sels[0], "post-success") && len(sels) == 2 {
			return 1, nil
		}
		return -1, errors.New("none")
	}
	if _, err = b.PublishUploadedVideo(ctx, p, ""); err == nil || !strings.Contains(err.Error(), "reported an error") {
		t.Errorf("error outcome: err = %v", err)
	}

	p = publishPage(t)
	p.race = func([]string) (int, error) { return -1, errors.New("none") }
	if _, err = b.PublishUploadedVideo(ctx, p, ""); err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Errorf("no outcome: err = %v", err)
	}

	p = publishPage(t)
	p.rules = append([]healthRule{{hsTextOf, func(healthCall) interface{} { return "something else" }}}, p.rules...)
	if _, err = b.PublishUploadedVideo(ctx, p, "My caption"); err == nil || !strings.Contains(err.Error(), "caption did not take") {
		t.Errorf("caption ignored: err = %v", err)
	}
	p = publishPage(t)
	p.rules = append([]healthRule{{hsFindSubmit, func(healthCall) interface{} { return map[string]interface{}{"count": 1, "state": "disabled"} }}}, p.rules...)
	if _, err = b.PublishUploadedVideo(ctx, p, ""); err == nil || !strings.Contains(err.Error(), "never enabled") {
		t.Errorf("disabled Post: err = %v", err)
	}
	p = publishPage(t)
	p.rules = append([]healthRule{{hsFindSubmit, func(healthCall) interface{} { return map[string]interface{}{"count": 0} }}}, p.rules...)
	if _, err = b.PublishUploadedVideo(ctx, p, ""); err == nil || !strings.Contains(err.Error(), "Post button not found") {
		t.Errorf("no Post: err = %v", err)
	}
	p = newHealthPage(t).scriptOpen().onValue(hsFindEditor, map[string]interface{}{"count": 0})
	if _, err = b.PublishUploadedVideo(ctx, p, "x"); err == nil || !strings.Contains(err.Error(), "caption box not found") {
		t.Errorf("no upload: err = %v", err)
	}
	p = publishPage(t).onValue(hsSelectCaption, false)
	p.rules = append([]healthRule{{hsSelectCaption, func(healthCall) interface{} { return false }}}, p.rules...)
	if _, err = b.PublishUploadedVideo(ctx, p, "x"); err == nil || !strings.Contains(err.Error(), "select caption") {
		t.Errorf("select failed: err = %v", err)
	}
}
