package browserjev

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/extension"
	"github.com/monoes/mono-agent/internal/hilsend"
	"github.com/monoes/mono-agent/internal/jevpick"
	"github.com/monoes/mono-agent/internal/testdb"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// fakeGuardPage records submit_guard calls and answers with state.
type fakeGuardPage struct {
	state     extension.SubmitGuardState
	actions   []string
	resubmits []browserctl.FormSummary
	failWith  error     // what the next resubmit returns
	origin    string    // the origin an arm reports
	log       *[]string // a timeline shared with a driver, when set
}

func (p *fakeGuardPage) SubmitGuard(action string) (*extension.SubmitGuardState, error) {
	p.actions = append(p.actions, action)
	if p.log != nil {
		*p.log = append(*p.log, "guard:"+action)
	}
	if action == extension.SubmitGuardArm {
		p.state.Armed, p.state.Origin = true, p.origin
	}
	st := p.state
	return &st, nil
}

func (p *fakeGuardPage) Screenshot(extension.ScreenshotOptions) (*extension.Screenshot, error) {
	return &extension.Screenshot{Data: base64.StdEncoding.EncodeToString([]byte("png")), MIME: "image/png"}, nil
}

func (p *fakeGuardPage) TabID() int { return 41 }

// fakeResubmit stands in for browserctl.ResubmitPage and ResubmitItem on a
// fakeGuardPage.
func fakeResubmit(t *testing.T) {
	oldPage, oldItem := resubmitPage, resubmitItem
	resubmitPage = func(gp guardPage, f browserctl.FormSummary) error {
		p := gp.(*fakeGuardPage)
		_, _ = p.SubmitGuard(extension.SubmitGuardAllowOnce)
		p.resubmits = append(p.resubmits, f)
		return p.failWith
	}
	resubmitItem = func(gp guardPage, it browserctl.SubmitItem) error { return resubmitPage(gp, it.Form) }
	t.Cleanup(func() { resubmitPage, resubmitItem = oldPage, oldItem })
}

func blockedAt(seq int64, url string) extension.BlockedSubmit {
	return extension.BlockedSubmit{Seq: seq, URL: url, Form: extension.SubmitForm{Action: url, Method: "post", Submitter: "#send",
		Fields: []extension.SubmitField{{Name: "msg", Type: "text", Value: "hi"}, {Name: "pw", Type: "password", Value: "•••"}}}}
}

