package httpnodes

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// contentEnv is a profile with one secret "vs" and an agent-access grant.
type contentEnv struct {
	t    *testing.T
	base context.Context
	db   *storage.Database
}

func newContentEnv(t *testing.T, grant secrets.AccessGrant) *contentEnv {
	t.Helper()
	keyring.MockInit()
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	base := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db.DB), "default")
	if _, err := secrets.Add(base, db.DB, "default", "secret", "vs", map[string]string{"secret": "vsecret-777"}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := secrets.SetAgentAccessGrant(base, db.DB, "default", grant); err != nil {
		t.Fatal(err)
	}
	return &contentEnv{t: t, base: base, db: db}
}

// send runs one http.request as an unapproved run does: the config is
// resolved against the stored references, then the node runs.
func (e *contentEnv) send(cfg map[string]interface{}) error {
	e.t.Helper()
	ctx := secrets.WithAccessNode(secrets.AllowResolving(e.base, e.db.DB, "default", secrets.AccessOrigin{WorkflowID: "wf"}), "n1", "Http")
	stored := map[string]interface{}{}
	for k, v := range cfg {
		stored[k] = v
	}
	if err := secrets.ResolveConfig(ctx, e.db.DB, "default", stored, cfg); err != nil {
		return err
	}
	outs, err := (&RequestNode{}).Execute(ctx, workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(map[string]interface{}{})}}, cfg)
	if err != nil {
		return err
	}
	for _, o := range outs {
		if o.Handle == "error" {
			for _, it := range o.Items {
				if msg, _ := it.JSON["error"].(string); msg != "" {
					return errors.New(msg)
				}
			}
		}
	}
	return nil
}

func (r *recorder) n() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.reqs)
}

// Review of #1418 slice 1 (P1): a proxy takes the whole request, body
// included, so a destination on this machine does not exempt a body that
// goes through a proxy that is not.
func TestAgentAccessBodyThroughAProxyIsNotExemptByTheLocalURL(t *testing.T) {
	e := newContentEnv(t, secrets.AccessGrant{AnyHost: true})
	proxy := newRecorder(t, nil)
	err := e.send(map[string]interface{}{"url": "http://localhost:9/r", "method": "POST", "proxy": proxy.URL, "body": "@secret:vs"})
	// The proxy here is on this machine, so this is allowed: the control.
	if err != nil {
		t.Fatalf("control: a body to a local URL through a local proxy: %v", err)
	}
	if proxy.n() != 1 {
		t.Fatalf("control: the local proxy saw %d requests", proxy.n())
	}
	// A proxy that is not this machine (named, reached by the grant's
	// --any-host) gets the body: refused, nothing sent.
	err = e.send(map[string]interface{}{"url": "http://localhost:9/r", "method": "POST", "proxy": "http://proxy.example.test:3128", "body": "@secret:vs"})
	if err == nil || !errors.Is(err, secrets.ErrPublishesSecret) && !strings.Contains(err.Error(), secrets.ErrPublishesSecret.Error()) {
		t.Errorf("a body sent to a local URL through a remote proxy (issue #1418): want the content refusal, got %v", err)
	}
}

// Review of #1418 slice 1 (P2): a redirect from a host on this machine to
// another re-sends a 307/308 body; every hop is checked.
func TestAgentAccessBodyIsNotResentByARedirectToAnotherHost(t *testing.T) {
	e := newContentEnv(t, secrets.AccessGrant{AnyHost: true})
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://vendor.example.test/r", http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()
	err := e.send(map[string]interface{}{"url": redirector.URL + "/r", "method": "POST", "body": "@secret:vs", "follow_redirects": true})
	if err == nil || !strings.Contains(err.Error(), secrets.ErrPublishesSecret.Error()) {
		t.Errorf("the redirect hop re-sent the secret body to another host (issue #1418): want the content refusal, got %v", err)
	}
}

// Review of #1418 slice 1 (P3): a secret as the NAME of the query parameter
// of an api-key (auth_api_key_in=query) is in the request line, as content.
func TestAgentAccessSecretAsQueryParameterNameIsRefused(t *testing.T) {
	e := newContentEnv(t, secrets.AccessGrant{AnyHost: true})
	proxy := newRecorder(t, nil)
	err := e.send(map[string]interface{}{"url": "http://vendor.example.test/r", "proxy": proxy.URL, "auth_type": "api_key", "auth_api_key_in": "query",
		"auth_api_key_name": "@secret:vs", "auth_api_key_value": "x"})
	if err == nil || !strings.Contains(err.Error(), secrets.ErrPublishesSecret.Error()) {
		t.Errorf("a secret as a query parameter name (issue #1418): want the content refusal, got %v", err)
	}
	if proxy.n() != 0 {
		t.Errorf("%d request(s) reached the proxy", proxy.n())
	}
}

