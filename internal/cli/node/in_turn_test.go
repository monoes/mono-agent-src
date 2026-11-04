package node

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/internal/action"
	browserpkg "github.com/monoes/mono-agent/internal/browser"
	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/connections"
	"github.com/monoes/mono-agent/internal/hilsend"
	"github.com/monoes/mono-agent/internal/nodes"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/workflow"
)

// `node run` decides once, at its entry, whether its process is the user's own terminal (opgate.Refusal), and
// everything below reads that one verdict: the credential destinations, the unapproved-step limits and the
// browser packages' run gate. A process inside a coder turn that cleared its markers is handled like a marked
// agent; the user's own terminal and every run the engine starts are unchanged.

// callerContext sets the process up as one caller. why is what the turn-aware gate answers ("" = the user).
type callerContext struct {
	name  string
	setup func(t *testing.T)
	// agent: the caller is not the user's own terminal.
	agent bool
}

func inTurnGate(why string) func(*testing.T) {
	return func(t *testing.T) {
		asTheOperator(t)
		opgate.SetForTest(t, func(bool) string { return why })
	}
}

func callerContexts() []callerContext {
	return []callerContext{
		{"operator", inTurnGate(""), false},
		{"in a turn, markers cleared", inTurnGate("this command runs inside a coder session's turn, so a model is running it"), true},
		{"token holder", inTurnGate("MONOAGENT_SESSION_TOKEN is set, so a process holding an agent token is running this command"), true},
		{"database unreadable", inTurnGate("cannot tell whether a coder session's turn runs this command (no such column)"), true},
		{"registered host, own database", inTurnGate("this command runs inside a coder session's turn of a registered host"), true},
		{"marked agent", func(t *testing.T) { asTheOperator(t); t.Setenv("CLAUDECODE", "1") }, true},
		{"marked chat", func(t *testing.T) { asTheOperator(t); t.Setenv(browserctl.EnvAICaller, browserctl.KindChat) }, true},
		{"marked workflow", func(t *testing.T) { asTheOperator(t); t.Setenv(workflow.EnvWorkflowRun, "exec-1") }, true},
	}
}

// probe is a node that records the context `node run` handed it.
type probe struct{ ctxs *[]context.Context }

func (probe) Type() string { return "test.probe" }
func (p probe) Execute(ctx context.Context, _ workflow.NodeInput, _ map[string]interface{}) ([]workflow.NodeOutput, error) {
	*p.ctxs = append(*p.ctxs, ctx)
	return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{{JSON: map[string]interface{}{"ok": true}}}}}, nil
}

// An ordinary node run by anything but the user's terminal runs as a step of a workflow the user has not
// approved (the profile's vault grant, file and program limits, audit); the user's own run is a normal one.
func TestNodeRunInATurnRunsAsAnUnapprovedStep(t *testing.T) {
	for _, c := range callerContexts() {
		t.Run(c.name, func(t *testing.T) {
			dbPath := migratedTestDB(t) // clears the markers: set the caller up after it
			c.setup(t)
			var seen []context.Context
			d := testDeps(dbPath, "default", false)
			d.BuildRegistry = func(*sql.DB) *workflow.NodeTypeRegistry {
				r := workflow.NewNodeTypeRegistry()
				r.Register("test.probe", func() workflow.NodeExecutor { return probe{&seen} })
				return r
			}
			if out, err := runNodeSubcmdStdin(t, d, `{"config":{}}`, "test.probe"); err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if len(seen) != 1 {
				t.Fatalf("the node ran %d times", len(seen))
			}
			if got := workflow.AIAuthoredFrom(seen[0]); got != c.agent {
				t.Errorf("unapproved step = %v, want %v", got, c.agent)
			}
			if got := workflow.AgentCallerFrom(seen[0]) != ""; got != c.agent {
				t.Errorf("the verdict is carried = %v, want %v", got, c.agent)
			}
		})
	}
}

// A credential whose config names a host outside the connection's own: refused for anyone but the user.
func TestNodeRunInATurnGetsNoDestinationOfItsOwn(t *testing.T) {
	var mu sync.Mutex
	var auths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	for _, c := range callerContexts() {
		t.Run(c.name, func(t *testing.T) {
			dbPath := migratedTestDB(t)
			c.setup(t)
			mu.Lock()
			auths = nil
			mu.Unlock()
			d := testDeps(dbPath, "default", true)
			d.ResolveCredential = func(_ context.Context, _ *connections.Store, key, _ string) (map[string]interface{}, error) {
				if key != "api-conn" {
					return nil, errors.New("no such connection")
				}
				return map[string]interface{}{"access_token": "api-tok"}, nil // names no host: the user's own terminal may name one
			}
			config := map[string]interface{}{"url": srv.URL + "/anything", "credential_id": "api-conn"}
			out, err := runNodeSubcmdStdin(t, d, emailPayload(t, config), "http.request")
			refused := errors.Is(err, workflow.ErrCredentialDestination)
			if refused != c.agent {
				t.Errorf("refused = %v (%v), want %v\n%s", refused, err, c.agent, out)
			}
			mu.Lock()
			defer mu.Unlock()
			if c.agent && len(auths) != 0 {
				t.Fatalf("the address got %q, want nothing", auths)
			}
		})
	}
}

