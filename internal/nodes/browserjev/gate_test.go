package browserjev

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/data"
	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/browserctl/browserctltest"
	"github.com/monoes/mono-agent/internal/hilsend"
	"github.com/monoes/mono-agent/internal/jevpick"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
	_ "modernc.org/sqlite"
)

func TestAICaller(t *testing.T) {
	ctx := context.Background()
	if c, ok := aiCaller(ctx); ok {
		t.Fatalf("the user's own run has no AI caller: %+v", c)
	}
	if _, ok := aiCaller(workflow.WithTrigger(ctx, "trigger.manual", nil)); ok {
		t.Error("a manual run is the user's")
	}
	org := workflow.WithTrigger(ctx, workflow.TriggerTypeOrgTool, map[string]interface{}{"org": map[string]interface{}{"name": "growth", "role": "writer"}})
	if c, ok := aiCaller(org); !ok || c.name != "org growth" || c.by.Org != "growth" || c.by.Role != "writer" || c.kind != browserctl.KindOrg {
		t.Errorf("a granted call: %+v %v", c, ok)
	}
	for _, tt := range []string{workflow.TriggerTypeOrgMessage, workflow.TriggerNodeTypeOrg} {
		if c, ok := aiCaller(workflow.WithTrigger(ctx, tt, nil)); !ok || c.kind != browserctl.KindOrg {
			t.Errorf("%s is the org side: %+v", tt, c)
		}
	}
	// A daemon running a marked `workflow run` uses the kind it was marked with.
	for mark, kind := range map[string]string{"chat": "chat", "coder": "coder", "org": "org", "other": "other", "agent": "other", "anything": "other"} {
		c, ok := aiCaller(workflow.WithTrigger(ctx, "trigger.manual", map[string]interface{}{workflow.AICallerKey: mark}))
		if !ok || c.kind != kind {
			t.Errorf("a run marked %q: %+v %v, want kind %s", mark, c, ok, kind)
		}
	}
	// A kind the shell declared but could not prove travels after a "/".
	for mark, declared := range map[string]string{"other/org": "org", "other/coder": "coder", "other/chat": "", "other/": ""} {
		if c, _ := aiCaller(workflow.WithTrigger(ctx, "trigger.manual", map[string]interface{}{workflow.AICallerKey: mark})); c.kind != browserctl.KindOther || c.declared != declared {
			t.Errorf("a run marked %q: %+v, want other declaring %q", mark, c, declared)
		}
	}
	data := map[string]interface{}{"who": "zed"}
	if MarkAICaller(data); len(data) != 1 {
		t.Errorf("the user's own run is not marked: %v", data)
	}
	t.Setenv(EnvAICaller, "chat")
	if MarkAICaller(data); data[workflow.AICallerKey] != "chat" {
		t.Errorf("a chat tool call's run is marked chat: %v", data)
	}
	if c, ok := aiCaller(ctx); !ok || c.name != "chat" || c.by.AgentID != "chat" || c.kind != browserctl.KindChat {
		t.Errorf("a chat tool call: %+v %v", c, ok)
	}

	// A shell is a shell even when it sets the chat's variable: other, or
	// coder / org as its markers say.
	t.Setenv("CLAUDECODE", "1")
	t.Setenv(hilsend.EnvAgentRole, "qa")
	if MarkAICaller(data); data[workflow.AICallerKey] != "other" {
		t.Errorf("an agent shell's run is marked other: %v", data)
	}
	if c, ok := aiCaller(ctx); !ok || c.name != "an agent (CLAUDECODE)" || c.by.Role != "qa" || c.kind != browserctl.KindOther {
		t.Errorf("an agent's shell: %+v %v", c, ok)
	}
	browserctltest.AsAgentKind(t, browserctl.KindCoder) // what a coder turn's spawner gives its shell
	if c, _ := aiCaller(ctx); c.kind != browserctl.KindCoder {
		t.Errorf("a coder chat's shell: %+v", c)
	}
	if MarkAICaller(data); data[workflow.AICallerKey] != "coder" {
		t.Errorf("a coder shell's run is marked coder: %v", data)
	}
	browserctltest.AsAgentKind(t, browserctl.KindOrg)
	t.Setenv(browserctl.EnvAgentKind, "")
	t.Setenv("MONOMIND_ORG_ROLE", "writer")
	if c, _ := aiCaller(ctx); c.kind != browserctl.KindOrg || c.declared != "" {
		t.Errorf("an org role's shell: %+v", c)
	}
	// Without a token to vouch for it, it is other declaring org, and its
	// run's mark says so.
	t.Setenv(browserctl.EnvAgentKindToken, "")
	if c, _ := aiCaller(ctx); c.kind != browserctl.KindOther || c.declared != browserctl.KindOrg {
		t.Errorf("an unproven org role's shell: %+v", c)
	}
	if MarkAICaller(data); data[workflow.AICallerKey] != "other/org" {
		t.Errorf("an unproven org role's run is marked other/org: %v", data)
	}
}

