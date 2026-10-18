package mcp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/secrets"
)

// An MCP caller is an AI: node_run of vault.secret_get, whose output is a
// vault entry's decrypted fields, is refused, and the value never comes back
// (the secret tools return names and metadata only).
func TestNodeRunRefusesToDecryptAVaultEntry(t *testing.T) {
	keyring.MockInit()
	s := newTestServerAllowMutations(t, true)
	rt, err := s.runtime()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.Add(context.Background(), rt.db.DB, testProfile, "secret", "kept", map[string]string{"secret": "vsecret-777"}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	text, err := callNamed(t, s, "node_run", map[string]interface{}{"type": "vault.secret_get", "config": map[string]interface{}{"name": "kept"}})
	if err == nil || !strings.Contains(err.Error(), "vault.secret_get decrypts a vault entry") || strings.Contains(text, "vsecret-777") {
		t.Fatalf("node_run vault.secret_get: %v\n%s", err, text)
	}
	// There is no workflow to approve: the refusal says to make one.
	if !strings.Contains(err.Error(), "put the step in a workflow and have the user save or activate it") || strings.Contains(err.Error(), "since you last saved") {
		t.Errorf("node_run's refusal: %v", err)
	}
	if list, err := callNamed(t, s, "secret_list", map[string]interface{}{}); err != nil || !strings.Contains(list, "kept") || strings.Contains(list, "vsecret-777") {
		t.Fatalf("secret_list: %v\n%s", err, list)
	}
}

// Nor does an @secret reference in a node_run step resolve: the step is
// refused, and nothing is sent where the AI pointed it.
func TestNodeRunResolvesNoSecretReference(t *testing.T) {
	keyring.MockInit()
	s := newTestServerAllowMutations(t, true)
	rt, err := s.runtime()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.Add(context.Background(), rt.db.DB, testProfile, "secret", "vs", map[string]string{"secret": "vsecret-777"}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, string(b))
		mu.Unlock()
	}))
	defer srv.Close()
	text, err := callNamed(t, s, "node_run", map[string]interface{}{"type": "http.request",
		"config": map[string]interface{}{"url": srv.URL, "method": "POST", "body": "@secret:vs"}})
	if err != nil {
		text += err.Error()
	}
	if !strings.Contains(text, "@secret:vs is not read from the vault") || strings.Contains(text, "vsecret-777") ||
		!strings.Contains(text, "node_run runs a step an AI wrote") || strings.Contains(text, "since you last saved") {
		t.Errorf("node_run http.request with a secret reference:\n%s", text)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 0 {
		t.Fatalf("node_run sent %q", got)
	}
}
