package node

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/storage"
)

// seedVaultSecret stores secret name=value in the database at dbPath.
func seedVaultSecret(t *testing.T, dbPath, name, value string) {
	t.Helper()
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.Add(context.Background(), db.DB, "default", "secret", name, map[string]string{"secret": value}, "", "", ""); err != nil {
		t.Fatal(err)
	}
}

// bodyRecorder is a listener that keeps every request body it gets.
func bodyRecorder(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), bodies...)
	}
}

// `node run` from an agent's shell runs its step as one of a workflow the
// user hasn't approved: an @secret reference in it resolves to nothing, the
// step is refused, and nothing is sent. The user's own `node run` resolves
// and sends it.
func TestNodeRunResolvesSecretsForTheOperatorOnly(t *testing.T) {
	dbPath := nodeVaultTestDB(t)
	seedVaultSecret(t, dbPath, "vs", "vsecret-777")
	srv, got := bodyRecorder(t)
	payload := `{"config":{"url":"` + srv.URL + `","method":"POST","body":"@secret:vs"}}`
	d := testDeps(dbPath, "default", false)

	t.Setenv("CLAUDECODE", "1")
	// http.request reports a refused item on its error output.
	out, err := runNodeSubcmdStdin(t, d, payload, "http.request")
	if err != nil {
		out += err.Error()
	}
	if !strings.Contains(out, "@secret:vs is not read from the vault") || strings.Contains(out, "vsecret-777") ||
		!strings.Contains(out, "node run runs a step an AI wrote") || strings.Contains(out, "since you last saved") {
		t.Errorf("an agent's node run:\n%s", out)
	}
	if b := got(); len(b) != 0 {
		t.Fatalf("an agent's node run sent %q", b)
	}

	t.Setenv("CLAUDECODE", "")
	if out, err := runNodeSubcmdStdin(t, d, payload, "http.request"); err != nil {
		t.Fatalf("the user's node run: %v\n%s", err, out)
	}
	if b := got(); len(b) != 1 || b[0] != "vsecret-777" {
		t.Fatalf("the user's node run sent %q, want the secret", b)
	}
}
