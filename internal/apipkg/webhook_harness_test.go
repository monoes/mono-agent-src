package apipkg

// The webhook half of the catalog harness: every package with a webhook
// block has catalog/testdata/webhook/<id>.json, whose create, delete and
// check requests are run against the mock server and compared like an
// operation's (method, path, query, body, auth), and whose verify sample
// must pass while a changed body must fail. See FORMAT.md §17.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

// webhookCase is catalog/testdata/webhook/<id>.json.
type webhookCase struct {
	// Params is the register config (n8n trigger param names).
	Params     map[string]any `json:"params"`
	Credential map[string]any `json:"credential,omitempty"`
	// CreateResponses answer the create requests in order;
	// ResponseHeaders are sent with every answer (Resource-ID ids).
	CreateResponses []any             `json:"create_responses"`
	ResponseHeaders map[string]string `json:"response_headers,omitempty"`
	ExpectCreate    []expectReq       `json:"expect_create"`
	// HookID and Extra are what Register must return.
	HookID string         `json:"hook_id"`
	Extra  map[string]any `json:"extra,omitempty"`
	// ExpectDelete are the delete requests for that registration.
	ExpectDelete []expectReq `json:"expect_delete"`
	Check        *struct {
		Response any       `json:"response"`
		Exists   bool      `json:"exists"`
		Expect   expectReq `json:"expect"`
	} `json:"check,omitempty"`
	// Verify is a signed sample delivery; Extra holds stored values the
	// verify secret reads (a vendor-issued signing secret).
	Verify *struct {
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
		Extra   map[string]any    `json:"extra,omitempty"`
	} `json:"verify,omitempty"`
}

// testHook is the AppWebhook every webhook case registers.
func testHook(cfg map[string]any) workflow.AppWebhook {
	return workflow.AppWebhook{WorkflowID: "wf-1", NodeID: "node-1", NodeType: "trigger.webhook",
		Config: cfg, URL: "https://hooks.example.test/webhook/wh-path", Path: "wh-path", Secret: "test-secret"}
}

func TestCatalogWebhooks(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // catalog packages only
	Reload()
	t.Cleanup(Reload)
	pkgs, _ := Load()
	n := 0
	for _, p := range pkgs {
		if p.Webhook == nil {
			continue
		}
		n++
		t.Run(p.ID, func(t *testing.T) { checkWebhookCase(t, p) })
	}
	if n == 0 {
		t.Fatal("no catalog package has a webhook block")
	}
}

