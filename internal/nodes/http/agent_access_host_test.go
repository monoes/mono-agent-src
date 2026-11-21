package httpnodes

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// Under the owner's agent access a secret read by an unapproved run goes
// only to a host the grant names; a request that carries none is not held
// to it (review of #943, H3).
func TestAgentAccessBindsASecretToItsHosts(t *testing.T) {
	keyring.MockInit()
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	testdb.AddDefaultProfile(t, db.DB)
	base := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db.DB), "default")
	if _, err := secrets.Add(base, db.DB, "default", "secret", "vs", map[string]string{"secret": "vsecret-777"}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	rec := newRecorder(t, nil)
	cfg := func(body string) map[string]interface{} {
		return map[string]interface{}{"url": rec.URL + "/r", "method": "POST", "body": body}
	}
	run := func(grant secrets.AccessGrant, body string) error {
		if err := secrets.SetAgentAccessGrant(base, db.DB, "default", grant); err != nil {
			t.Fatal(err)
		}
		ctx := secrets.AllowResolving(base, db.DB, "default", secrets.AccessOrigin{WorkflowID: "wf"})
		outs, err := (&RequestNode{}).Execute(ctx, workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(map[string]interface{}{})}}, cfg(body))
		if err != nil {
			return err
		}
		// A failed request is routed to the error output with its message.
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

	err = run(secrets.AccessGrant{Hosts: []string{"api.example.com"}}, "@secret:vs")
	if err == nil || !strings.Contains(err.Error(), secrets.ErrHostNotAllowed.Error()) {
		t.Fatalf("a secret to a host the grant does not name: %v", err)
	}
	rec.mu.Lock()
	n := len(rec.reqs)
	rec.mu.Unlock()
	if n != 0 {
		t.Fatalf("the request was sent: %d", n)
	}
	if err := run(secrets.AccessGrant{Hosts: []string{"api.example.com"}}, "no secret here"); err != nil {
		t.Errorf("a request with no secret was held to the hosts: %v", err)
	}
	if err := run(secrets.AccessGrant{Hosts: []string{"127.0.0.1"}}, "@secret:vs"); err != nil {
		t.Fatalf("a secret to a granted host: %v", err)
	}
	if got := rec.last(t).Body; got != "vsecret-777" {
		t.Errorf("the granted host received %q", got)
	}
	if err := run(secrets.AccessGrant{AnyHost: true}, "@secret:vs"); err != nil {
		t.Errorf("--any-host: %v", err)
	}
	if err := run(secrets.AccessGrant{Hosts: []string{"127.0.0.1"}}, "@secret:vs"); err != nil {
		t.Errorf("again: %v", err)
	}
}

