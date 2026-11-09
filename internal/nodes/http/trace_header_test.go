package httpnodes

import (
	"context"
	"github.com/monoes/mono-agent/internal/testhome"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/tracesig"
	"github.com/monoes/mono-agent/internal/workflow"
)

// sentTraceHeader runs one GET in a run started by triggerType with data,
// and returns the X-Monoagent-Trace header the server saw.
func sentTraceHeader(t *testing.T, triggerType string, data map[string]interface{}, headers map[string]interface{}) string {
	t.Helper()
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get(tracesig.Header)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	ctx := workflow.WithTrigger(context.Background(), triggerType, data)
	cfg := map[string]interface{}{"method": "GET", "url": srv.URL}
	if headers != nil {
		cfg["headers"] = headers
	}
	if _, err := (&RequestNode{}).Execute(ctx, workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(map[string]interface{}{})}}, cfg); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return got
}

// A run on a chain sends it on, signed with this machine's key, so a
// webhook it reaches can continue the chain; a run on no chain sends none.
func TestRequestNodeSignsTheRunsChain(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	key, err := tracesig.Key()
	if err != nil {
		t.Fatal(err)
	}
	onChain := map[string]interface{}{"trace": map[string]interface{}{"chain_id": "chn_run", "hop": float64(2)}}

	tok := sentTraceHeader(t, workflow.TriggerTypeOrgTool, onChain, nil)
	if chain, hop, ok := tracesig.Verify(key, tok); !ok || chain != "chn_run" || hop != 2 {
		t.Fatalf("header %q verifies as %q %d %v, want chn_run at 2", tok, chain, hop, ok)
	}

	webhookRun := map[string]interface{}{workflow.WebhookTraceKey: map[string]interface{}{"chain_id": "chn_hook", "hop": float64(5)}}
	if chain, hop, ok := tracesig.Verify(key, sentTraceHeader(t, workflow.TriggerNodeTypeWebhook, webhookRun, nil)); !ok || chain != "chn_hook" || hop != 5 {
		t.Fatalf("webhook run header: %q %d %v", chain, hop, ok)
	}

	if tok := sentTraceHeader(t, "trigger.manual", onChain, nil); tok != "" {
		t.Fatalf("a manual run sent a trace header %q", tok)
	}
}

// A workflow cannot send a trace of its own choosing: a configured header
// of that name is replaced by the run's own, or removed when there is none
// (a replayed token copied from input must not travel).
func TestRequestNodeTraceHeaderCannotBeSetByTheWorkflow(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	key, _ := tracesig.Key()
	replayed, _ := tracesig.Sign(key, "chn_victim", 0)
	custom := map[string]interface{}{tracesig.Header: replayed}

	if tok := sentTraceHeader(t, "trigger.manual", map[string]interface{}{}, custom); tok != "" {
		t.Fatalf("configured header travelled from a run on no chain: %q", tok)
	}
	onChain := map[string]interface{}{"trace": map[string]interface{}{"chain_id": "chn_run", "hop": float64(1)}}
	if chain, _, _ := tracesig.Verify(key, sentTraceHeader(t, workflow.TriggerTypeOrgTool, onChain, custom)); chain != "chn_run" {
		t.Fatalf("configured header won over the run's chain: %q", chain)
	}
}

