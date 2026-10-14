package connect

import (
	"encoding/json"
	"strings"
	"testing"
)

const testConnString = "postgres://u:hunter2-very-secret@localhost:5432/db"

// connect save reads the fields from stdin, prints no credential, and the
// connection is then listed, tested, found for a node and removed — all
// within the active profile.
func TestConnectSaveListTestRemove(t *testing.T) {
	e := newSessionsTestEnv(t)
	out, errOut, code := e.run(t, `{"connection_string":"`+testConnString+`"}`,
		"--profile", "work", "--json", "connect", "save", "postgresql", "--method", "connstring", "--stdin-json")
	if code != 0 {
		t.Fatalf("save: code %d, stderr %s", code, errOut)
	}
	if strings.Contains(out+errOut, "hunter2-very-secret") {
		t.Fatalf("save printed the credential:\n%s\n%s", out, errOut)
	}
	var saved map[string]any
	if err := json.Unmarshal([]byte(out), &saved); err != nil {
		t.Fatalf("save stdout not JSON: %q", out)
	}
	id, _ := saved["id"].(string)
	if id == "" || saved["platform"] != "postgresql" || saved["profile_id"] != "work" {
		t.Fatalf("saved = %v", saved)
	}
	if _, ok := saved["data"]; ok {
		t.Fatal("save output carries the credential data")
	}

	var list []map[string]any
	out, _, _ = e.run(t, "", "--profile", "work", "--json", "connect", "list", "--platform", "postgresql")
	if err := json.Unmarshal([]byte(out), &list); err != nil || len(list) != 1 || list[0]["id"] != id {
		t.Fatalf("list(work) = %q", out)
	}
	out, _, _ = e.run(t, "", "--profile", "default", "--json", "connect", "list")
	if strings.TrimSpace(out) != "[]" {
		t.Fatalf("list(default) = %q, want []", out)
	}

	var opts []map[string]any
	out, _, _ = e.run(t, "", "--profile", "work", "--json", "connect", "for-node", "db.postgresql.query")
	if err := json.Unmarshal([]byte(out), &opts); err != nil {
		t.Fatalf("for-node stdout = %q", out)
	}
	// No platform named in db.postgresql.query: every connection is a candidate.
	if len(opts) != 1 || opts[0]["id"] != id || opts[0]["method"] != "connstring" {
		t.Fatalf("for-node = %v", opts)
	}
	out, _, _ = e.run(t, "", "--profile", "work", "--json", "connect", "for-node", "action.instagram.publish_post")
	if strings.TrimSpace(out) != "[]" {
		t.Fatalf("for-node instagram = %q, want []", out)
	}

	// Another profile can't test or remove it.
	if _, _, code := e.run(t, "", "--profile", "default", "--json", "connect", "test", id); code != 2 {
		t.Fatalf("cross-profile test: code %d, want 2", code)
	}
	if _, _, code := e.run(t, "", "--profile", "default", "--json", "connect", "remove", id); code != 2 {
		t.Fatalf("cross-profile remove: code %d, want 2", code)
	}

	out, errOut, code = e.run(t, "", "--profile", "work", "--json", "connect", "test", id)
	var tested map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &tested) != nil || tested["status"] != "ok" {
		t.Fatalf("test: code %d, out %q, stderr %q", code, out, errOut)
	}
	out, _, code = e.run(t, "", "--profile", "work", "--json", "connect", "remove", id)
	if code != 0 || !strings.Contains(out, `"removed": true`) {
		t.Fatalf("remove: code %d, out %q", code, out)
	}
	if _, _, code := e.run(t, "", "--profile", "work", "--json", "connect", "test", id); code != 2 {
		t.Fatalf("test after remove: code %d, want 2", code)
	}
}

