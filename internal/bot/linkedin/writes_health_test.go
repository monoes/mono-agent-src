//go:build !nosocial

package linkedin

// Offline tests of the LinkedIn write paths (react to a post, like a
// comment, comment/reply, send a message, reply in a thread): state
// detection, refusals, verification and their error paths. Clicks go
// through the DOM-click fallback of clickMarked (the fake has no CDP and
// no element handles); typing through the execCommand fallback.

import (
	"context"
	"strings"
	"sync"
	"testing"
)

const (
	hsMenuReaction = "const menus = 'div.reactions-menu"
	hsHasTrigger   = "(tok) => !!L.marked(tok)"
	hsHover        = "'pointerover', 'pointerenter'"
	hsCommentBox   = "no_comment_button"
	hsCommentSub   = "comments-comment-box__submit-button"
	hsPosted       = "const fresh = arts.find"
	hsCommentIDs   = "('#' + i))"
	hsFocusEd      = "const b = L.activeDeep();"
	hsExecInsert   = "document.execCommand('insertText'"
	hsEditorText   = "L.editorText(el) : ''"
	hsEntry        = "/connect|invite|follow|pending|withdraw/i"
	hsMenuItem     = ".artdeco-dropdown__content"
	hsComposerMsg  = "div.msg-form__contenteditable"
	hsSendBtn      = "msg-form__send-button"
	hsSent         = "present: !!ed"
	hsThreadName   = ".msg-entity-lockup__entity-title, .msg-thread__link-to-profile"
)

// onSeq answers the i-th call with vs[i], repeating the last value.
func (p *healthPage) onSeq(sub string, vs ...interface{}) *healthPage {
	return p.on(sub, func(c healthCall) interface{} {
		if c.N < len(vs) {
			return vs[c.N]
		}
		return vs[len(vs)-1]
	})
}

var healthBox = map[string]interface{}{"X": 1, "Y": 1, "W": 20, "H": 10, "Hit": true}

// clickable scripts the DOM-click fallback every clickMarked runs.
func (p *healthPage) clickable() *healthPage {
	return p.onValue(hsMarkedBox, healthBox).onValue(hsDOMClick, true)
}

// typing scripts typeMarked: the editor takes focus and execCommand text
// shows up in the editor (echoed by editorText, "" before anything was
// typed).
func (p *healthPage) typing() *healthPage {
	var mu sync.Mutex
	typed := ""
	return p.onValue(hsFocusEd, map[string]interface{}{"focused": true}).
		on(hsExecInsert, func(c healthCall) interface{} {
			mu.Lock()
			defer mu.Unlock()
			typed, _ = c.Args[1].(string)
			return true
		}).
		on(hsEditorText, func(healthCall) interface{} {
			mu.Lock()
			defer mu.Unlock()
			return typed
		})
}

func postPage(t *testing.T) *healthPage {
	return newHealthPage(t).onValue(hsUnmark, true).onValue(hsWaitOK, healthOK).clickable()
}

func react(state, current string) map[string]interface{} {
	return map[string]interface{}{"state": state, "count": 1, "current": current, "label": "React Like"}
}

// --- react to a post -------------------------------------------------------------