// fakeGuard holds the submits of a guardedDriver until Submit.
type fakeGuard struct {
	url       string
	blocked   []blockedSubmit
	decisions []string // in order; the last one repeats
	asked     []hilsend.PreparedBy
	submitted []string
	changed   bool     // the page no longer shows what was approved
	follows   []string // the page URLs Follow saw, one per action
	drv       *fakeDriver
}

func (g *fakeGuard) Blocked(context.Context) ([]blockedSubmit, error) {
	out := g.blocked
	g.blocked = nil
	return out, nil
}

func (g *fakeGuard) Ask(_ context.Context, b blockedSubmit, by hilsend.PreparedBy) (string, error) {
	g.asked = append(g.asked, by)
	return "hil-1", nil
}

func (g *fakeGuard) Decision(context.Context, string) (string, error) {
	d := g.decisions[0]
	if len(g.decisions) > 1 {
		g.decisions = g.decisions[1:]
	}
	if d == "consumed" { // `browser submit --hil` sent it meanwhile
		g.drv.results = g.drv.query != ""
	}
	return d, nil
}

func (g *fakeGuard) Submit(_ context.Context, b blockedSubmit, id string) error {
	if g.changed {
		return fmt.Errorf("repeat the approved submit: %w", &browserctl.SubmitChangedError{What: "the form's contents"})
	}
	g.submitted = append(g.submitted, id)
	g.drv.results = g.drv.query != ""
	return nil
}

func (g *fakeGuard) Follow(_ context.Context, pageURL string) error {
	g.follows = append(g.follows, pageURL)
	return nil
}

func (g *fakeGuard) TabID() int { return 77 }

// guardedDriver is a fakeDriver whose Go button (e3) submits a form, which
// an armed guard holds.
type guardedDriver struct {
	*fakeDriver
	g      *fakeGuard
	armed  bool
	closed int
}

func (d *guardedDriver) ArmSubmitGuard(context.Context) (submitGuard, error) {
	d.armed = true
	return d.g, nil
}

func (d *guardedDriver) Act(ctx context.Context, p *jevpick.PageState, a *jevpick.Action, text string) error {
	if a.ID != "e3" || !d.armed {
		return d.fakeDriver.Act(ctx, p, a, text)
	}
	d.executed = append(d.executed, "e3:")
	d.g.blocked = append(d.g.blocked, blockedSubmit{Seq: len(d.executed), URL: d.g.url,
		Form: browserctl.FormSummary{Action: d.g.url, Method: "get", Submitter: "#go"}})
	return nil
}