func TestExtGuardHoldsSubmitsAsBrowserSubmitItems(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fakeResubmit(t)
	db := testdb.Open(t).DB
	ctx := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db), "p1")
	if _, err := armGuard(context.Background(), &fakeGuardPage{}); err == nil {
		t.Error("no database to hold approvals: arming must fail")
	}

	page := &fakeGuardPage{state: extension.SubmitGuardState{Blocked: []extension.BlockedSubmit{blockedAt(3, "https://old.example.com/")}}}
	g, err := armGuard(ctx, page)
	if err != nil || page.actions[0] != extension.SubmitGuardArm || g.seen != 3 {
		t.Fatalf("arm: %v %v seen=%d", err, page.actions, g.seen)
	}
	if got, _ := g.Blocked(ctx); len(got) != 0 {
		t.Errorf("a record from before the run is not the run's: %+v", got)
	}
	page.state.Blocked = append(page.state.Blocked, blockedAt(4, "https://shop.example.com/cart"))
	got, err := g.Blocked(ctx)
	if err != nil || len(got) != 1 || got[0].Seq != 4 || got[0].Form.Submitter != "#send" || got[0].Form.Fields[1].Value != "•••" {
		t.Fatalf("blocked = %+v, %v", got, err)
	}

	id, err := g.Ask(ctx, got[0], hilsend.PreparedBy{AgentID: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	it, err := browserctl.GetSubmitItem(ctx, db, "p1", id)
	if err != nil || it.Status != "pending" || it.TabID != 41 || it.Via != "guard" || it.Seq != 4 || it.Origin != "https://shop.example.com" ||
		it.RequestedBy.AgentID != "chat" {
		t.Fatalf("item = %+v, %v", it, err)
	}
	if _, err := os.Stat(it.Screenshot); err != nil || !strings.Contains(it.Screenshot, ".monoagent/browser/hil/") {
		t.Errorf("card screenshot %q: %v", it.Screenshot, err)
	}
	if st, _ := g.Decision(ctx, id); st != "pending" {
		t.Errorf("decision = %s", st)
	}
	framed := blockedAt(5, "https://shop.example.com/cart")
	framed.FrameID = 2
	page.state.Blocked = append(page.state.Blocked, framed)
	if got, _ := g.Blocked(ctx); len(got) != 1 || got[0].FrameID != 2 {
		t.Fatalf("a submit in a frame: %+v", got)
	} else if fid, err := g.Ask(ctx, got[0], hilsend.PreparedBy{}); err != nil {
		t.Fatal(err)
	} else if it, _ := browserctl.GetSubmitItem(ctx, db, "p1", fid); it.FrameID != 2 {
		t.Errorf("the item keeps the frame it was held in: %+v", it)
	}
	if _, err := browserctl.DecideSubmitItem(ctx, db, "p1", id, true); err != nil {
		t.Fatal(err)
	}
	if st, _ := g.Decision(ctx, id); st != "approved" {
		t.Errorf("decision = %s", st)
	}

	page.actions = nil
	if err := g.Submit(ctx, got[0], id); err != nil {
		t.Fatal(err)
	}
	if strings.Join(page.actions, ",") != extension.SubmitGuardAllowOnce || len(page.resubmits) != 1 || page.resubmits[0].Submitter != "#send" {
		t.Errorf("submit: actions=%v resubmits=%v", page.actions, page.resubmits)
	}
	if it, _ := browserctl.GetSubmitItem(ctx, db, "p1", id); it.Status != "consumed" || it.SubmittedAt == "" {
		t.Errorf("an approval is used once: %+v", it)
	}
	page.actions = nil
	if err := g.Submit(ctx, got[0], id); err != nil || len(page.actions) != 0 {
		t.Errorf("an item already used is not sent again: %v %v", err, page.actions)
	}
}

// Only the user's own approval counts: one an agent could set (MCP, the
// HTTP API) is not used.
func TestExtGuardIgnoresApprovalsMadeElsewhere(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	db := testdb.Open(t).DB
	ctx := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db), "p1")
	g, err := armGuard(ctx, &fakeGuardPage{})
	if err != nil {
		t.Fatal(err)
	}
	id, err := g.Ask(ctx, blockedSubmit{Seq: 1, URL: "https://shop.example.com/"}, hilsend.PreparedBy{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE hil_pending SET status = 'approved' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if st, _ := g.Decision(ctx, id); st != "approved_elsewhere" {
		t.Errorf("decision = %s", st)
	}
}

func TestExtGuardRearmsAndRecoversRecords(t *testing.T) {
	fakeResubmit(t)
	db := testdb.Open(t).DB
	ctx := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db), "p1")
	page := &fakeGuardPage{}
	g, err := armGuard(ctx, page)
	if err != nil {
		t.Fatal(err)
	}
	page.state.Blocked = []extension.BlockedSubmit{blockedAt(9, "https://a.example.com/")}
	if got, _ := g.Blocked(ctx); len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	// The extension restarted: the guard is off and its records start over.
	page.state = extension.SubmitGuardState{Blocked: []extension.BlockedSubmit{blockedAt(1, "https://a.example.com/")}}
	page.actions = nil
	got, _ := g.Blocked(ctx)
	if len(got) != 1 || got[0].Seq != 1 || !strings.Contains(strings.Join(page.actions, ","), extension.SubmitGuardArm) {
		t.Errorf("after a restart: blocked=%+v actions=%v", got, page.actions)
	}

	// A submit that can no longer be repeated holds the next one again.
	page.actions, page.failWith = nil, errors.New("the form is gone")
	if err := g.Submit(ctx, got[0], ""); err == nil || strings.Join(page.actions, ",") != "allow_once,arm" {
		t.Errorf("err=%v actions=%v", err, page.actions)
	}
}

// A click the page did not react to is not a failed submit: the run goes on
// and its own "nothing changed" check watches the page.
func TestExtGuardTreatsAnUnansweredClickAsSent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fakeResubmit(t)
	db := testdb.Open(t).DB
	ctx := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db), "p1")
	page := &fakeGuardPage{}
	g, err := armGuard(ctx, page)
	if err != nil {
		t.Fatal(err)
	}
	page.state.Blocked = []extension.BlockedSubmit{blockedAt(2, "http://localhost:3000/")}
	got, _ := g.Blocked(ctx)
	page.actions, page.failWith = nil, &browserctl.NoReactionError{}
	if err := g.Submit(ctx, got[0], ""); err != nil {
		t.Errorf("an unanswered click failed the run: %v", err)
	}
}