func checkWebhookCase(t *testing.T, p *Package) {
	b, err := os.ReadFile("catalog/testdata/webhook/" + p.ID + ".json")
	if err != nil {
		t.Fatalf("catalog/%s.json has a webhook block and needs catalog/testdata/webhook/%s.json: %v", p.ID, p.ID, err)
	}
	var c webhookCase
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("catalog/testdata/webhook/%s.json: %v", p.ID, err)
	}
	cred := c.Credential
	if cred == nil {
		cred = fakeCredential(p)
	}
	cfg := map[string]any{"credential": cred}
	for k, v := range c.Params {
		cfg[k] = v
	}
	hook := testHook(cfg)
	reg := webhookRegistrar{id: p.ID}
	credDef := p.selectCredential(cred)
	params := p.Webhook.hookParams(cfg)

	srv := newMockServer(t, nil, 0, c.CreateResponses...)
	srv.mu.Lock()
	srv.header = c.ResponseHeaders
	srv.mu.Unlock()
	got, err := reg.Register(context.Background(), hook)
	if err != nil {
		t.Fatalf("%s webhook create failed: %v", p.ID, err)
	}
	checkHookRequests(t, p.ID+" create", srv.requests(), c.ExpectCreate, credDef, cred, params)
	if got.ID != c.HookID {
		t.Errorf("%s webhook: hook id %q, want %q", p.ID, got.ID, c.HookID)
	}
	for k, want := range c.Extra {
		if !reflect.DeepEqual(normalize(got.Extra[k]), normalize(want)) {
			t.Errorf("%s webhook: extra.%s = %s, want %s", p.ID, k, jsonText(got.Extra[k]), jsonText(want))
		}
	}

	srv = newMockServer(t, nil, 0)
	if err := reg.Unregister(context.Background(), hook, got); err != nil {
		t.Fatalf("%s webhook delete failed: %v", p.ID, err)
	}
	checkHookRequests(t, p.ID+" delete", srv.requests(), c.ExpectDelete, credDef, cred, params)
	srv = newMockServer(t, nil, http.StatusNotFound)
	if err := reg.Unregister(context.Background(), hook, got); err != nil {
		t.Errorf("%s webhook: a 404 on delete should count as removed, got %v", p.ID, err)
	}

	if ch := c.Check; ch != nil {
		srv = newMockServer(t, ch.Response, 0)
		exists, err := reg.Exists(context.Background(), hook, got)
		if err != nil || exists != ch.Exists {
			t.Errorf("%s webhook check: exists=%v err=%v, want %v", p.ID, exists, err, ch.Exists)
		}
		checkHookRequests(t, p.ID+" check", srv.requests(), []expectReq{ch.Expect}, credDef, cred, params)
		srv = newMockServer(t, nil, http.StatusNotFound)
		if exists, err := reg.Exists(context.Background(), hook, got); exists || err != nil {
			t.Errorf("%s webhook check: a 404 should mean gone, got exists=%v err=%v", p.ID, exists, err)
		}
	} else if p.Webhook.Check != nil {
		t.Errorf("%s webhook has a check block; the case needs \"check\"", p.ID)
	}

	if v := c.Verify; v != nil {
		h := http.Header{}
		for k, val := range v.Headers {
			h.Set(k, val)
		}
		stored := workflow.AppWebhookHook{ID: got.ID, Extra: got.Extra}
		for k, val := range v.Extra {
			stored.Extra[k] = val
		}
		if err := reg.VerifyDelivery(h, []byte(v.Body), hook, stored); err != nil {
			t.Errorf("%s webhook verify: the sample delivery should pass: %v", p.ID, err)
		}
		if err := reg.VerifyDelivery(h, []byte(v.Body+" "), hook, stored); err == nil && p.Webhook.Verify.Alg != "token" {
			t.Errorf("%s webhook verify: a changed body should fail", p.ID)
		}
	} else if p.Webhook.Verify != nil {
		t.Errorf("%s webhook has a verify block; the case needs a signed \"verify\" sample", p.ID)
	}
}

// checkHookRequests compares the captured requests with the expected ones.
func checkHookRequests(t *testing.T, where string, reqs []captured, want []expectReq, c *Credential, cred, params map[string]any) {
	t.Helper()
	if len(reqs) != len(want) {
		var seen []string
		for _, r := range reqs {
			seen = append(seen, r.method+" "+r.path)
		}
		t.Errorf("%s: %d requests (%s), the case lists %d", where, len(reqs), strings.Join(seen, ", "), len(want))
	}
	for i := 0; i < len(reqs) && i < len(want); i++ {
		w, got, e := fmt.Sprintf("%s · request %d", where, i), reqs[i], want[i]
		if e.Method != "" && got.method != e.Method {
			t.Errorf("%s: method: got %s, want %s", w, got.method, e.Method)
		}
		if !pathMatches(got.path, e.Path) {
			t.Errorf("%s: path: got %q, want %q", w, got.path, e.Path)
		}
		checkQuery(t, w, got.query, e.Query, c)
		checkBody(t, w, got, e.Body)
		for k, v := range e.Headers {
			if g := got.header.Get(k); g != v {
				t.Errorf("%s: header %s: got %q, want %q", w, k, g, v)
			}
		}
		if !authPinned(e, c) {
			checkAuth(t, w, got, c, cred, params, nil, nil)
		}
	}
}

// authPinned reports whether the case lists the auth header itself (a step
// with auth: false, or a token from an earlier step), so the credential
// injection is not checked against it.
func authPinned(e expectReq, c *Credential) bool {
	for k := range e.Headers {
		if strings.EqualFold(k, "Authorization") {
			return true
		}
		if c != nil {
			for ik := range c.Inject.Headers {
				if strings.EqualFold(k, ik) {
					return true
				}
			}
		}
	}
	return false
}