func TestHealthLikePost(t *testing.T) {
	healthTimings(t)
	b := &LinkedInBot{}
	ctx := context.Background()

	p := postPage(t).onValue(hsFindReact, react("ready", "")).onSeq(hsMarkedReact, react("ready", ""), react("reacted", "Like"))
	res, err := b.LikePost(ctx, p, healthPost, "")
	if err != nil || res["reaction"] != "like" || res["already_reacted"] != false {
		t.Errorf("like = %v, %v", res, err)
	}
	if p.hitCount(hsDOMClick) != 1 {
		t.Errorf("clicks = %d, want 1", p.hitCount(hsDOMClick))
	}

	p = postPage(t).onValue(hsFindReact, react("reacted", "Celebrate"))
	if res, err = b.LikePost(ctx, p, healthPost, "LIKE"); err != nil || res["already_reacted"] != true || res["current_reaction"] != "celebrate" {
		t.Errorf("already = %v, %v", res, err)
	}
	if p.hitCount(hsDOMClick) != 0 {
		t.Error("clicked an already reacted post")
	}

	// Celebrate through the menu trigger.
	p = postPage(t).onValue(hsFindReact, react("ready", "")).onValue(hsHasTrigger, true).
		onValue(hsMenuReaction, map[string]interface{}{"state": "ready", "count": 1}).
		onValue(hsMarkedReact, react("reacted", "Celebrate"))
	if res, err = b.LikePost(ctx, p, healthPost, "celebrate"); err != nil || res["reaction"] != "celebrate" {
		t.Errorf("celebrate = %v, %v", res, err)
	}
	if p.hitCount(hsDOMClick) != 2 || p.hitCount(hsHover) != 0 {
		t.Errorf("clicks %d, hovers %d; want 2 (menu trigger, reaction) and 0", p.hitCount(hsDOMClick), p.hitCount(hsHover))
	}

	// Love through a hover when there is no trigger.
	p = postPage(t).onValue(hsFindReact, react("ready", "")).onValue(hsHasTrigger, false).onValue(hsHover, true).
		onValue(hsMenuReaction, map[string]interface{}{"state": "ready", "count": 1}).
		onValue(hsMarkedReact, react("reacted", ""))
	if _, err = b.LikePost(ctx, p, healthPost, "love"); err != nil {
		t.Errorf("love: %v", err)
	}
	if p.hitCount(hsHover) != 1 {
		t.Errorf("hovers = %d, want 1", p.hitCount(hsHover))
	}
}

func TestHealthLikePostFailures(t *testing.T) {
	healthTimings(t)
	b := &LinkedInBot{}
	ctx := context.Background()
	if _, err := b.LikePost(ctx, newHealthPage(t), " ", "like"); err == nil {
		t.Error("no URL: want error")
	}
	if _, err := b.LikePost(ctx, newHealthPage(t), healthPost, "angry"); err == nil || !strings.Contains(err.Error(), "unknown reaction") {
		t.Errorf("bad reaction: err = %v", err)
	}
	for _, st := range []string{"not_found", "ambiguous"} {
		p := postPage(t).onValue(hsFindReact, map[string]interface{}{"state": st, "count": 2})
		if _, err := b.LikePost(ctx, p, healthPost, "like"); err == nil || !strings.Contains(err.Error(), "could not find the post's reaction button") {
			t.Errorf("%s: err = %v", st, err)
		}
		if p.hitCount(hsDOMClick) != 0 {
			t.Errorf("%s: clicked", st)
		}
	}
	p := postPage(t).onValue(hsFindReact, react("ready", "")).onValue(hsMarkedReact, react("ready", ""))
	if _, err := b.LikePost(ctx, p, healthPost, "like"); err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Errorf("unconfirmed: err = %v", err)
	}
	p = postPage(t).onValue(hsFindReact, react("ready", "")).onValue(hsMarkedReact, map[string]interface{}{"state": "not_found"})
	if _, err := b.LikePost(ctx, p, healthPost, "like"); err == nil || !strings.Contains(err.Error(), "disappeared") {
		t.Errorf("button gone: err = %v", err)
	}
	// Asked for Celebrate, the post shows Like: never accepted as done.
	p = postPage(t).onValue(hsFindReact, react("ready", "")).onValue(hsHasTrigger, true).
		onValue(hsMenuReaction, map[string]interface{}{"state": "ready"}).onValue(hsMarkedReact, react("reacted", "Like"))
	if _, err := b.LikePost(ctx, p, healthPost, "celebrate"); err == nil || !strings.Contains(err.Error(), `now shows "Like"`) {
		t.Errorf("wrong reaction: err = %v", err)
	}
	// The element has no box: the click fails.
	p = newHealthPage(t).onValue(hsUnmark, true).onValue(hsWaitOK, healthOK).
		onValue(hsMarkedBox, map[string]interface{}{"W": 0, "H": 0}).onValue(hsFindReact, react("ready", ""))
	if _, err := b.LikePost(ctx, p, healthPost, "like"); err == nil || !strings.Contains(err.Error(), "failed to click Like") {
		t.Errorf("no box: err = %v", err)
	}
	p = newHealthPage(t).onValue(hsWaitOK, map[string]interface{}{"ok": false})
	if _, err := b.LikePost(ctx, p, healthPost, "like"); err == nil || !strings.Contains(err.Error(), "no post rendered") {
		t.Errorf("no post: err = %v", err)
	}
}