// An approval is handed back when nothing reached the page, and used up
// when something did.
func TestExtGuardHandsBackAnUnsentApproval(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fakeResubmit(t)
	db := testdb.Open(t).DB
	ctx := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db), "p1")
	page := &fakeGuardPage{}
	g, err := armGuard(ctx, page)
	if err != nil {
		t.Fatal(err)
	}
	b := blockedSubmit{Seq: 1, URL: "https://shop.example.com/", Form: browserctl.FormSummary{Submitter: "#go"}}
	for _, tc := range []struct {
		fail error
		want string
	}{
		{fmt.Errorf("%w: no guard", browserctl.ErrNotSent), "approved"},
		{&browserctl.SubmitChangedError{What: "the form's contents"}, "approved"},
		{errors.New("click failed"), "consumed"},
	} {
		id, err := g.Ask(ctx, b, hilsend.PreparedBy{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := browserctl.DecideSubmitItem(ctx, db, "p1", id, true); err != nil {
			t.Fatal(err)
		}
		page.failWith = tc.fail
		if err := g.Submit(ctx, b, id); err == nil {
			t.Fatal("a failed resubmit is an error")
		}
		if it, _ := browserctl.GetSubmitItem(ctx, db, "p1", id); it.Status != tc.want {
			t.Errorf("after %v: status %s, want %s", tc.fail, it.Status, tc.want)
		}
	}
}

// A page that moved to another origin is guarded again before anything acts
// on it: the extension disarms a tab whose top page changes origin.
func TestExtGuardFollowsTheTabToANewOrigin(t *testing.T) {
	db := testdb.Open(t).DB
	ctx := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db), "p1")
	page := &fakeGuardPage{origin: "https://a.example.com"}
	g, err := armGuard(ctx, page)
	if err != nil {
		t.Fatal(err)
	}
	page.actions = nil
	for _, u := range []string{"https://a.example.com/cart", "", "about:blank"} {
		if err := g.Follow(ctx, u); err != nil || len(page.actions) != 0 {
			t.Fatalf("%q: the guard holds this page already: %v %v", u, err, page.actions)
		}
	}
	if err := g.Follow(ctx, "https://b.example.com/checkout"); err != nil || strings.Join(page.actions, ",") != "arm" {
		t.Fatalf("another origin: %v %v", err, page.actions)
	}
	if err := g.Follow(ctx, "https://b.example.com/pay"); err != nil || len(page.actions) != 1 {
		t.Errorf("armed there once: %v", page.actions)
	}
}

// navDriver moves to another origin after its first action. Its guard is a
// real extGuard over a fakeGuardPage, both writing one timeline.
type navDriver struct {
	*fakeDriver
	page *fakeGuardPage
	log  *[]string
}

func (d *navDriver) ArmSubmitGuard(ctx context.Context) (submitGuard, error) {
	return armGuard(ctx, d.page)
}

func (d *navDriver) Observe(ctx context.Context) (*jevpick.PageState, error) {
	p, err := d.fakeDriver.Observe(ctx)
	if err == nil {
		p.URL = "https://a.example.com/"
		if len(d.executed) > 0 {
			p.URL = "https://b.example.com/"
		}
		if d.results {
			p.URL += "results"
		}
	}
	return p, err
}

func (d *navDriver) Act(ctx context.Context, p *jevpick.PageState, a *jevpick.Action, text string) error {
	*d.log = append(*d.log, "act:"+a.ID)
	return d.fakeDriver.Act(ctx, p, a, text)
}

