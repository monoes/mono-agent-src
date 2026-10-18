package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/secrets"
)

// No MCP caller writes httpapi-token, the HTTP API's bearer: secret_add,
// secret_update and secret_delete refuse it, and the entry is as the server
// made it. Other entries stay writable (#845 security review).
func TestNoToolWritesTheHTTPAPIToken(t *testing.T) {
	s, dbPath := newAPIKeyServer(t, true)
	side := sideDB(t, dbPath).DB
	ctx := context.Background()
	token, err := secrets.Add(secrets.AllowReservedWrite(ctx), side, "default", "secret", secrets.ReservedEntry, map[string]string{"token": "server-made"}, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	other, err := secrets.Add(ctx, side, "default", "secret", "openai", map[string]string{"secret": "sk-1"}, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string]map[string]any{
		"secret_add":    {"kind": "secret", "name": "httpapi-token", "fields": map[string]any{"token": "agent-made"}},
		"secret_update": {"id": token, "fields": map[string]any{"token": "agent-made"}},
		"secret_delete": {"id": token},
	} {
		if out, err := callAPITool(t, s, name, args); err == nil || !strings.Contains(err.Error(), secrets.ErrReservedEntry.Error()) {
			t.Errorf("%s of httpapi-token: %q, %v", name, out, err)
		}
	}
	if out, err := callAPITool(t, s, "secret_update", map[string]any{"id": other, "name": "httpapi-token"}); err == nil || !strings.Contains(err.Error(), secrets.ErrReservedEntry.Error()) {
		t.Errorf("renaming another entry to httpapi-token: %q, %v", out, err)
	}
	fields, _, err := secrets.DecryptFields(ctx, side, "default", token)
	if err != nil || fields["token"] != "server-made" {
		t.Errorf("the token after the tools: %v, %v", fields, err)
	}
	if _, err := callAPITool(t, s, "secret_update", map[string]any{"id": other, "fields": map[string]any{"secret": "sk-2"}}); err != nil {
		t.Errorf("another entry stays writable: %v", err)
	}
}