// Only requests to this machine carry the token by default; other hosts get
// it with propagate_trace (a system that returns it to a webhook here).
func TestTraceHeaderStaysOnThisMachineByDefault(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	t.Setenv("MONOAGENT_WEBHOOK_ADDR", "10.0.0.5:9321")
	ctx := workflow.WithTrigger(context.Background(), workflow.TriggerTypeOrgTool,
		map[string]interface{}{"trace": map[string]interface{}{"chain_id": "chn_run", "hop": float64(1)}})
	sent := func(rawURL string, config map[string]interface{}) bool {
		req, err := http.NewRequest(http.MethodGet, rawURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		setTraceHeader(ctx, req, config, nil)
		return req.Header.Get(tracesig.Header) != ""
	}
	for _, u := range []string{"http://127.0.0.1:9321/webhook/x", "http://localhost/x", "http://[::1]:8080/x", "http://10.0.0.5:9321/webhook/x"} {
		if !sent(u, nil) {
			t.Errorf("%s: no header, want one (this machine)", u)
		}
	}
	if sent("https://api.example.com/x", nil) {
		t.Error("a third-party host got the token without propagate_trace")
	}
	if !sent("https://api.example.com/x", map[string]interface{}{"propagate_trace": true}) {
		t.Error("propagate_trace did not send the token")
	}
}

// An API-key auth header of the same name cannot replace the signed token.
func TestTraceHeaderSurvivesAnAPIKeyHeaderOfTheSameName(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	key, _ := tracesig.Key()
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get(tracesig.Header)
	}))
	defer srv.Close()
	ctx := workflow.WithTrigger(context.Background(), workflow.TriggerTypeOrgTool,
		map[string]interface{}{"trace": map[string]interface{}{"chain_id": "chn_run", "hop": float64(1)}})
	cfg := map[string]interface{}{"method": "GET", "url": srv.URL, "auth_type": "api_key",
		"auth_api_key_in": "header", "auth_api_key_name": tracesig.Header, "auth_api_key_value": "v1.chn_victim.0.replayed"}
	if _, err := (&RequestNode{}).Execute(ctx, workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(map[string]interface{}{})}}, cfg); err != nil {
		t.Fatal(err)
	}
	if chain, _, ok := tracesig.Verify(key, got); !ok || chain != "chn_run" {
		t.Fatalf("server saw %q, want the run's own signed chain", got)
	}
}

// The token does not follow a redirect to another host.
func TestTraceHeaderIsDroppedOnACrossHostRedirect(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	var atTarget string
	reached := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		atTarget = r.Header.Get(tracesig.Header)
	}))
	defer target.Close()
	// Same machine, different host string: 127.0.0.1 redirects to localhost.
	targetURL := strings.Replace(target.URL, "127.0.0.1", "localhost", 1)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, targetURL, http.StatusFound)
	}))
	defer origin.Close()
	ctx := workflow.WithTrigger(context.Background(), workflow.TriggerTypeOrgTool,
		map[string]interface{}{"trace": map[string]interface{}{"chain_id": "chn_run", "hop": float64(1)}})
	if _, err := (&RequestNode{}).Execute(ctx, workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(map[string]interface{}{})}},
		map[string]interface{}{"method": "GET", "url": origin.URL}); err != nil {
		t.Fatal(err)
	}
	if !reached {
		t.Fatal("the redirect was not followed, so this test proves nothing")
	}
	if atTarget != "" {
		t.Fatalf("the token followed a cross-host redirect: %q", atTarget)
	}
}

// #132 item 2: the token carries the deeper of the run's hop and the hop
// its input item reached on the same chain (after the run's own org.send).
func TestRequestNodeSignsTheItemsDeeperHop(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	key, _ := tracesig.Key()
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get(tracesig.Header)
	}))
	defer srv.Close()
	ctx := workflow.WithTrigger(context.Background(), workflow.TriggerNodeTypeOrg,
		map[string]interface{}{"trace": map[string]interface{}{"chain_id": "chn_run", "hop": float64(1)}})
	sentAt := func(item map[string]interface{}) int {
		t.Helper()
		if _, err := (&RequestNode{}).Execute(ctx, workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(item)}},
			map[string]interface{}{"method": "GET", "url": srv.URL}); err != nil {
			t.Fatal(err)
		}
		chain, hop, ok := tracesig.Verify(key, got)
		if !ok || chain != "chn_run" {
			t.Fatalf("server saw %q", got)
		}
		return hop
	}
	if hop := sentAt(map[string]interface{}{"trace": map[string]interface{}{"chain_id": "chn_run", "hop": float64(4)}}); hop != 4 {
		t.Fatalf("signed hop %d, want the item's 4", hop)
	}
	if hop := sentAt(map[string]interface{}{"trace": map[string]interface{}{"chain_id": "chn_other", "hop": float64(9)}}); hop != 1 {
		t.Fatalf("signed hop %d, want the run's 1 (the item is on another chain)", hop)
	}
}