func TestConnectSaveRejectsBadInput(t *testing.T) {
	e := newSessionsTestEnv(t)
	for _, tc := range []struct {
		stdin string
		args  []string
	}{
		{`{}`, []string{"connect", "save", "nope", "--method", "apikey", "--stdin-json"}},
		{`{}`, []string{"connect", "save", "postgresql", "--stdin-json"}},
		{`{}`, []string{"connect", "save", "postgresql", "--method", "connstring"}},
		{`not json hunter2-very-secret`, []string{"connect", "save", "postgresql", "--method", "connstring", "--stdin-json"}},
	} {
		out, errOut, code := e.run(t, tc.stdin, append([]string{"--json"}, tc.args...)...)
		if code != 3 {
			t.Errorf("%v: code %d, want 3 (stderr %q)", tc.args, code, errOut)
		}
		if strings.Contains(out+errOut, "hunter2-very-secret") {
			t.Errorf("%v: echoed the payload: %q", tc.args, errOut)
		}
	}
}

// The OAuth client secret goes in on stdin, is stored encrypted, and comes
// back out only with --reveal.
func TestConnectOAuthClientRoundTrip(t *testing.T) {
	e := newSessionsTestEnv(t)
	const secret = "oauth-client-secret-xyz"

	if _, _, code := e.run(t, "", "--profile", "work", "--json", "connect", "get-oauth-client", "google_sheets"); code != 2 {
		t.Fatalf("get before set: code %d, want 2", code)
	}
	out, errOut, code := e.run(t, secret+"\n", "--profile", "work", "--json", "connect", "set-oauth-client", "google_sheets",
		"--client-id", "cid-123", "--client-secret-stdin")
	if code != 0 || strings.Contains(out+errOut, secret) {
		t.Fatalf("set: code %d, out %q, stderr %q", code, out, errOut)
	}
	var stored string
	_ = e.db.DB.QueryRow(`SELECT client_secret FROM platform_oauth_credentials WHERE platform = 'google_sheets' AND profile_id = 'work'`).Scan(&stored)
	if stored == "" || strings.Contains(stored, secret) {
		t.Fatalf("client secret stored as %q, want it encrypted", stored)
	}

	var view map[string]any
	out, _, _ = e.run(t, "", "--profile", "work", "--json", "connect", "get-oauth-client", "google_sheets")
	if err := json.Unmarshal([]byte(out), &view); err != nil || view["client_id"] != "cid-123" || view["has_client_secret"] != true {
		t.Fatalf("get = %q", out)
	}
	if strings.Contains(out, secret) {
		t.Fatal("get printed the secret without --reveal")
	}
	out, _, _ = e.run(t, "", "--profile", "work", "connect", "get-oauth-client", "google_sheets")
	if strings.Contains(out, secret) {
		t.Fatal("text get printed the secret without --reveal")
	}
	out, _, _ = e.run(t, "", "--profile", "work", "--json", "connect", "get-oauth-client", "google_sheets", "--reveal")
	if err := json.Unmarshal([]byte(out), &view); err != nil || view["client_secret"] != secret {
		t.Fatalf("get --reveal = %q", out)
	}

	// Scoped to the profile.
	if _, _, code := e.run(t, "", "--profile", "default", "--json", "connect", "get-oauth-client", "google_sheets"); code != 2 {
		t.Fatalf("other profile: code %d, want 2", code)
	}
	if _, _, code := e.run(t, "x", "--json", "connect", "set-oauth-client", "google_sheets", "--client-id", "c",
		"--client-secret", "a", "--client-secret-stdin"); code != 3 {
		t.Fatalf("both secret flags: code %d, want 3", code)
	}
}

func TestNodeCredentialPlatform(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"action.instagram.publish_post": "instagram",
		"action.x.post":                 "x",
		"service.dropbox":               "", // contains an "x" but names no platform
		"service.google_sheets":         "google_sheets",
		"service.gmail.send":            "gmail",
		"db.postgres":                   "",
	} {
		if got := nodeCredentialPlatform(in); got != want {
			t.Errorf("nodeCredentialPlatform(%q) = %q, want %q", in, got, want)
		}
	}
}