func TestRunRearmsTheGuardAfterACrossOriginNavigation(t *testing.T) {
	t.Setenv(EnvAICaller, "chat")
	policyServer(t, nil)
	var log []string
	drv := &navDriver{fakeDriver: &fakeDriver{}, page: &fakeGuardPage{origin: "https://a.example.com", log: &log}, log: &log}
	n := &Node{writer: zurich, open: func(context.Context, string) (driver, func(), error) { return drv, func() {}, nil }}
	ctx := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), testdb.Open(t).DB), "p1")
	out, err := n.Execute(ctx, workflow.NodeInput{}, map[string]interface{}{"url": "https://a.example.com/", "goal": "Search for zurich", "api_key": "k"})
	if err != nil || out[0].Items[0].JSON["status"] != "done" {
		t.Fatalf("run: %v %v", out, err)
	}
	got := strings.Join(log, ",")
	if got != "guard:arm,act:e1,guard:status,guard:arm,act:e3,guard:status" {
		t.Errorf("timeline = %s: the guard must be armed again on b.example.com before acting there", got)
	}
}

// bridgeStub answers an ExtensionPage's commands the way the extension
// would: submit_guard with its state, everything else unsupported.
type bridgeStub struct {
	state   extension.SubmitGuardState
	actions []string
}

func (b *bridgeStub) SendCommand(cmd *extension.Command, _ time.Duration) (*extension.Response, error) {
	if cmd.Type != extension.CmdSubmitGuard {
		return nil, fmt.Errorf("extension error: %s is not stubbed", cmd.Type)
	}
	action, _ := cmd.Params["action"].(string)
	b.actions = append(b.actions, action)
	if action == extension.SubmitGuardArm {
		b.state.Armed, b.state.Origin = true, "https://shop.example.com"
	}
	return &extension.Response{Success: true, Data: b.state}, nil
}

// A submit held in a frame of the page is recorded with that frame, stored
// unbound (browserctl repeats only top-frame submits), and its approval is
// refused and handed back instead of being retried in the top frame.
func TestExtGuardRefusesASubmitHeldInAFrame(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	db := testdb.Open(t).DB
	ctx := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db), "p1")
	stub := &bridgeStub{}
	g, err := armGuard(ctx, extension.NewExtensionPage(stub, 41))
	if err != nil {
		t.Fatal(err)
	}
	framed := blockedAt(1, "https://shop.example.com/cart")
	framed.FrameID = 3
	stub.state.Blocked = []extension.BlockedSubmit{framed}
	got, err := g.Blocked(ctx)
	if err != nil || len(got) != 1 || got[0].FrameID != 3 {
		t.Fatalf("blocked = %+v, %v", got, err)
	}
	id, err := g.Ask(ctx, got[0], hilsend.PreparedBy{AgentID: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	it, err := browserctl.GetSubmitItem(ctx, db, "p1", id)
	if err != nil || it.FrameID != 3 || it.TabID != 41 || it.FormDigest != "" {
		t.Fatalf("item = %+v, %v: want frame 3, unbound", it, err)
	}
	if _, err := browserctl.DecideSubmitItem(ctx, db, "p1", id, true); err != nil {
		t.Fatal(err)
	}
	stub.actions = nil
	if err := g.Submit(ctx, got[0], id); !errors.Is(err, browserctl.ErrSubmitUnbound) {
		t.Fatalf("submit = %v, want it refused as unbound", err)
	}
	for _, a := range stub.actions {
		if a == extension.SubmitGuardAllowOnce {
			t.Errorf("nothing may be let through: %v", stub.actions)
		}
	}
	if it, _ := browserctl.GetSubmitItem(ctx, db, "p1", id); it.Status != "approved" {
		t.Errorf("the approval is handed back: %s", it.Status)
	}

	// On a local page no approval is asked, and a framed submit is not
	// repeated in the top frame either.
	stub.actions = nil
	local := blockedSubmit{Seq: 2, URL: "http://localhost:5173/", FrameID: 3, Form: browserctl.FormSummary{Submitter: "#go"}}
	end, err := settleSubmits(ctx, &onceGuard{extGuard: g, b: local}, caller{name: "chat"})
	if err != nil || end == nil || end.status != "blocked" || !strings.Contains(end.reason, "frame") {
		t.Fatalf("a framed local submit: %+v %v", end, err)
	}
	for _, a := range stub.actions {
		if a == extension.SubmitGuardAllowOnce {
			t.Errorf("nothing may be let through: %v", stub.actions)
		}
	}
}

// onceGuard is g reporting b as blocked.
type onceGuard struct {
	*extGuard
	b blockedSubmit
}

func (o *onceGuard) Blocked(context.Context) ([]blockedSubmit, error) {
	return []blockedSubmit{o.b}, nil
}