func TestHealthLikePostReactionMissingFromMenu(t *testing.T) {
	healthTimings(t)
	// The reactions menu never shows the requested reaction (3s poll): no
	// substitute is clicked.
	p := postPage(t).onValue(hsFindReact, react("ready", "")).onValue(hsHasTrigger, false).onValue(hsHover, true).
		onValue(hsMenuReaction, map[string]interface{}{"state": "not_found"})
	if _, err := (&LinkedInBot{}).LikePost(context.Background(), p, healthPost, "funny"); err == nil || !strings.Contains(err.Error(), "funny reaction not found") {
		t.Errorf("err = %v", err)
	}
	if p.hitCount(hsDOMClick) != 0 {
		t.Error("clicked a substitute reaction")
	}
}

// --- like a comment ----------------------------------------------------------------

const healthCommentID = "urn:li:comment:(activity:7123456789012345678,1)"

func TestHealthLikeComment(t *testing.T) {
	healthTimings(t)
	b := &LinkedInBot{}
	ctx := context.Background()
	p := postPage(t).onValue(hsCommentLike, react("ready", "")).onValue(hsMarkedReact, react("reacted", "Like"))
	fn, _ := b.GetMethodByName("like_comment")
	res, err := fn(ctx, p, healthPost, healthCommentID)
	if err != nil || res.(map[string]interface{})["already_liked"] != false || p.hitCount(hsDOMClick) != 1 {
		t.Errorf("like = %v, %v", res, err)
	}

	p = postPage(t).onValue(hsCommentLike, react("reacted", "Like"))
	if res, err := b.LikeComment(ctx, p, healthPost, healthCommentID); err != nil || res["already_liked"] != true || p.hitCount(hsDOMClick) != 0 {
		t.Errorf("already = %v, %v", res, err)
	}

	// Not rendered at first: comments are expanded, then it is found.
	p = postPage(t).onValue(hsCommentCount, 2).onValue(hsLoadMore, false).
		on(hsCommentLike, func(c healthCall) interface{} {
			if p.hitCount(hsLoadMore) == 0 {
				return map[string]interface{}{"state": "no_comment"}
			}
			return react("reacted", "Like")
		})
	if res, err := b.LikeComment(ctx, p, healthPost, healthCommentID); err != nil || res["already_liked"] != true {
		t.Errorf("after expanding = %v, %v", res, err)
	}

	p = postPage(t).onValue(hsCommentCount, 2).onValue(hsLoadMore, false).onValue(hsCommentLike, map[string]interface{}{"state": "no_comment"})
	if _, err := b.LikeComment(ctx, p, healthPost, healthCommentID); err == nil || !strings.Contains(err.Error(), "not found on") {
		t.Errorf("missing comment: err = %v", err)
	}
	p = postPage(t).onValue(hsCommentLike, map[string]interface{}{"state": "ambiguous", "count": 2})
	if _, err := b.LikeComment(ctx, p, healthPost, healthCommentID); err == nil || !strings.Contains(err.Error(), "could not find the Like button") {
		t.Errorf("ambiguous: err = %v", err)
	}
	p = postPage(t).onValue(hsCommentLike, react("ready", "")).onValue(hsMarkedReact, react("ready", ""))
	if _, err := b.LikeComment(ctx, p, healthPost, healthCommentID); err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Errorf("unconfirmed: err = %v", err)
	}
	if _, err := b.LikeComment(ctx, p, healthPost, " "); err == nil {
		t.Error("no comment id: want error")
	}
	if _, err := b.LikeComment(ctx, p, "", healthCommentID); err == nil {
		t.Error("no post: want error")
	}
}

// --- comment / reply -------------------------------------------------------------------