func gatedRun(t *testing.T, url string, decisions []string, timeout float64) (*guardedDriver, map[string]interface{}) {
	t.Helper()
	policyServer(t, nil)
	drv := &guardedDriver{fakeDriver: &fakeDriver{}}
	drv.g = &fakeGuard{url: url, decisions: decisions, drv: drv.fakeDriver}
	n := &Node{writer: zurich, open: func(context.Context, string) (driver, func(), error) {
		return drv, func() { drv.closed++ }, nil
	}}
	out, err := n.Execute(vault.ContextWithProfileID(context.Background(), "p1"), workflow.NodeInput{}, map[string]interface{}{
		"url": "https://shop.example.com/", "goal": "Search for zurich", "api_key": "k", "timeout": timeout,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return drv, out[0].Items[0].JSON
}

func TestUserRunIsNotGated(t *testing.T) {
	drv, res := gatedRun(t, "https://shop.example.com/search", []string{"rejected"}, 30)
	if res["status"] != "done" || drv.armed || len(drv.g.asked) != 0 || res["hil_id"] != nil {
		t.Fatalf("a run the user started must behave as before: armed=%v asked=%v res=%v", drv.armed, drv.g.asked, res)
	}
}

func TestAISubmitWaitsForApproval(t *testing.T) {
	t.Setenv(EnvAICaller, "chat")
	drv, res := gatedRun(t, "https://shop.example.com/search", []string{"pending", "pending", "approved"}, 30)
	if res["status"] != "done" || !drv.armed {
		t.Fatalf("status=%v (%v) armed=%v", res["status"], res["reason"], drv.armed)
	}
	if len(drv.g.asked) != 1 || drv.g.asked[0].AgentID != "chat" || strings.Join(drv.g.submitted, ",") != "hil-1" {
		t.Errorf("asked=%+v submitted=%v", drv.g.asked, drv.g.submitted)
	}
	if got := strings.Join(drv.executed, ","); got != "e1:zurich,e3:" || drv.closed != 1 {
		t.Errorf("executed=%s closed=%d", got, drv.closed)
	}
}

func TestAISubmitRejectedEndsBlocked(t *testing.T) {
	t.Setenv(EnvAICaller, "chat")
	drv, res := gatedRun(t, "https://shop.example.com/search", []string{"pending", "rejected"}, 30)
	if res["status"] != "blocked" || res["hil_id"] != "hil-1" || !strings.Contains(res["reason"].(string), "rejected") {
		t.Fatalf("res = %v", res)
	}
	if len(drv.g.submitted) != 0 || drv.closed != 1 || res["tab_id"] != nil {
		t.Errorf("submitted=%v closed=%d tab=%v", drv.g.submitted, drv.closed, res["tab_id"])
	}
}

func TestAISubmitStillPendingLeavesTheTabOpen(t *testing.T) {
	t.Setenv(EnvAICaller, "chat")
	drv, res := gatedRun(t, "https://shop.example.com/search", []string{"pending"}, 1)
	if res["status"] != "awaiting_approval" || res["hil_id"] != "hil-1" || res["tab_id"] != 77 {
		t.Fatalf("res = %v", res)
	}
	if !strings.Contains(res["reason"].(string), "browser submit --hil hil-1") || drv.closed != 0 || len(drv.g.submitted) != 0 {
		t.Errorf("reason=%v closed=%d submitted=%v", res["reason"], drv.closed, drv.g.submitted)
	}
}

func TestAISubmitConsumedElsewhereGoesOn(t *testing.T) {
	t.Setenv(EnvAICaller, "chat")
	drv, res := gatedRun(t, "https://shop.example.com/search", []string{"consumed"}, 30)
	if res["status"] != "done" || len(drv.g.asked) != 1 || len(drv.g.submitted) != 0 {
		t.Fatalf("a submit `browser submit --hil` already sent is not sent again: %v submitted=%v", res, drv.g.submitted)
	}
}

func TestAISubmitApprovedElsewhereIsNotSent(t *testing.T) {
	t.Setenv(EnvAICaller, "chat")
	drv, res := gatedRun(t, "https://shop.example.com/search", []string{"approved_elsewhere"}, 30)
	if res["status"] != "blocked" || !strings.Contains(res["reason"].(string), "approved outside") || len(drv.g.submitted) != 0 {
		t.Fatalf("res=%v submitted=%v", res, drv.g.submitted)
	}
}

// An approval for a form the page no longer shows is not used: the run ends
// blocked.
func TestAISubmitChangedSinceApprovalEndsBlocked(t *testing.T) {
	t.Setenv(EnvAICaller, "chat")
	policyServer(t, nil)
	drv := &guardedDriver{fakeDriver: &fakeDriver{}}
	drv.g = &fakeGuard{url: "https://shop.example.com/search", decisions: []string{"approved"}, drv: drv.fakeDriver, changed: true}
	n := &Node{writer: zurich, open: func(context.Context, string) (driver, func(), error) { return drv, func() {}, nil }}
	out, err := n.Execute(vault.ContextWithProfileID(context.Background(), "p1"), workflow.NodeInput{}, map[string]interface{}{
		"url": "https://shop.example.com/", "goal": "Search for zurich", "api_key": "k"})
	if err != nil {
		t.Fatal(err)
	}
	res := out[0].Items[0].JSON
	if res["status"] != "blocked" || !strings.Contains(res["reason"].(string), "changed since it was approved") || res["hil_id"] != "hil-1" {
		t.Fatalf("res = %v", res)
	}
}

func TestAISubmitOnADevPageNeedsNoApproval(t *testing.T) {
	t.Setenv(EnvAICaller, "chat")
	drv, res := gatedRun(t, "http://localhost:5173/search", []string{"rejected"}, 30)
	if res["status"] != "done" || len(drv.g.asked) != 0 || strings.Join(drv.g.submitted, ",") != "" || len(drv.g.submitted) != 1 {
		t.Fatalf("status=%v asked=%v submitted=%q", res["status"], drv.g.asked, drv.g.submitted)
	}
}

func TestAIRunFailsClosedWithoutAGuard(t *testing.T) {
	t.Setenv(EnvAICaller, "chat")
	policyServer(t, nil)
	_, err := testNode(&fakeDriver{}, zurich).Execute(vault.ContextWithProfileID(context.Background(), "p1"), workflow.NodeInput{},
		map[string]interface{}{"url": "https://shop.example.com/", "goal": "Search for zurich", "api_key": "k"})
	if err == nil || !strings.Contains(err.Error(), "chat started this run") {
		t.Fatalf("err = %v", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("not a timeout")
	}
}

// A `workflow run` started by a process a workflow node started that cannot show the token of an
// approved, live run marks its run as one an unapproved workflow called, so the engine holds its
// sends and browser steps like that workflow's. Dropping or forging the token, an unapproved run's
// token, a finished run's token and an unreadable store all mark it; the approved run's child and
// the user's own terminal do not.
func TestMarkAICallerMarksAnUnapprovedRunsChild(t *testing.T) {
	bg := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "runs.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	m, _ := data.MigrationsFS.ReadFile("migrations/160_workflow_run_tokens.sql")
	if _, err := db.Exec(string(m)); err != nil {
		t.Fatal(err)
	}
	good, _ := workflow.IssueRunToken(bg, db, "exec-1", true)
	bad, _ := workflow.IssueRunToken(bg, db, "exec-2", false)
	done, _ := workflow.IssueRunToken(bg, db, "exec-3", true)
	if err := workflow.EndRunToken(bg, db, done); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name  string
		env   map[string]string
		db    *sql.DB
		marks bool
	}{
		{"the user's own terminal", nil, db, false},
		{"approved run's child", map[string]string{workflow.EnvWorkflowRun: "exec-1", workflow.EnvRunToken: good}, db, false},
		{"child that dropped the token", map[string]string{workflow.EnvWorkflowRun: "exec-1"}, db, true},
		{"child with a forged token", map[string]string{workflow.EnvWorkflowRun: "exec-1", workflow.EnvRunToken: "0123456789abcdef0123456789abcdef"}, db, true},
		{"unapproved run's child", map[string]string{workflow.EnvWorkflowRun: "exec-2", workflow.EnvRunToken: bad}, db, true},
		{"finished run's child", map[string]string{workflow.EnvWorkflowRun: "exec-3", workflow.EnvRunToken: done}, db, true},
		{"unreadable store", map[string]string{workflow.EnvWorkflowRun: "exec-1", workflow.EnvRunToken: good}, nil, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, k := range append(browserctl.AgentContextEnv(), EnvAICaller, workflow.EnvUnapprovedRun, workflow.EnvWorkflowRun, workflow.EnvRunToken) {
				t.Setenv(k, "")
			}
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			td := map[string]interface{}{}
			MarkAICallerRun(bg, func() (*sql.DB, error) {
				if c.db == nil {
					return nil, errors.New("no database")
				}
				return c.db, nil
			}, td)
			if got := td[workflow.AICallerKey] == workflow.UnapprovedWorkflowCaller; got != c.marks {
				t.Fatalf("marked = %v (%v), want %v", got, td, c.marks)
			}
			if c.marks {
				if _, called := workflow.CalledByUnapproved(td); !called {
					t.Fatal("the engine does not see it as called by an unapproved workflow")
				}
			}
		})
	}
}
