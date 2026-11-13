package httpnodes

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// Issue #1418 (P1, red until the guard lands): host binding says where a
// granted secret may go, never what it is used as. Under agent access a
// granted secret may reach an http node only in an auth surface; in the body
// of a request to a granted vendor host it would be published as content.
// A host on this machine is exempt (the value does not leave it).
//
// The vendor host is a name that is not this machine, reached through a
// proxy that is: the grant names both, so the host check passes and the
// request lands on the recorder.
func TestAgentAccessGrantedSecretIsNotPublishedAsBodyContent(t *testing.T) {
	keyring.MockInit()
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	base := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db.DB), "default")
	const value = "vsecret-777"
	if _, err := secrets.Add(base, db.DB, "default", "secret", "vs", map[string]string{"secret": value}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	rec := newRecorder(t, nil)
	if err := secrets.SetAgentAccessGrant(base, db.DB, "default", secrets.AccessGrant{Hosts: []string{"vendor.example.test", "127.0.0.1"}}); err != nil {
		t.Fatal(err)
	}
	// send runs one http.request the way RunExecution does: the whole config
	// is resolved against the stored references first, then the node runs.
	send := func(url string, extra map[string]interface{}) error {
		ctx := secrets.WithAccessNode(secrets.AllowResolving(base, db.DB, "default", secrets.AccessOrigin{WorkflowID: "wf"}), "n1", "Http")
		cfg := map[string]interface{}{"url": url, "method": "POST", "proxy": rec.URL}
		for k, v := range extra {
			cfg[k] = v
		}
		stored := map[string]interface{}{}
		for k, v := range cfg {
			stored[k] = v
		}
		if err := secrets.ResolveConfig(ctx, db.DB, "default", stored, cfg); err != nil {
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
	count := func() int {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		return len(rec.reqs)
	}

	// Control: the auth surface of a granted host still works.
	if err := send("http://vendor.example.test/r", map[string]interface{}{"auth_type": "bearer", "auth_token": "@secret:vs"}); err != nil {
		t.Fatalf("control: a secret as bearer auth to a granted host: %v", err)
	}
	if count() != 1 {
		t.Fatalf("control: the bearer request did not reach the recorder (%d)", count())
	}
	before := count()

	// The body of a request to a granted vendor host is content.
	err = send("http://vendor.example.test/r", map[string]interface{}{"body": "@secret:vs"})
	if err == nil {
		t.Errorf("a granted secret was sent as the body of a request to a granted vendor host (issue #1418, P1): want a refusal, got none")
	}
	if n := count() - before; n != 0 {
		t.Errorf("%d request(s) carrying the secret as body content reached the host; the refusal must come before anything is sent", n)
	}
}