// A browser action of a package that is not built-in is refused for anyone but the user before the browser is
// connected; a built-in package is not asked.
func TestNodeRunInATurnRunsNoRecordedBrowserAction(t *testing.T) {
	action.SetDefSource(recordedSource{})
	t.Cleanup(func() { action.SetDefSource(nil) })
	for _, c := range callerContexts() {
		t.Run(c.name, func(t *testing.T) {
			dbPath := migratedTestDB(t)
			c.setup(t)
			d := testDeps(dbPath, "default", false)
			d.BuildRegistry = func(*sql.DB) *workflow.NodeTypeRegistry {
				r := workflow.NewNodeTypeRegistry()
				r.Register("acme.press", func() workflow.NodeExecutor { return nodes.NewBrowserNode("acme", "press") })
				return r
			}
			connects := 0
			d.ConnectBridge = func(string, zerolog.Logger) (browserpkg.ExtensionBridge, error) {
				connects++
				return nil, errors.New("no browser in tests")
			}
			raw, _ := json.Marshal(map[string]interface{}{"config": map[string]interface{}{}})
			_, err := runNodeSubcmdStdin(t, d, string(raw), "acme.press")
			var ce *clicore.Error
			refused := errors.As(err, &ce) && ce.Code == 3 && strings.Contains(err.Error(), nodes.ErrUserOnlyBrowserRun.Error())
			if refused != c.agent {
				t.Errorf("refused = %v (%v), want %v", refused, err, c.agent)
			}
			if c.agent && connects != 0 {
				t.Fatalf("the browser was connected %d time(s)", connects)
			}
			if !c.agent && connects != 1 {
				t.Fatalf("the user's run reached the browser %d time(s), want 1", connects)
			}
		})
	}
}

// The gate reads the marker's text first, so a marked caller's refusal is byte-identical; the command's verdict
// is only the fallback, and an engine run (no verdict) is judged as before.
func TestBrowserRunRefusalReadsTheMarkerThenTheCarrier(t *testing.T) {
	action.SetDefSource(recordedSource{})
	t.Cleanup(func() { action.SetDefSource(nil) })
	asTheOperator(t)
	b := nodes.NewBrowserNode("acme", "press")
	if err := b.RunRefusal(context.Background()); err != nil {
		t.Fatalf("no marker, no verdict: %v", err)
	}
	err := b.RunRefusal(workflow.WithAgentCaller(context.Background(), "in a turn"))
	if !errors.Is(err, nodes.ErrUserOnlyBrowserRun) || !strings.HasSuffix(strings.SplitN(err.Error(), "so only the user runs acme.press: ", 2)[1], "in a turn; the user runs it in their own terminal, the app, or a workflow they saved") {
		t.Fatalf("verdict only: %v", err)
	}
	t.Setenv("CLAUDECODE", "1")
	marked := b.RunRefusal(context.Background())
	both := b.RunRefusal(workflow.WithAgentCaller(context.Background(), "in a turn"))
	if marked == nil || both == nil || marked.Error() != both.Error() || !strings.Contains(marked.Error(), operator.Refusal()) {
		t.Fatalf("marked: %v\nmarked and a verdict: %v", marked, both)
	}
}

// countingDevto is service.devto that counts the articles it publishes.
type countingDevto struct{ runs *int }

func (countingDevto) Type() string { return "service.devto" }
func (c countingDevto) Execute(context.Context, workflow.NodeInput, map[string]interface{}) ([]workflow.NodeOutput, error) {
	*c.runs++
	return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{{JSON: map[string]interface{}{"id": "1"}}}}}, nil
}

// A send started by an agent, or by a process the markers do not show, is queued for approval and does not run: the send gate
// (hilsend.Guard) is asked with the command's verdict as well as the markers, so a process inside a coder turn
// that cleared its markers is queued like a marked agent. The user's own terminal sends.
func TestNodeRunSendInATurnIsQueuedForApproval(t *testing.T) {
	for _, c := range callerContexts() {
		t.Run(c.name, func(t *testing.T) {
			dbPath := migratedTestDB(t)
			c.setup(t)
			runs := 0
			d := testDeps(dbPath, "default", false)
			d.BuildRegistry = func(*sql.DB) *workflow.NodeTypeRegistry {
				workflow.SetExecGuard(hilsend.Guard) // as noderegistry.Build does
				r := workflow.NewNodeTypeRegistry()
				r.Register("service.devto", func() workflow.NodeExecutor { return countingDevto{&runs} })
				return r
			}
			payload := `{"config":{"operation":"publish_article","title":"Hello","body_markdown":"Hi all","api_key":"k"}}`
			out, err := runNodeSubcmdStdin(t, d, payload, "service.devto")
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			// The chat and a workflow run are marked callers whose sends this gate never queued: unchanged.
			want := c.agent && c.name != "marked chat" && c.name != "marked workflow"
			queued := strings.Contains(out, "hil_id")
			if queued != want || (runs == 0) != want {
				t.Fatalf("queued = %v, ran %d time(s), want queued = %v\n%s", queued, runs, want, out)
			}
		})
	}
}