func commentPage(t *testing.T, box ...interface{}) *healthPage {
	return postPage(t).typing().
		onSeq(hsCommentBox, box...).
		onValue(hsCommentIDs, []interface{}{"urn:li:comment:(a,1)"}).
		onValue(hsCommentSub, map[string]interface{}{"state": "ready"})
}

func TestHealthCommentOnPost(t *testing.T) {
	healthTimings(t)
	b := &LinkedInBot{}
	ctx := context.Background()
	p := commentPage(t, map[string]interface{}{"state": "closed"}, map[string]interface{}{"state": "open"}).
		onSeq(hsPosted, map[string]interface{}{"ok": false}, map[string]interface{}{"ok": true, "id": "urn:li:comment:(a,9)"})
	fn, _ := b.GetMethodByName("comment_on_post")
	res, err := fn(ctx, p, healthPost, "Nice work!")
	if err != nil {
		t.Fatal(err)
	}
	if m := res.(map[string]interface{}); m["comment_id"] != "urn:li:comment:(a,9)" || m["parent_comment_id"] != "" {
		t.Errorf("res = %v", m)
	}
	// Open the box, then submit.
	if p.hitCount(hsDOMClick) < 2 {
		t.Errorf("clicks = %d, want the Comment button, the editor focus and Submit", p.hitCount(hsDOMClick))
	}

	// A reply: the comment's reply box renders a moment later.
	p = commentPage(t, map[string]interface{}{"state": "no_comment"}, map[string]interface{}{"state": "open"}).
		onValue(hsCommentCount, 3).onValue(hsLoadMore, false).
		onValue(hsPosted, map[string]interface{}{"ok": true, "id": "r1"})
	if res, err := b.CommentOnPost(ctx, p, healthPost, "Agreed", healthCommentID); err != nil || res["parent_comment_id"] != healthCommentID {
		t.Errorf("reply = %v, %v", res, err)
	}
}

func TestHealthCommentOnPostFailures(t *testing.T) {
	healthTimings(t)
	b := &LinkedInBot{}
	ctx := context.Background()
	if _, err := b.CommentOnPost(ctx, newHealthPage(t), "", "x", ""); err == nil {
		t.Error("no URL: want error")
	}
	if _, err := b.CommentOnPost(ctx, newHealthPage(t), healthPost, " ", ""); err == nil {
		t.Error("no text: want error")
	}
	p := commentPage(t, map[string]interface{}{"state": "no_comment"}).onValue(hsCommentCount, 0).onValue(hsLoadMore, false)
	if _, err := b.CommentOnPost(ctx, p, healthPost, "x", "urn:li:comment:(a,404)"); err == nil || !strings.Contains(err.Error(), "not found on") {
		t.Errorf("missing parent: err = %v", err)
	}
	for _, st := range []string{"ambiguous", "no_comment_button", "no_reply_button"} {
		p = commentPage(t, map[string]interface{}{"state": st, "count": 2})
		if _, err := b.CommentOnPost(ctx, p, healthPost, "x", ""); err == nil || !strings.Contains(err.Error(), "could not find the comment box") {
			t.Errorf("%s: err = %v", st, err)
		}
	}
	// Typing does not take.
	p = commentPage(t, map[string]interface{}{"state": "open"})
	p.rules = append([]healthRule{{hsEditorText, func(healthCall) interface{} { return "" }}}, p.rules...)
	if _, err := b.CommentOnPost(ctx, p, healthPost, "x", ""); err == nil || !strings.Contains(err.Error(), "typing the comment failed") {
		t.Errorf("typing ignored: err = %v", err)
	}
	// The submit button stays disabled (3s poll).
	p = commentPage(t, map[string]interface{}{"state": "open"})
	p.rules = append([]healthRule{{hsCommentSub, func(healthCall) interface{} { return map[string]interface{}{"state": "disabled"} }}}, p.rules...)
	if _, err := b.CommentOnPost(ctx, p, healthPost, "x", ""); err == nil || !strings.Contains(err.Error(), "submit button not usable (disabled)") {
		t.Errorf("disabled submit: err = %v", err)
	}
	// Submitted but never shows up.
	p = commentPage(t, map[string]interface{}{"state": "open"}).onValue(hsPosted, map[string]interface{}{"ok": false})
	if _, err := b.CommentOnPost(ctx, p, healthPost, "x", ""); err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Errorf("unconfirmed: err = %v", err)
	}
}

