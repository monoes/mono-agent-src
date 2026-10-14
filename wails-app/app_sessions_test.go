//go:build !windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// connFakeCLI installs a fake monoagentcli that logs each argv to args.log
// and each stdin to stdin.log, then runs body; it returns both log paths.
func connFakeCLI(t *testing.T, body string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "args.log")
	stdinLog := filepath.Join(dir, "stdin.log")
	t.Setenv("MONOAGENTCLI_BIN", fakeCLI(t, `echo "$*" >> '`+argsLog+`'
cat >> '`+stdinLog+`'
`+body))
	return argsLog, stdinLog
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

func newConnTestApp(t *testing.T) *App {
	t.Helper()
	a := newTestApp(t)
	a.ctx = context.Background()
	a.setActiveProfileID("work")
	return a
}

func TestSessionBindingsShellOut(t *testing.T) {
	argsLog, _ := connFakeCLI(t, `case "$*" in
  *"login status"*) echo '[{"id":7,"username":"me","platform":"linkedin","expiry":"2026-10-01T12:00:00Z","when_added":"2026-09-01T12:00:00Z","status":"active"},
    {"id":8,"username":"me","platform":"x","expiry":"2026-09-01T12:00:00Z","when_added":"2026-08-01T12:00:00Z","status":"expired"},
    {"username":"","platform":"producthunt","status":"logged_out"}]' ;;
  *"login test 7"*) echo '{"status":"ok"}' ;;
  *"login test 8"*) echo 'warning: something unrelated' >&2; echo 'session expired' >&2; exit 4 ;;
  *"login delete 7"*) echo '{"id":7,"deleted":true}' ;;
  *"login delete 9"*) echo 'session 9 not found' >&2; exit 2 ;;
esac
`)
	a := newConnTestApp(t)

	got := a.GetSessions()
	if len(got) != 2 {
		t.Fatalf("GetSessions = %+v, want the two sessions (no logged_out row)", got)
	}
	if got[0] != (SessionInfo{ID: 7, Username: "me", Platform: "linkedin", Expiry: "2026-10-01T12:00:00Z", AddedAt: "2026-09-01T12:00:00Z", Active: true}) || got[1].Active {
		t.Fatalf("GetSessions = %+v", got)
	}
	if r := a.TestSession(7); r != "ok" {
		t.Fatalf("TestSession(7) = %q", r)
	}
	if r := a.TestSession(8); r != "error: session expired" {
		t.Fatalf("TestSession(8) = %q", r)
	}
	// DeleteSession's CLI half (the binding then logs, which needs the
	// Wails runtime context).
	if err := a.deleteSession(7); err != nil {
		t.Fatalf("deleteSession(7) = %v", err)
	}
	if err := a.deleteSession(9); err == nil || err.Error() != "session 9 not found" {
		t.Fatalf("deleteSession(9) = %v", err)
	}

	want := []string{
		"--profile work --json login status",
		"--profile work --json login test 7",
		"--profile work --json login test 8",
		"--profile work --json login delete 7",
		"--profile work --json login delete 9",
	}
	if got := loggedArgs(t, argsLog); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("CLI calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestGetSessionsSurvivesCLIFailure(t *testing.T) {
	connFakeCLI(t, "echo boom >&2; exit 1\n")
	if got := newConnTestApp(t).GetSessions(); got != nil {
		t.Fatalf("GetSessions = %+v, want nil", got)
	}
}

func TestConnectionBindingsShellOut(t *testing.T) {
	argsLog, _ := connFakeCLI(t, `case "$*" in
  *"connect list --platform gmail"*) echo '[{"id":"g1","platform":"gmail","method":"oauth","label":"Gmail","account_id":"a@b","status":"active","created_at":"","updated_at":""}]' ;;
  *"connect list --platform"*) echo '[]' ;;
  *"connect list"*) echo '[{"id":"c1","platform":"github","method":"apikey","label":"GitHub","account_id":"me","status":"active","created_at":"x","updated_at":"y"}]' ;;
  *"connect for-node"*) echo '[{"id":"c1","label":"GitHub","platform":"github","method":"apikey"}]' ;;
  *"connect test c-ok"*) echo '{"status":"ok"}' ;;
  *"connect test c-bad"*) echo 'connection test failed: 401 unauthorized' >&2; exit 4 ;;
  *"connect test"*) echo 'connection not found' >&2; exit 2 ;;
  *"login test 42"*) echo '{"status":"ok"}' ;;
  *"login test"*) echo 'session not found' >&2; exit 2 ;;
  *"connect remove c1"*) echo '{"id":"c1","removed":true}' ;;
  *"connect remove"*) echo 'connection "zz" not found' >&2; exit 2 ;;
esac
`)
	a := newConnTestApp(t)

	if got := a.ListConnections(""); len(got) != 1 || got[0].ID != "c1" || got[0].AccountID != "me" {
		t.Fatalf("ListConnections = %+v", got)
	}
	if got := a.GetConnectionsForPlatform("gmail"); len(got) != 1 || got[0].ID != "g1" {
		t.Fatalf("GetConnectionsForPlatform = %+v", got)
	}
	if got := a.ListCredentialsForNode("service.github"); len(got) != 1 || got[0] != (CredentialOption{ID: "c1", Label: "GitHub", Platform: "github", Method: "apikey"}) {
		t.Fatalf("ListCredentialsForNode = %+v", got)
	}
	for id, want := range map[string]string{
		"c-ok":  "ok",
		"c-bad": "error: connection test failed: 401 unauthorized",
		"42":    "ok",                          // a browser session id
		"43":    "error: connection not found", // neither, and no active connection
		"gmail": "ok",                          // a platform with an active connection
	} {
		if got := a.TestConnection(id); got != want {
			t.Errorf("TestConnection(%q) = %q, want %q", id, got, want)
		}
	}
	if got := a.RemoveConnection("c1"); got != "ok" {
		t.Fatalf("RemoveConnection(c1) = %q", got)
	}
	if got := a.RemoveConnection("zz"); got != "error: connection not found" {
		t.Fatalf("RemoveConnection(zz) = %q", got)
	}

	got := strings.Join(loggedArgs(t, argsLog), "\n")
	for _, want := range []string{
		"--profile work --json connect list",
		"--profile work --json connect list --platform gmail",
		"--profile work --json connect for-node service.github",
		"--profile work --json connect test c-ok",
		"--profile work --json connect test c-bad",
		"--profile work --json login test 42",
		"--profile work --json login test 43",
		"--profile work --json connect list --platform 43",
		"--profile work --json connect remove c1",
	} {
		if !strings.Contains(got+"\n", want+"\n") {
			t.Errorf("missing CLI call %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "login test c-bad") || strings.Contains(got, "login test gmail") {
		t.Errorf("a non-numeric id was tried as a session:\n%s", got)
	}
}

// Secrets (connection fields, the OAuth client secret) reach the CLI on
// stdin and never appear in its argv.
func TestConnectionSecretsTravelOnStdin(t *testing.T) {
	argsLog, stdinLog := connFakeCLI(t, `case "$*" in
  *"connect save"*) echo '{"id":"new-1","platform":"github","method":"apikey","label":"GitHub","account_id":"","status":"active","created_at":"","updated_at":""}' ;;
  *"set-oauth-client"*) echo '{"saved":true}' ;;
  *"get-oauth-client outlook"*) echo '{"platform":"outlook","profile_id":"work","client_id":"cid","has_client_secret":true,"client_secret":"s3cret-client-value"}' ;;
  *"get-oauth-client"*) echo 'no OAuth client stored' >&2; exit 2 ;;
esac
`)
	a := newConnTestApp(t)
	const apiKey = "ghp_topsecret_api_key"
	const clientSecret = "s3cret-client-value"

	if got := a.SaveConnectionDirect("github", "apikey", `{"api_key":"`+apiKey+`"}`); got != "ok:new-1" {
		t.Fatalf("SaveConnectionDirect = %q", got)
	}
	if got := a.SaveConnectionDirect("github", "apikey", `not json`); got != "error: invalid field values JSON" {
		t.Fatalf("SaveConnectionDirect(bad JSON) = %q", got)
	}
	if got := a.SetOAuthCredentials("outlook", "cid", clientSecret); got != "ok" {
		t.Fatalf("SetOAuthCredentials = %q", got)
	}
	if got := a.SetOAuthCredentials("outlook", "cid", ""); got != "ok" {
		t.Fatalf("SetOAuthCredentials(no secret) = %q", got)
	}
	if got := a.SetOAuthCredentials("outlook", "", "x"); got != "error: clientID is required" {
		t.Fatalf("SetOAuthCredentials(no id) = %q", got)
	}
	if got := a.GetOAuthCredentials("outlook"); got != `{"clientID":"cid","clientSecret":"`+clientSecret+`"}` {
		t.Fatalf("GetOAuthCredentials = %q", got)
	}
	if got := a.GetOAuthCredentials("slack"); got != "" {
		t.Fatalf("GetOAuthCredentials(unset) = %q, want empty", got)
	}

	args := readLog(t, argsLog)
	want := []string{
		"--profile work --json connect save github --method apikey --stdin-json",
		"--profile work --json connect set-oauth-client outlook --client-id cid --client-secret-stdin",
		"--profile work --json connect set-oauth-client outlook --client-id cid",
		"--profile work --json connect get-oauth-client outlook --reveal",
		"--profile work --json connect get-oauth-client slack --reveal",
	}
	if got := strings.TrimSpace(args); got != strings.Join(want, "\n") {
		t.Fatalf("CLI calls:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
	if strings.Contains(args, apiKey) || strings.Contains(args, clientSecret) {
		t.Fatalf("a secret appeared in argv:\n%s", args)
	}
	stdin := readLog(t, stdinLog)
	if !strings.Contains(stdin, `{"api_key":"`+apiKey+`"}`) || !strings.Contains(stdin, clientSecret) {
		t.Fatalf("secrets not sent on stdin: %q", stdin)
	}
}

// The OAuth connect forwards the CLI's JSON progress lines and reports its
// last stderr line as the error.
func TestRunOAuthConnectStreamsProgress(t *testing.T) {
	a := newConnTestApp(t)
	var got []string
	emit := func(msg, kind string) { got = append(got, kind+":"+msg) }

	ok := fakeCLI(t, `echo '{"message":"Opening browser","kind":"info"}' >&2
echo '{"message":"Connected as me","kind":"success"}' >&2
echo '{"id":"o1","platform":"outlook","method":"oauth","label":"Outlook","account_id":"me@x","status":"active","created_at":"","updated_at":""}'
`)
	conn, err := runOAuthConnect(a.ctx, ok, oauthConnectArgs("work", "outlook", nil, false), "", emit)
	if err != nil || conn.AccountID != "me@x" {
		t.Fatalf("runOAuthConnect = %+v, %v", conn, err)
	}
	if strings.Join(got, "|") != "info:Opening browser|success:Connected as me" {
		t.Fatalf("progress = %v", got)
	}

	bad := fakeCLI(t, `echo '{"message":"Opening browser","kind":"info"}' >&2
echo 'missing OAuth credentials' >&2
exit 4
`)
	if _, err := runOAuthConnect(a.ctx, bad, oauthConnectArgs("work", "outlook", nil, false), "", emit); err == nil || err.Error() != "missing OAuth credentials" {
		t.Fatalf("failure err = %v", err)
	}
}
