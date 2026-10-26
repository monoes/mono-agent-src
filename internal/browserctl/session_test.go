package browserctl_test

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/browserctl/browserctltest"
	"github.com/monoes/mono-agent/internal/extension"
	"github.com/monoes/mono-agent/internal/testdb"
)

func setEnv(t *testing.T, agent bool) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	for _, k := range browserctl.AgentContextEnv() {
		t.Setenv(k, "")
	}
	for _, k := range []string{"MONOAGENT_CONVERSATION_ID", "MONOAGENT_AGENT_ROLE", "MONOMIND_ORG_NAME", browserctl.EnvCoderTitle} {
		t.Setenv(k, "")
	}
	if agent {
		t.Setenv("CLAUDECODE", "1")
	}
}

var pngBytes = []byte("\x89PNG\r\n\x1a\nfake")

func withScreenshot(f *browserctltest.Fake) {
	f.On(extension.CmdScreenshot, func(*extension.Command) (any, error) {
		return map[string]any{"data": base64.StdEncoding.EncodeToString(pngBytes), "mime": "image/png", "width": 800, "height": 600}, nil
	})
}

func TestAgentOpenTabGoesIntoItsGroupAndIsWatched(t *testing.T) {
	setEnv(t, true)
	f := browserctltest.New()
	s, err := browserctl.Open(browserctl.Options{Profile: "p1", NoTab: true, Sender: f})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenTab("https://example.com/", ""); err == nil {
		t.Fatal("an agent opened https://example.com")
	}
	if len(f.Calls()) != 0 {
		t.Fatalf("a refused open still sent %v", f.Types())
	}
	id, err := s.OpenTab("http://localhost:3000/", "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"create_tab", "page_info", "tab_group", "watch_tab", "read_console", "read_network", "reload", "page_info"}
	if got := f.Types(); !reflect.DeepEqual(got, want) {
		t.Fatalf("commands = %v, want %v", got, want)
	}
	tab, _ := f.Tab(id)
	if tab.GroupTitle != "Coder · Agent" {
		t.Fatalf("group = %q", tab.GroupTitle)
	}

	// The next command in this context acts on that tab.
	s2, err := browserctl.Open(browserctl.Options{Profile: "p1", Sender: f})
	if err != nil {
		t.Fatal(err)
	}
	if s2.TabID() != id || s2.URL() != "http://localhost:3000/" {
		t.Fatalf("default tab = %d %q", s2.TabID(), s2.URL())
	}
}

func TestConsoleStartsAtTheLastLoad(t *testing.T) {
	setEnv(t, true)
	f := browserctltest.New()
	newest := 4 // the first load's messages, replayed when the watch starts
	f.On(extension.CmdReadConsole, func(c *extension.Command) (any, error) {
		since, _ := c.Params["since"].(int)
		entries := []any{}
		for seq := since + 1; seq <= newest; seq++ {
			entries = append(entries, map[string]any{"seq": seq, "level": "error", "text": "boom"})
		}
		return map[string]any{"entries": entries, "next": newest}, nil
	})
	s, _ := browserctl.Open(browserctl.Options{Profile: "p1", NoTab: true, Sender: f})
	if _, err := s.OpenTab("http://localhost:3000/", ""); err != nil {
		t.Fatal(err)
	}
	newest = 6 // the reload logs two
	s2, _ := browserctl.Open(browserctl.Options{Profile: "p1", Sender: f})
	got, next, err := s2.Console(-1, nil, 0)
	if err != nil || len(got) != 2 || got[0].Seq != 5 || next != 6 {
		t.Fatalf("console since the load = %+v next %d, %v", got, next, err)
	}
	if all, _, _ := s2.Console(0, nil, 0); len(all) != 6 {
		t.Fatalf("console from the start = %d entries", len(all))
	}
}

func TestAgentIsRefusedOnTabsItMayNotDrive(t *testing.T) {
	setEnv(t, true)
	f := browserctltest.New()
	bank := f.AddTab("https://bank.example/account", "", true)
	if _, err := browserctl.Open(browserctl.Options{Profile: "p1", Sender: f}); !errors.Is(err, browserctl.ErrNoTab) {
		t.Fatalf("no group tab: %v", err)
	}
	_, err := browserctl.Open(browserctl.Options{Profile: "p1", Tab: bank, Sender: f})
	var na *browserctl.NotAllowedError
	if !errors.As(err, &na) || na.Origin != "https://bank.example" {
		t.Fatalf("bank tab: %v", err)
	}
	if strings.Contains(err.Error(), "/account") {
		t.Fatalf("the refusal leaks the path: %v", err)
	}
	// Allowed once on the allowlist.
	if _, err := browserctl.Open(browserctl.Options{Profile: "p1", Tab: bank, Sender: f, Allowlist: []string{"https://bank.example"}}); err != nil {
		t.Fatal(err)
	}
	// A tab the agent used that navigated away is refused, not swapped.
	local := f.AddTab("http://localhost:5173/", "Coder · Agent", false)
	if _, err := browserctl.Open(browserctl.Options{Profile: "p1", Tab: local, Sender: f}); err != nil {
		t.Fatal(err)
	}
	f.SetURL(local, "https://accounts.example/login")
	if _, err := browserctl.Open(browserctl.Options{Profile: "p1", Sender: f}); !errors.As(err, &na) {
		t.Fatalf("navigated-away last tab: %v", err)
	}
}

func TestVisibleTabsForAnAgent(t *testing.T) {
	setEnv(t, true)
	f := browserctltest.New()
	f.AddTab("https://mail.example/", "", true)
	own := f.AddTab("https://somewhere.example/", "Coder · Agent", false)
	dev := f.AddTab("http://localhost:8080/", "", false)
	s, err := browserctl.Open(browserctl.Options{Profile: "p1", NoTab: true, Sender: f})
	if err != nil {
		t.Fatal(err)
	}
	tabs, err := s.VisibleTabs()
	if err != nil {
		t.Fatal(err)
	}
	if len(tabs) != 2 || tabs[0].TabID != own || tabs[1].TabID != dev {
		t.Fatalf("visible = %+v", tabs)
	}
	setEnv(t, false)
	s, _ = browserctl.Open(browserctl.Options{Profile: "p1", NoTab: true, Sender: f})
	if tabs, _ := s.VisibleTabs(); len(tabs) != 3 {
		t.Fatalf("a human sees %d tabs", len(tabs))
	}
}

func TestHumanDefaultsToTheActiveTab(t *testing.T) {
	setEnv(t, false)
	f := browserctltest.New()
	f.AddTab("chrome://settings", "", false)
	active := f.AddTab("file:///home/me/a.html", "", true)
	s, err := browserctl.Open(browserctl.Options{Profile: "p1", Sender: f})
	if err != nil {
		t.Fatal(err)
	}
	if s.TabID() != active || s.Agent() {
		t.Fatalf("tab = %d agent=%v", s.TabID(), s.Agent())
	}
}

func TestSubmitGateHoldsBlockedSubmitsOnce(t *testing.T) {
	setEnv(t, true)
	ctx := context.Background()
	db := testdb.Open(t).DB
	f := browserctltest.New()
	withScreenshot(f)
	tab := f.AddTab("https://example.com/new", "Coder · Agent", true)
	blocked := []any{}
	f.On(extension.CmdSubmitGuard, func(c *extension.Command) (any, error) {
		return map[string]any{"armed": true, "blocked": blocked}, nil
	})
	s, err := browserctl.Open(browserctl.Options{Profile: "p1", Sender: f, Allowlist: []string{"https://example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if !s.Gated() {
		t.Fatal("an allowlisted remote page is not gated")
	}
	if err := s.BeforeAct(ctx, db); err != nil {
		t.Fatalf("before: %v", err)
	}
	blocked = []any{map[string]any{"seq": 1, "url": "https://example.com/new", "form": map[string]any{
		"action": "https://example.com/post", "method": "post", "selector": "form#f", "submitter": nil,
		"fields": []any{map[string]any{"name": "body", "type": "textarea", "value": "hello"}}}}}
	err = s.AfterAct(ctx, db)
	var aw *browserctl.AwaitingApproval
	if !errors.As(err, &aw) || aw.Via != "guard" || aw.Origin != "https://example.com" {
		t.Fatalf("after: %v", err)
	}
	it, err := browserctl.GetSubmitItem(ctx, db, "p1", aw.HILID)
	if err != nil {
		t.Fatal(err)
	}
	if it.Form.Selector != "form#f" || it.TabID != tab || it.Seq != 1 || it.Screenshot == "" {
		t.Fatalf("item = %+v", it)
	}
	if b, err := os.ReadFile(it.Screenshot); err != nil || string(b) != string(pngBytes) {
		t.Fatalf("card screenshot: %v", err)
	}
	// Reported once: the next command does not raise it again.
	s2, _ := browserctl.Open(browserctl.Options{Profile: "p1", Sender: f, Allowlist: []string{"https://example.com"}})
	if err := s2.BeforeAct(ctx, db); err != nil {
		t.Fatalf("second before: %v", err)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM hil_pending WHERE workflow_id = 'browser.submit'`).Scan(&n)
	if n != 1 {
		t.Fatalf("%d submit items", n)
	}
}

func TestGateFailsClosedWhenTheGuardCannotArm(t *testing.T) {
	setEnv(t, true)
	f := browserctltest.New()
	f.AddTab("https://example.com/", "Coder · Agent", true)
	f.On(extension.CmdSubmitGuard, func(*extension.Command) (any, error) { return nil, errors.New("Unknown command: submit_guard") })
	s, err := browserctl.Open(browserctl.Options{Profile: "p1", Sender: f, Allowlist: []string{"https://example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.BeforeAct(context.Background(), testdb.Open(t).DB); err == nil {
		t.Fatal("acted on a gated page with no guard")
	}
}

func TestLocalPagesAreNotGated(t *testing.T) {
	setEnv(t, true)
	f := browserctltest.New()
	f.AddTab("http://localhost:3000/", "Coder · Agent", true)
	s, _ := browserctl.Open(browserctl.Options{Profile: "p1", Sender: f})
	if s.Gated() {
		t.Fatal("localhost is gated")
	}
	if err := s.BeforeAct(context.Background(), testdb.Open(t).DB); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Types() {
		if c == extension.CmdSubmitGuard {
			t.Fatalf("the guard was armed on localhost: %v", f.Types())
		}
	}
}

func resubmitSetup(t *testing.T, fp *browserctltest.FormPage) (*browserctltest.Fake, *browserctl.Session, browserctl.SubmitItem) {
	t.Helper()
	setEnv(t, true)
	f := browserctltest.New()
	f.AddTab("https://example.com/", "Coder · Agent", true)
	f.On("eval_cdp", fp.Handler())
	s, _ := browserctl.Open(browserctl.Options{Profile: "p1", Sender: f, Allowlist: []string{"https://example.com"}})
	it, err := browserctl.CreateSubmitItem(context.Background(), testdb.Open(t).DB, browserctl.SubmitRequest{
		Profile: "p1", URL: "https://example.com/", Via: "click", Page: s.Page(), Form: browserctl.FormSummary{Selector: "#f", Submitter: "#send"}})
	if err != nil || it.FormDigest == "" {
		t.Fatalf("held item %+v, %v", it, err)
	}
	return f, s, it
}

// The replay checks the form in the page, then clicks with the browser's own
// mouse events and reads how the page took it. It never goes back to the
// script's synthetic click(), which a page's handlers can ignore.
func TestResubmitClicksWithTrustedMouseEventsAndReadsTheReaction(t *testing.T) {
	fp := &browserctltest.FormPage{Selector: "#f", Fields: []browserctl.FormField{{Name: "q", Value: "hi"}}}
	f, s, it := resubmitSetup(t, fp)
	if err := s.Resubmit(it); err != nil || fp.Triggered != 1 {
		t.Fatalf("resubmit: %v (%d submits)", err, fp.Triggered)
	}
	// arm, allow_once, the trigger in an isolated world, a move, the check
	// right before the press, press and release, then the reads of how the
	// page took it, all in the same world.
	calls := f.Calls()
	last := calls[len(calls)-12:]
	method := func(c extension.Command) string { m, _ := c.Params["method"].(string); return m }
	expr := func(c extension.Command) (string, any) {
		p, _ := c.Params["params"].(map[string]interface{})
		e, _ := p["expression"].(string)
		return e, p["contextId"]
	}
	trig, trigCtx := expr(last[5])
	recheck, recheckCtx := expr(last[7])
	peek, peekCtx := expr(last[10])
	collect, collectCtx := expr(last[11])
	if last[0].Type != extension.CmdSubmitGuard || last[0].Params["action"] != extension.SubmitGuardArm ||
		last[1].Type != extension.CmdSubmitGuard || last[1].Params["action"] != extension.SubmitGuardAllowOnce || last[1].Params["frameId"] != 0 ||
		last[2].Type != extension.CmdPageInfo || method(last[3]) != "Page.getFrameTree" || method(last[4]) != "Page.createIsolatedWorld" ||
		method(last[5]) != "Runtime.evaluate" || trigCtx != 7 ||
		!strings.HasPrefix(trig, "/*mono:trigger*/") || !strings.Contains(trig, it.FormDigest) ||
		method(last[7]) != "Runtime.evaluate" || recheckCtx != 7 || !strings.HasPrefix(recheck, "/*mono:recheck*/") ||
		method(last[10]) != "Runtime.evaluate" || peekCtx != 7 || !strings.HasPrefix(peek, "/*mono:peek*/") ||
		method(last[11]) != "Runtime.evaluate" || collectCtx != 7 || !strings.HasPrefix(collect, "/*mono:collect*/") {
		t.Fatalf("calls = %+v", last)
	}
	var mouse []string
	for _, i := range []int{6, 8, 9} {
		p, _ := last[i].Params["params"].(map[string]interface{})
		if method(last[i]) != "Input.dispatchMouseEvent" || p["x"] != 120 || p["y"] != 48 {
			t.Fatalf("call %d is not a mouse event at the control: %+v", i, last[i].Params)
		}
		mouse = append(mouse, p["type"].(string))
	}
	if strings.Join(mouse, ",") != "mouseMoved,mousePressed,mouseReleased" {
		t.Fatalf("mouse events = %v", mouse)
	}
	for _, c := range calls {
		p, _ := c.Params["params"].(map[string]interface{})
		if e, _ := p["expression"].(string); strings.Contains(e, ".click()") {
			t.Fatalf("a script calls the synthetic click(): %.200s", e)
		}
	}
}

// A click that reached the control but changed nothing is reported, not
// passed off as a submit.
func TestResubmitReportsAClickThePageDidNotReactTo(t *testing.T) {
	fp := &browserctltest.FormPage{Selector: "#f", NoReaction: true, Fields: []browserctl.FormField{{Name: "q", Value: "hi"}}}
	_, s, it := resubmitSetup(t, fp)
	var idle *browserctl.NoReactionError
	if err := s.Resubmit(it); !errors.As(err, &idle) || errors.Is(err, browserctl.ErrNotSent) {
		t.Fatalf("resubmit of a click nothing answered: %v", err)
	}
}

// The page's own check in the click event stops a click whose form changed
// after the approval; the guard is closed again.
func TestResubmitStopsAClickWhoseFormChangedBeforeIt(t *testing.T) {
	fp := &browserctltest.FormPage{Selector: "#f", Blocks: true, Fields: []browserctl.FormField{{Name: "q", Value: "hi"}}}
	f, s, it := resubmitSetup(t, fp)
	var changed *browserctl.SubmitChangedError
	if err := s.Resubmit(it); !errors.As(err, &changed) {
		t.Fatalf("a form changed under the click: %v", err)
	}
	if got := guardActions(f); strings.Join(got, ",") != "arm,allow_once,disarm,arm" {
		t.Fatalf("guard actions = %v", got)
	}
}

// The check right before the press stops a click when the form changed or
// something moved over the control after the mouse moved there: the button is
// never pressed, the watch is taken down and the guard closed again.
func TestResubmitChecksAgainBeforePressing(t *testing.T) {
	for name, fp := range map[string]*browserctltest.FormPage{
		"form changed":  {Selector: "#f", Drifts: true, Fields: []browserctl.FormField{{Name: "q", Value: "hi"}}},
		"control moved": {Selector: "#f", MovesAway: true, Fields: []browserctl.FormField{{Name: "q", Value: "hi"}}},
	} {
		f, s, it := resubmitSetup(t, fp)
		err := s.Resubmit(it)
		var changed *browserctl.SubmitChangedError
		if !errors.Is(err, browserctl.ErrNotSent) && !errors.As(err, &changed) {
			t.Fatalf("%s: %v", name, err)
		}
		for _, c := range f.Calls() {
			if p, _ := c.Params["params"].(map[string]interface{}); p["type"] == "mousePressed" {
				t.Fatalf("%s: the button was pressed", name)
			}
		}
		if got := guardActions(f); strings.Join(got, ",") != "arm,allow_once,disarm,arm" {
			t.Fatalf("%s: guard actions = %v", name, got)
		}
	}
}

// A click that landed on another element is cancelled in the page and the
// approval goes back; a click the page did not answer re-arms the guard so
// an unused allowance does not stay open.
func TestResubmitMisdirectedAndUnansweredClicksCloseTheGuard(t *testing.T) {
	fp := &browserctltest.FormPage{Selector: "#f", Misdirects: true, Fields: []browserctl.FormField{{Name: "q", Value: "hi"}}}
	f, s, it := resubmitSetup(t, fp)
	if err := s.Resubmit(it); !errors.Is(err, browserctl.ErrNotSent) {
		t.Fatalf("a misdirected click: %v", err)
	}
	if got := guardActions(f); strings.Join(got, ",") != "arm,allow_once,disarm,arm" {
		t.Fatalf("misdirected: guard actions = %v", got)
	}
	fp = &browserctltest.FormPage{Selector: "#f", NoReaction: true, Fields: []browserctl.FormField{{Name: "q", Value: "hi"}}}
	f, s, it = resubmitSetup(t, fp)
	var idle *browserctl.NoReactionError
	if err := s.Resubmit(it); !errors.As(err, &idle) {
		t.Fatalf("an unanswered click: %v", err)
	}
	if got := guardActions(f); strings.Join(got, ",") != "arm,allow_once,arm" {
		t.Fatalf("unanswered: guard actions = %v", got)
	}
}

// A stray click elsewhere that was cancelled does not undo a click that
// reached the control: the approval is used up, not handed back.
func TestResubmitAStrayClickDoesNotHandTheApprovalBack(t *testing.T) {
	fp := &browserctltest.FormPage{Selector: "#f", StrayAndFired: true, Fields: []browserctl.FormField{{Name: "q", Value: "hi"}}}
	_, s, it := resubmitSetup(t, fp)
	if err := s.Resubmit(it); err != nil {
		t.Fatalf("a click that reached the control with a stray one beside it: %v", err)
	}
}

// A result that cannot be read is a submit only when the tab's address
// really changed; otherwise it is unknown, the guard is closed and the
// error says so.
func TestResubmitAnUnreadableResultNeedsTheTabToHaveMoved(t *testing.T) {
	fp := &browserctltest.FormPage{Selector: "#f", ReadFails: "Cannot find context with specified id", Fields: []browserctl.FormField{{Name: "q", Value: "hi"}}}
	f, s, it := resubmitSetup(t, fp)
	err := s.Resubmit(it)
	if err == nil || errors.Is(err, browserctl.ErrNotSent) || !strings.Contains(err.Error(), "check the page") {
		t.Fatalf("an unreadable result on a page that did not move: %v", err)
	}
	if got := guardActions(f); strings.Join(got, ",") != "arm,allow_once,disarm,arm" {
		t.Fatalf("guard actions = %v", got)
	}
	fp = &browserctltest.FormPage{Selector: "#f", ReadFails: "Cannot find context with specified id", Fields: []browserctl.FormField{{Name: "q", Value: "hi"}}}
	f, s, it = resubmitSetup(t, fp)
	fp.OnRead = func() { f.SetURL(s.TabID(), "https://example.com/sent") }
	if err := s.Resubmit(it); err != nil {
		t.Fatalf("a page that moved on after the click: %v", err)
	}
}

// A control the page would ignore, or one something covers, is not clicked
// and the approval goes back.
func TestResubmitDoesNotClickADisabledOrCoveredControl(t *testing.T) {
	for name, fp := range map[string]*browserctltest.FormPage{
		"disabled": {Selector: "#f", Disabled: true, Fields: []browserctl.FormField{{Name: "q", Value: "hi"}}},
		"covered":  {Selector: "#f", Covered: true, Fields: []browserctl.FormField{{Name: "q", Value: "hi"}}},
	} {
		f, s, it := resubmitSetup(t, fp)
		if err := s.Resubmit(it); !errors.Is(err, browserctl.ErrNotSent) {
			t.Fatalf("%s: %v", name, err)
		}
		for _, c := range f.Calls() {
			if c.Params["method"] == "Input.dispatchMouseEvent" {
				t.Fatalf("%s: the control was clicked", name)
			}
		}
		if got := guardActions(f); strings.Join(got, ",") != "arm,allow_once,disarm,arm" {
			t.Fatalf("%s: guard actions = %v", name, got)
		}
	}
}

func TestWriteScreenshot(t *testing.T) {
	dir := t.TempDir()
	shot := &browserctl.Shot{Data: pngBytes, Mime: "image/png"}
	a, err := browserctl.WriteScreenshot(dir, "", shot)
	if err != nil {
		t.Fatal(err)
	}
	b, err := browserctl.WriteScreenshot(dir, "", shot)
	if err != nil {
		t.Fatal(err)
	}
	if a == b || filepath.Dir(a) != filepath.Join(dir, ".monoagent", "screenshots") || filepath.Dir(b) != filepath.Dir(a) || !strings.HasSuffix(a, "-1.png") {
		t.Fatalf("paths %s %s", a, b)
	}
	c, err := browserctl.WriteScreenshot(dir, "out/x.png", shot)
	if err != nil || c != filepath.Join(dir, "out", "x.png") {
		t.Fatalf("--out path %s, %v", c, err)
	}
}