// --- messages -------------------------------------------------------------------------

func messagePage(t *testing.T, entry map[string]interface{}) *healthPage {
	return newHealthPage(t).onValue(hsUnmark, true).clickable().typing().
		onValue(hsEntry, entry).
		onValue(hsComposerMsg, map[string]interface{}{"ok": true, "state": "ready", "count": 1}).
		onValue(hsSendBtn, map[string]interface{}{"state": "ready"}).
		onValue(hsSent, map[string]interface{}{"composer": "", "bubbles": []interface{}{}, "present": true})
}

func TestHealthSendMessageTo(t *testing.T) {
	healthTimings(t)
	b := &LinkedInBot{}
	ctx := context.Background()

	p := messagePage(t, map[string]interface{}{"state": "button", "name": "Ann Lee"})
	res, err := b.SendMessageTo(ctx, p, "ann-lee-test", "Hi Ann")
	if err != nil || res["name"] != "Ann Lee" || res["recipient"] != "https://www.linkedin.com/in/ann-lee-test/" {
		t.Errorf("button = %v, %v", res, err)
	}

	p = messagePage(t, map[string]interface{}{"state": "link", "href": "/messaging/compose/?recipient=x", "name": "Ann Lee"})
	if _, err = b.SendMessageTo(ctx, p, "https://www.linkedin.com/in/ann-lee-test/", "Hi"); err != nil {
		t.Fatal(err)
	}
	if navs := p.navigations(); len(navs) != 2 || navs[1] != "https://www.linkedin.com/messaging/compose/?recipient=x" {
		t.Errorf("navigations = %v", navs)
	}

	// Message hides in the More menu.
	for item, wantNavs := range map[string]int{"button": 1, "link": 2} {
		p = messagePage(t, map[string]interface{}{"state": "more", "name": "Ann Lee"})
		menu := map[string]interface{}{"state": item, "href": "https://www.linkedin.com/messaging/thread/2-X/"}
		// The menu-item script shares the entry script's "bad" pattern: its rule goes first.
		p.rules = append([]healthRule{{hsMenuItem, func(healthCall) interface{} { return menu }}}, p.rules...)
		if _, err = b.SendMessageTo(ctx, p, "ann-lee-test", "Hi"); err != nil {
			t.Errorf("more/%s: %v", item, err)
		}
		if n := len(p.navigations()); n != wantNavs {
			t.Errorf("more/%s: %d navigations, want %d", item, n, wantNavs)
		}
	}
}

