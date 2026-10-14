//go:build !nosocial

package instagram

// Offline tests of the Instagram direct-message paths: opening a thread
// (profile button, new-message compose, inbox), sending and verifying.

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const (
	hsDMComposer = "if (c) mark(c, m); return !!c;"
	hsBubbles    = "() => dmBubbles()"
	hsDMValue    = "return el ? boxValue(el) : ''"
	hsSendBtn    = "/^send$/i"
	hsToast      = "() => errorToast()"
	hsMsgBtn     = "profileButton(/^message$/i)"
	hsQuery      = `'input[name="queryBox"]', '[role="dialog"]`
	hsNewMsg     = `svg[aria-label="New message"]`
	hsRecipient  = "!row.querySelector("
	hsChat       = "/^(chat|next)$/i"
	hsInboxRow   = `const rows = Array.from(document.querySelectorAll('a[href*="/direct/t/"]`
)

// lastInput makes boxValue echo what was typed last, like a real input.
func (p *healthPage) lastInput() *healthPage {
	return p.on(hsBoxValue, func(healthCall) interface{} {
		p.mu.Lock()
		defer p.mu.Unlock()
		if len(p.inputs) == 0 {
			return ""
		}
		return p.inputs[len(p.inputs)-1]
	})
}

// dmThread scripts an open conversation whose composer accepts text, has a
// Send button, and renders the sent message as a new bubble.
func (p *healthPage) dmThread(msg string) *healthPage {
	return p.lastInput().
		onValue(hsDMComposer, true).
		onSeq(hsBubbles, []interface{}{"older message"}, []interface{}{"older message", msg}).
		onValue(hsTypeFocus, "").
		onValue(hsSendBtn, true).
		onValue(hsToast, "").
		onValue(hsDMValue, msg)
}

func TestHealthSendMessageViaProfile(t *testing.T) {
	healthFast(t)
	p := newHealthPage(t).scriptOpen().onValue(hsHeader, true).onValue(hsMsgBtn, true).dmThread("hello there")
	if err := New().SendMessage(context.Background(), p, "@synth.ada", "hello there"); err != nil {
		t.Fatal(err)
	}
	if got := p.navigations(); len(got) != 1 || got[0] != "https://www.instagram.com/synth.ada/" {
		t.Errorf("navigations = %v", got)
	}
	// Message button, Send button.
	if p.clickCount() != 2 {
		t.Errorf("clicks = %d, want 2", p.clickCount())
	}
}

func TestHealthSendMessageViaCompose(t *testing.T) {
	healthFast(t)
	p := newHealthPage(t).scriptOpen().
		onValue(hsHeader, true).onValue(hsMsgBtn, false).
		onValue(hsNewMsg, true).
		onValue(hsRecipient, true).
		onSeq(hsChat, map[string]interface{}{"found": true, "disabled": true}, map[string]interface{}{"found": true}).
		dmThread("hi")
	// The search box appears once "New message" was clicked.
	p.rules = append([]healthRule{{hsQuery, func(healthCall) interface{} { return p.clickCount() > 0 }}}, p.rules...)
	fn, _ := New().GetMethodByName("send_message")
	res, err := fn(context.Background(), p, "https://www.instagram.com/synth.ada/", "hi")
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]interface{})
	if m["method"] != "compose" || m["status"] != "sent" || m["username"] != "synth.ada" {
		t.Errorf("res = %v", m)
	}
	if len(p.inputs) != 2 || p.inputs[0] != "synth.ada" || p.inputs[1] != "hi" {
		t.Errorf("typed %q, want the recipient then the message", p.inputs)
	}
	if p.count(hsNewMsg) != 1 || p.count(hsChat) != 2 {
		t.Errorf("new message %d, chat %d; want 1 and 2 (waited for Chat to enable)", p.count(hsNewMsg), p.count(hsChat))
	}
}

func TestHealthSendMessageFailures(t *testing.T) {
	healthFast(t)
	ctx := context.Background()
	b := New()
	if err := b.SendMessage(ctx, newHealthPage(t), " @ ", "x"); err == nil {
		t.Error("empty username: want error")
	}
	if err := b.SendMessage(ctx, newHealthPage(t), "a.b", "  "); err == nil {
		t.Error("empty message: want error")
	}
	// A login wall on the profile does not fall back to compose.
	p := newHealthPage(t).onValue(hsLogin, true)
	if err := b.SendMessage(ctx, p, "a.b", "hi"); !errors.Is(err, ErrNotLoggedIn) {
		t.Errorf("login wall: err = %v", err)
	}
	if n := len(p.navigations()); n != 1 {
		t.Errorf("navigations = %d, want 1", n)
	}
	// Neither route opens a thread.
	p = newHealthPage(t).scriptOpen().onValue(hsHeader, false).onValue(hsQuery, false).onValue(hsNewMsg, false)
	if err := b.SendMessage(ctx, p, "a.b", "hi"); err == nil || !strings.Contains(err.Error(), "could not open a conversation") {
		t.Errorf("no thread: err = %v", err)
	}
	// Instagram's error toast.
	p = newHealthPage(t).scriptOpen().onValue(hsHeader, true).onValue(hsMsgBtn, true).dmThread("hi")
	p.rules = append([]healthRule{{hsToast, func(healthCall) interface{} { return "You can't message this account" }}}, p.rules...)
	if err := b.SendMessage(ctx, p, "a.b", "hi"); err == nil || !strings.Contains(err.Error(), "can't message") {
		t.Errorf("toast: err = %v", err)
	}
	// Nothing confirms the send.
	p = newHealthPage(t).scriptOpen().onValue(hsHeader, true).onValue(hsMsgBtn, true).dmThread("hi")
	p.rules = append([]healthRule{{hsBubbles, func(healthCall) interface{} { return []interface{}{} }}}, p.rules...)
	if err := b.SendMessage(ctx, p, "a.b", "hi"); err == nil || !strings.Contains(err.Error(), "could not be verified") {
		t.Errorf("unverified: err = %v", err)
	}
	// No composer in the thread.
	p = newHealthPage(t).scriptOpen().onValue(hsHeader, true).onValue(hsMsgBtn, true).onValue(hsDMComposer, false).
		onValue(hsQuery, false).onValue(hsNewMsg, false)
	if err := b.SendMessage(ctx, p, "a.b", "hi"); err == nil || !strings.Contains(err.Error(), "message box not found") {
		t.Errorf("no composer: err = %v", err)
	}
}