// A destination on this machine is exempt from the body rule, by name and by
// address; a name that merely starts or ends like one is not.
func TestAgentAccessBodyToThisMachineIsExempt(t *testing.T) {
	e := newContentEnv(t, secrets.AccessGrant{AnyHost: true})
	rec := newRecorder(t, nil)
	local := strings.Replace(rec.URL, "127.0.0.1", "localhost", 1)
	if err := e.send(map[string]interface{}{"url": local + "/r", "method": "POST", "body": "@secret:vs"}); err != nil {
		t.Errorf("a body to localhost: %v", err)
	}
	if rec.n() != 1 {
		t.Errorf("localhost received %d requests", rec.n())
	}
	for host, want := range map[string]bool{"localhost": true, "LOCALHOST": true, "127.0.0.1": true, "::1": true, "127.0.0.1.evil.com": false, "localhost.evil.com": false, "0.0.0.0": false, "vendor.example.test": false} {
		if got := ownHost(host); got != want {
			t.Errorf("ownHost(%q) = %v, want %v", host, got, want)
		}
	}
}

// Review of #1418 delta (P3): a secret written raw in the URL's query (; & +
// % in it are not what a decoded parameter name shows) reaches the request
// line by data flow and is refused; an auth value in the query is not.
func TestAgentAccessSecretRawInTheQueryIsRefused(t *testing.T) {
	e := newContentEnv(t, secrets.AccessGrant{AnyHost: true})
	for i, v := range []string{"pass+word-123", "pass;word-123", "pass&word-123", "pass%41word-123"} {
		name := "raw" + strconv.Itoa(i)
		if _, err := secrets.Add(e.base, e.db.DB, "default", "secret", name, map[string]string{"secret": v}, "", "", ""); err != nil {
			t.Fatal(err)
		}
		proxy := newRecorder(t, nil)
		// The reference resolves (it is the whole field); the URL carries the
		// value as a template over the response would.
		err := e.send(map[string]interface{}{"url": "http://vendor.example.test/r?" + v + "=1", "proxy": proxy.URL, "method": "GET", "x": "@secret:" + name})
		if err == nil || !strings.Contains(err.Error(), secrets.ErrPublishesSecret.Error()) {
			t.Errorf("%q raw in the query (issue #1418): want the content refusal, got %v", v, err)
		}
		if proxy.n() != 0 {
			t.Errorf("%q: %d request(s) reached the proxy", v, proxy.n())
		}
	}
	// The auth value in the query is not content.
	proxy := newRecorder(t, nil)
	if err := e.send(map[string]interface{}{"url": "http://vendor.example.test/r", "proxy": proxy.URL, "auth_type": "api_key", "auth_api_key_in": "query",
		"auth_api_key_name": "key", "auth_api_key_value": "@secret:vs"}); err != nil {
		t.Errorf("a query api key: %v", err)
	}
}

// Review of #1418 delta (P3): every hop is checked, a hop with no body too,
// and a 301/302/303 that turns a POST into a GET is not refused for the body
// it dropped.
func TestAgentAccessRedirectHops(t *testing.T) {
	e := newContentEnv(t, secrets.AccessGrant{AnyHost: true})
	to := func(loc string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, loc, http.StatusFound) }))
	}
	// A GET hop whose address carries the secret as a query name.
	hop := to("http://vendor.example.test/r?vsecret-777=1")
	defer hop.Close()
	err := e.send(map[string]interface{}{"url": hop.URL + "/r", "method": "GET", "follow_redirects": true, "x": "@secret:vs"})
	if err == nil || !strings.Contains(err.Error(), secrets.ErrPublishesSecret.Error()) {
		t.Errorf("a redirect hop carrying the secret in its query (issue #1418): want the content refusal, got %v", err)
	}
	// 302 turns the POST into a GET: the body is not sent again, so the hop is
	// not refused (the dial then fails: no such host here).
	plain := to("http://vendor.example.test/r")
	defer plain.Close()
	err = e.send(map[string]interface{}{"url": plain.URL + "/r", "method": "POST", "body": "@secret:vs", "follow_redirects": true})
	if err != nil && strings.Contains(err.Error(), secrets.ErrPublishesSecret.Error()) {
		t.Errorf("a 302 hop that drops the body was refused for it: %v", err)
	}
}