func TestHealthSendMessageToRefusals(t *testing.T) {
	healthTimings(t)
	b := &LinkedInBot{}
	ctx := context.Background()
	if _, err := b.SendMessageTo(ctx, newHealthPage(t), " ", "x"); err == nil {
		t.Error("no recipient: want error")
	}
	if _, err := b.SendMessageTo(ctx, newHealthPage(t), "x", " "); err == nil {
		t.Error("no message: want error")
	}
	// No Message option: never falls back to Connect.
	p := messagePage(t, map[string]interface{}{"state": "none", "name": "Bo"})
	if _, err := b.SendMessageTo(ctx, p, "bo", "Hi"); err == nil || !strings.Contains(err.Error(), "no Message option") {
		t.Errorf("no option: err = %v", err)
	}
	if p.hitCount(hsDOMClick) != 0 || p.hitCount(hsExecInsert) != 0 {
		t.Error("clicked or typed without a Message option")
	}
	p = messagePage(t, map[string]interface{}{"state": "more", "name": "Bo"})
	p.rules = append([]healthRule{{hsMenuItem, func(healthCall) interface{} { return map[string]interface{}{"state": "none"} }}}, p.rules...)
	if _, err := b.SendMessageTo(ctx, p, "bo", "Hi"); err == nil || !strings.Contains(err.Error(), "no Message option") {
		t.Errorf("empty More menu: err = %v", err)
	}
	// A draft is already in the composer.
	p = messagePage(t, map[string]interface{}{"state": "button", "name": "Bo"})
	p.rules = append([]healthRule{{hsEditorText, func(healthCall) interface{} { return "half-written" }}}, p.rules...)
	if _, err := b.SendMessageTo(ctx, p, "bo", "Hi"); err == nil || !strings.Contains(err.Error(), "already holds a draft") {
		t.Errorf("draft: err = %v", err)
	}
	// Several chats open, none clearly the recipient's.
	p = messagePage(t, map[string]interface{}{"state": "button", "name": "Bo"})
	p.rules = append([]healthRule{{hsComposerMsg, func(healthCall) interface{} {
		return map[string]interface{}{"ok": false, "state": "ambiguous", "count": 3}
	}}}, p.rules...)
	if _, err := b.SendMessageTo(ctx, p, "bo", "Hi"); err == nil || !strings.Contains(err.Error(), "3 conversations are open") {
		t.Errorf("ambiguous: err = %v", err)
	}
	// No composer at all.
	p = messagePage(t, map[string]interface{}{"state": "button", "name": "Bo"})
	p.rules = append([]healthRule{{hsComposerMsg, func(healthCall) interface{} {
		return map[string]interface{}{"ok": false, "state": "none"}
	}}}, p.rules...)
	if _, err := b.SendMessageTo(ctx, p, "bo", "Hi"); err == nil || !strings.Contains(err.Error(), "message composer not found") {
		t.Errorf("no composer: err = %v", err)
	}
	// Send never verified.
	p = messagePage(t, map[string]interface{}{"state": "button", "name": "Bo"})
	p.rules = append([]healthRule{{hsSent, func(healthCall) interface{} {
		return map[string]interface{}{"composer": "Hi", "bubbles": []interface{}{"older"}, "present": true}
	}}}, p.rules...)
	if _, err := b.SendMessageTo(ctx, p, "bo", "Hi"); err == nil || !strings.Contains(err.Error(), "send could not be verified") {
		t.Errorf("unverified: err = %v", err)
	}
	// Send button disabled (3s poll), no Jev.
	p = messagePage(t, map[string]interface{}{"state": "button", "name": "Bo"})
	p.rules = append([]healthRule{{hsSendBtn, func(healthCall) interface{} { return map[string]interface{}{"state": "disabled"} }}}, p.rules...)
	if _, err := b.SendMessageTo(ctx, p, "bo", "Hi"); err == nil || !strings.Contains(err.Error(), "Send button not usable (disabled)") {
		t.Errorf("disabled Send: err = %v", err)
	}
}

func TestHealthReplyToConversationAndThreadTarget(t *testing.T) {
	healthTimings(t)
	b := &LinkedInBot{}
	ctx := context.Background()
	const thread = "https://www.linkedin.com/messaging/thread/2-AAA/"
	p := messagePage(t, nil).onValue(hsThreadName, "Ann Lee")
	// The composer vanished after sending: verified by the bubble.
	p.rules = append([]healthRule{{hsSent, func(healthCall) interface{} {
		return map[string]interface{}{"present": false, "bubbles": []interface{}{"See you then"}}
	}}}, p.rules...)
	res, err := b.SendMessageTo(ctx, p, thread, "See you   then")
	if err != nil || res["recipient"] != thread {
		t.Errorf("thread target = %v, %v", res, err)
	}
	if navs := p.navigations(); len(navs) != 1 || navs[0] != thread {
		t.Errorf("navigations = %v", navs)
	}
	if err := b.ReplyToConversation(ctx, p, "https://www.linkedin.com/in/x/", "hi"); err == nil {
		t.Error("not a thread URL: want error")
	}
	if err := b.ReplyToConversation(ctx, p, thread, " "); err == nil {
		t.Error("no message: want error")
	}
	if err := b.SendMessage(ctx, p, "", "hi"); err == nil {
		t.Error("SendMessage without a recipient: want error")
	}
}