func TestHealthSendMessageEnterWhenNoSendButton(t *testing.T) {
	healthFast(t)
	p := newHealthPage(t).scriptOpen().onValue(hsHeader, true).onValue(hsMsgBtn, true).onValue(hsEnterOn, true).dmThread("hi")
	p.rules = append([]healthRule{
		{hsSendBtn, func(healthCall) interface{} { return false }},
		{hsBubbles, func(healthCall) interface{} { return []interface{}{} }},
		{hsDMValue, func(healthCall) interface{} { return "" }}, // composer cleared
	}, p.rules...)
	if err := New().SendMessage(context.Background(), p, "a.b", "hi"); err != nil {
		t.Fatal(err)
	}
	if len(p.keys) != 1 || p.keys[0] != '\r' {
		t.Errorf("keys = %q, want Enter", p.keys)
	}
}

func TestHealthReplyToConversation(t *testing.T) {
	healthFast(t)
	ctx := context.Background()
	b := New()

	p := newHealthPage(t).scriptOpen().dmThread("ok!")
	res, err := b.ReplyToConversation(ctx, p, "/direct/t/12345/", "ok!")
	if err != nil || res["url"] != "https://www.instagram.com/direct/t/12345/" || res["status"] != "sent" {
		t.Errorf("by URL = %v, %v", res, err)
	}

	p = newHealthPage(t).scriptOpen().onValue(hsInboxRow, true).dmThread("ok!")
	res, err = b.ReplyToConversation(ctx, p, "@synth.ada", "ok!")
	if err != nil || res["method"] != "inbox" || res["username"] != "synth.ada" {
		t.Errorf("by inbox = %v, %v", res, err)
	}
	if navs := p.navigations(); len(navs) != 1 || navs[0] != "https://www.instagram.com/direct/inbox/" {
		t.Errorf("navigations = %v", navs)
	}

	// Not in the inbox: start a new conversation.
	p = newHealthPage(t).scriptOpen().onValue(hsInboxRow, false).onValue(hsHeader, true).onValue(hsMsgBtn, true).dmThread("ok!")
	if res, err = b.ReplyToConversation(ctx, p, "synth.ada", "ok!"); err != nil || res["method"] != "new_message" {
		t.Errorf("new = %v, %v", res, err)
	}

	if _, err = b.ReplyToConversation(ctx, newHealthPage(t), "synth.ada", " "); err == nil {
		t.Error("empty reply: want error")
	}
	if _, err = b.ReplyToConversation(ctx, newHealthPage(t), "https://www.instagram.com/explore/", "x"); err == nil || !strings.Contains(err.Error(), "neither") {
		t.Errorf("bad target: err = %v", err)
	}
	p = newHealthPage(t).onValue(hsLogin, true)
	if _, err = b.ReplyToConversation(ctx, p, "/direct/t/1/", "x"); !errors.Is(err, ErrNotLoggedIn) {
		t.Errorf("login wall: err = %v", err)
	}
	p = newHealthPage(t).scriptOpen().onValue(hsInboxRow, false).onValue(hsHeader, false).onValue(hsQuery, false).onValue(hsNewMsg, false)
	if _, err = b.ReplyToConversation(ctx, p, "synth.ada", "x"); err == nil || !strings.Contains(err.Error(), "starting one failed") {
		t.Errorf("no inbox thread, no new one: err = %v", err)
	}
}

func TestHealthGetProfileDataAndBasics(t *testing.T) {
	healthFast(t)
	b := New()
	if b.Platform() != "INSTAGRAM" || !strings.Contains(b.LoginURL(), "/accounts/login/") {
		t.Errorf("platform/login = %s %s", b.Platform(), b.LoginURL())
	}
	p := newHealthPage(t).onValue(hsDOM, healthFixture(t, "profile", "dom"))
	p.url = "https://www.instagram.com/synth.cy/"
	res, err := b.GetProfileData(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if res["profile_url"] != p.url || res["username"] != "synth.cy" || res["following_count"] != "1234" {
		t.Errorf("res = %v", res)
	}
	if _, err := b.GetProfileData(context.Background(), nil); err == nil {
		t.Error("nil page: want error")
	}
	p = newHealthPage(t).onValue(hsDOM, healthFixture(t, "profile", "dom_no_header"))
	if _, err := b.GetProfileData(context.Background(), p); err == nil {
		t.Error("no header: want error")
	}
}
