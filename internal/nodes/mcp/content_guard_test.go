package mcpnode

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
)

func TestLoopbackURL(t *testing.T) {
	for url, want := range map[string]bool{
		"http://localhost/mcp":                  true,
		"http://LOCALHOST:8080/mcp":             true,
		"http://localhost./mcp":                 true,
		"http://127.0.0.1:9/mcp":                true,
		"http://127.4.5.6/mcp":                  true,
		"http://[::1]:9/mcp":                    true,
		"http://127.0.0.1.evil.com/mcp":         false,
		"http://localhost.evil.com/mcp":         false,
		"http://0.0.0.0/mcp":                    false,
		"http://vendor.example.test/mcp":        false,
		"http://user@vendor.example.test/mcp":   false,
		"http://127.0.0.1@vendor.example.test/": false,
		"not a url%%":                           false,
		"":                                      false,
	} {
		if got := loopbackURL(url); got != want {
			t.Errorf("loopbackURL(%q) = %v, want %v", url, got, want)
		}
	}
}

// Issue #1418: a tool call's arguments are content. A granted secret in
// them reaches a remote server only as its headers; a server on this
// machine is exempt; a stdio server is a program on it.
func TestAgentAccessSecretInToolArgumentsIsRefusedForARemoteServer(t *testing.T) {
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
	if err := secrets.SetAgentAccessGrant(base, db.DB, "default", secrets.AccessGrant{AnyHost: true}); err != nil {
		t.Fatal(err)
	}
	ctx := secrets.WithAccessNode(secrets.AllowResolving(base, db.DB, "default", secrets.AccessOrigin{WorkflowID: "wf"}), "n1", "Tool")
	if _, err := secrets.Resolve(ctx, db.DB, "default", "@secret:vs"); err != nil {
		t.Fatal(err)
	}
	args := map[string]interface{}{"note": "token is " + value}

	remote := &serverConfig{Transport: "http", URL: "https://vendor.example.test/mcp"}
	if err := checkArgsContent(ctx, remote, args); !errors.Is(err, secrets.ErrPublishesSecret) {
		t.Errorf("a secret in the arguments to a remote server: %v", err)
	}
	if err := checkArgsContent(ctx, remote, map[string]interface{}{"note": "hello"}); err != nil {
		t.Errorf("arguments without the secret: %v", err)
	}
	for _, u := range []string{"http://localhost:9/mcp", "http://127.0.0.1:9/mcp", "http://[::1]:9/mcp"} {
		if err := checkArgsContent(ctx, &serverConfig{Transport: "http", URL: u}, args); err != nil {
			t.Errorf("%s is this machine: %v", u, err)
		}
	}
	if err := checkArgsContent(ctx, &serverConfig{Transport: "stdio", Command: "x"}, args); err != nil {
		t.Errorf("a stdio server: %v", err)
	}
	if err := checkArgsContent(base, remote, args); err != nil {
		t.Errorf("a run under no agent access: %v", err)
	}
}
