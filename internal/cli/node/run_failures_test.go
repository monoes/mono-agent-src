package node

import (
	"strings"
	"testing"
)

// A database that does not open is an error, not a run without vault, credentials or publication record.
func TestNodeRunFailsWhenTheDatabaseDoesNotOpen(t *testing.T) {
	asTheOperator(t)
	_, err := runNodeSubcmdStdin(t, testDeps("", "default", false), `{"config":{"url":"http://127.0.0.1:9"}}`, "http.request")
	if err == nil || !strings.Contains(err.Error(), "no database in this test") {
		t.Fatalf("node run with an unopenable database: %v", err)
	}
}

// A credential the config names that does not resolve stops the run, as --credential does.
func TestNodeRunFailsOnAnUnresolvedConfigCredential(t *testing.T) {
	dbPath := nodeVaultTestDB(t)
	srv, got := bodyRecorder(t)
	payload := `{"config":{"url":"` + srv.URL + `","method":"POST","body":"x","credential_id":"missing-conn"}}`
	_, err := runNodeSubcmdStdin(t, testDeps(dbPath, "default", false), payload, "http.request")
	if err == nil || !strings.Contains(err.Error(), "credential resolution failed") {
		t.Fatalf("node run with an unresolvable config credential_id: %v", err)
	}
	if b := got(); len(b) != 0 {
		t.Fatalf("the node ran without its credential and sent %q", b)
	}
}