// A proxy is a second destination: a secret may not ride a request through
// a proxy the grant does not name, nor with certificate checks off (review
// of #944, A1).
func TestAgentAccessRefusesProxyAndInsecureForASecret(t *testing.T) {
	keyring.MockInit()
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	testdb.AddDefaultProfile(t, db.DB)
	base := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db.DB), "default")
	if _, err := secrets.Add(base, db.DB, "default", "secret", "vs", map[string]string{"secret": "vsecret-777"}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	dest := newRecorder(t, nil)
	evil := newRecorder(t, nil)
	if err := secrets.SetAgentAccessGrant(base, db.DB, "default", secrets.AccessGrant{Hosts: []string{"127.0.0.1"}}); err != nil {
		t.Fatal(err)
	}
	run := func(extra map[string]interface{}, body string) error {
		ctx := secrets.AllowResolving(base, db.DB, "default", secrets.AccessOrigin{WorkflowID: "wf"})
		cfg := map[string]interface{}{"url": dest.URL + "/r", "method": "POST", "body": body}
		for k, v := range extra {
			cfg[k] = v
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
	err = run(map[string]interface{}{"proxy": "http://localhost:" + evil.URL[strings.LastIndex(evil.URL, ":")+1:]}, "@secret:vs")
	if err == nil || !strings.Contains(err.Error(), secrets.ErrHostNotAllowed.Error()) {
		t.Fatalf("a secret through a proxy the grant does not name: %v", err)
	}
	if n := len(evil.reqs) + len(dest.reqs); n != 0 {
		t.Fatalf("something was sent: %d", n)
	}
	err = run(map[string]interface{}{"allow_unauthorized_certs": true}, "@secret:vs")
	if err == nil || !strings.Contains(err.Error(), secrets.ErrHostNotAllowed.Error()) {
		t.Fatalf("a secret with certificate checks off: %v", err)
	}
	if err := run(map[string]interface{}{"proxy": "http://localhost:1", "allow_unauthorized_certs": true}, "no secret"); err != nil && strings.Contains(err.Error(), secrets.ErrHostNotAllowed.Error()) {
		t.Errorf("a request with no secret was held: %v", err)
	}
}

// Review of #944 (N1): a secret in Basic credentials, bearer, API key,
// query, header or body goes only to a granted host.
func TestAgentAccessBindsEverySecretCarrierToItsHosts(t *testing.T) {
	keyring.MockInit()
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	testdb.AddDefaultProfile(t, db.DB)
	base := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db.DB), "default")
	if _, err := secrets.Add(base, db.DB, "default", "secret", "vs", map[string]string{"secret": "vsecret-777"}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	rec := newRecorder(t, nil)
	run := func(host string, extra map[string]interface{}) error {
		if err := secrets.SetAgentAccessGrant(base, db.DB, "default", secrets.AccessGrant{Hosts: []string{host}}); err != nil {
			t.Fatal(err)
		}
		ctx := secrets.AllowResolving(base, db.DB, "default", secrets.AccessOrigin{WorkflowID: "wf"})
		ctx = secrets.WithAccessNode(ctx, "n1", "Http")
		cfg := map[string]interface{}{"url": rec.URL + "/r", "method": "POST"}
		for k, v := range extra {
			cfg[k] = v
		}
		// RunExecution resolves the whole config before the node runs.
		stored := map[string]interface{}{}
		for k, v := range cfg {
			stored[k] = v
		}
		if err := secrets.ResolveConfig(ctx, db.DB, "default", stored, cfg); err != nil {
			t.Fatal(err)
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
	for name, extra := range map[string]map[string]interface{}{
		"basic":   {"auth_type": "basic", "auth_username": "user", "auth_password": "@secret:vs"},
		"bearer":  {"auth_type": "bearer", "auth_token": "@secret:vs"},
		"api_key": {"auth_type": "api_key", "auth_api_key_in": "header", "auth_api_key_name": "X-Api-Key", "auth_api_key_value": "@secret:vs"},
		"query":   {"auth_type": "api_key", "auth_api_key_in": "query", "auth_api_key_name": "key", "auth_api_key_value": "@secret:vs"},
		"body":    {"body": "@secret:vs"},
	} {
		err := run("api.example.com", extra)
		if err == nil || !strings.Contains(err.Error(), secrets.ErrHostNotAllowed.Error()) {
			t.Errorf("%s: a secret to a host the grant does not name: %v", name, err)
		}
		if err := run("127.0.0.1", extra); err != nil {
			t.Errorf("%s: a granted host: %v", name, err)
		}
	}
	if n := len(rec.reqs); n != 5 {
		t.Errorf("%d requests reached the recorder, want 5 (the granted ones)", n)
	}
	if err := run("api.example.com", map[string]interface{}{"auth_type": "basic", "auth_username": "user", "auth_password": "plain"}); err != nil {
		t.Errorf("a node that resolved no secret was held: %v", err)
	}
}

// The credentials inside a Basic token are scanned, not just the token.
func TestCarryTextsDecodeBasicCredentials(t *testing.T) {
	req, _ := http.NewRequest("GET", "http://u:p@h.test/x", nil)
	req.SetBasicAuth("user", "pw-secret")
	var found bool
	for _, s := range carryTexts(req) {
		if s == "pw-secret" || s == "user:pw-secret" {
			found = true
		}
	}
	if !found {
		t.Errorf("decoded credentials are not scanned: %v", carryTexts(req))
	}
}

// Review of #944 (F1): once any step of a run read a secret under a
// host-bound grant, every send of the run is held to the grant's hosts, so a
// transformed echo of the secret cannot be carried by another node to a
// foreign host.
func TestAgentAccessTaintFollowsDataBetweenNodes(t *testing.T) {
	keyring.MockInit()
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	testdb.AddDefaultProfile(t, db.DB)
	base := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db.DB), "default")
	if _, err := secrets.Add(base, db.DB, "default", "secret", "vs", map[string]string{"secret": "vsecret-777"}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	granted := newRecorder(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("echo")) // a host that echoes what it is sent
	})
	foreign := newRecorder(t, nil)
	foreignURL := strings.Replace(foreign.URL, "127.0.0.1", "localhost", 1)
	if err := secrets.SetAgentAccessGrant(base, db.DB, "default", secrets.AccessGrant{Hosts: []string{"127.0.0.1"}}); err != nil {
		t.Fatal(err)
	}
	send := func(ctx context.Context, node, url, body string) error {
		ctx = secrets.WithAccessNode(ctx, node, node)
		cfg := map[string]interface{}{"url": url, "method": "POST", "body": body}
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
	forms := map[string]string{
		"replace": strings.ReplaceAll("vsecret-777", "-", ""),
		"join":    strings.Join(strings.Split("vsecret-777", ""), "."),
		"upper-e": strings.ReplaceAll("vsecret-777", "e", "E"),
	}
	for name, form := range forms {
		ctx := secrets.AllowResolving(base, db.DB, "default", secrets.AccessOrigin{WorkflowID: "wf"})
		if err := send(ctx, "A", granted.URL+"/e", "@secret:vs"); err != nil {
			t.Fatalf("%s: node A: %v", name, err)
		}
		before := len(foreign.all())
		err := send(ctx, "B", foreignURL+"/x", form)
		if err == nil || !strings.Contains(err.Error(), secrets.ErrHostNotAllowed.Error()) {
			t.Errorf("%s: node B sent a transformed secret to a foreign host: %v", name, err)
		}
		if n := len(foreign.all()); n != before {
			t.Errorf("%s: the foreign host received %d request(s)", name, n-before)
		}
	}
	// A run that read no secret may send anywhere; so may --any-host.
	clean := secrets.AllowResolving(base, db.DB, "default", secrets.AccessOrigin{WorkflowID: "wf"})
	if err := send(clean, "B", foreignURL+"/x", "plain"); err != nil {
		t.Errorf("a run that read no secret was held: %v", err)
	}
	if err := secrets.SetAgentAccessGrant(base, db.DB, "default", secrets.AccessGrant{AnyHost: true}); err != nil {
		t.Fatal(err)
	}
	anyHost := secrets.AllowResolving(base, db.DB, "default", secrets.AccessOrigin{WorkflowID: "wf"})
	if err := send(anyHost, "A", granted.URL+"/e", "@secret:vs"); err != nil {
		t.Fatal(err)
	}
	if err := send(anyHost, "B", foreignURL+"/x", "x"); err != nil {
		t.Errorf("--any-host: %v", err)
	}
}
