package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/connections"
	"github.com/monoes/mono-agent/internal/resources"
)

// Fixture credential values; none may ever appear in the command's output.
const (
	resAccess  = "ya29.fixture-access-7c41d0"
	resRefresh = "1//fixture-refresh-5e92ab"
	resRenewed = "ya29.fixture-renewed-3f18cc"
	resSlack   = "xoxb-fixture-slack-8d27e4"
)

// fakeProviders serves the Drive, Sheets, Gmail and Slack endpoints the
// resource picker uses, accepting only "Bearer <*want>", and points
// newResourcesClient at it. It returns the requests it saw.
func fakeProviders(t *testing.T, want *string) *[]string {
	t.Helper()
	var seen []string
	mux := http.NewServeMux()
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			seen = append(seen, r.Method+" "+r.URL.Path)
			if r.Header.Get("Authorization") != "Bearer "+*want {
				// Echo the rejected credential, as some providers do, so the
				// tests prove the CLI scrubs it.
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprintf(w, `{"error":{"code":401,"message":"Invalid Credentials: %s"}}`, r.Header.Get("Authorization"))
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("/drive/files", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["mimeType"] != "application/vnd.google-apps.folder" {
				http.Error(w, "bad mimeType", http.StatusBadRequest)
				return
			}
			fmt.Fprintf(w, `{"id":"fold-new","name":%q}`, body["name"])
			return
		}
		q := r.URL.Query().Get("q")
		if strings.Contains(q, "google-apps.folder") {
			fmt.Fprint(w, `{"files":[{"id":"fold1","name":"Reports","modifiedTime":"2026-09-01T10:00:00Z"}]}`)
			return
		}
		if !strings.Contains(q, "google-apps.spreadsheet") || !strings.Contains(q, `name contains 'Q3 \'plan\''`) {
			http.Error(w, "unexpected q: "+q, http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, `{"files":[{"id":"sheet1","name":"Q3 'plan'","modifiedTime":"2026-09-02T10:00:00Z"}]}`)
	}))
	mux.HandleFunc("/sheets/spreadsheets", auth(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Properties struct {
				Title string `json:"title"`
			} `json:"properties"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprintf(w, `{"spreadsheetId":"sheet-new","properties":{"title":%q}}`, body.Properties.Title)
	}))
	mux.HandleFunc("/gmail/users/me/labels", auth(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"labels":[{"id":"INBOX","name":"INBOX"},{"id":"Label_1","name":"Clients"}]}`)
	}))
	mux.HandleFunc("/slack/conversations.list", auth(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true,"channels":[{"id":"C1","name":"general"}]}`)
	}))
	mux.HandleFunc("/slack/users.list", auth(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true,"members":[{"id":"U1","name":"ada","profile":{"real_name":"Ada Lovelace"}},{"id":"U2","name":"bob","profile":{}}]}`)
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	prev := newResourcesClient
	newResourcesClient = func() *resources.Client {
		return &resources.Client{HTTP: srv.Client(), Endpoints: resources.Endpoints{
			Drive: srv.URL + "/drive", Sheets: srv.URL + "/sheets", Gmail: srv.URL + "/gmail", Slack: srv.URL + "/slack",
		}}
	}
	t.Cleanup(func() { newResourcesClient = prev })
	return &seen
}

func (e *sessionsTestEnv) seedConnection(t *testing.T, profile, platform string, data map[string]interface{}) string {
	t.Helper()
	store := connections.NewStore(e.db.DB)
	c := &connections.Connection{Platform: platform, Method: connections.MethodOAuth, ProfileID: profile, Data: data}
	if err := store.Save(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	return c.ID
}

// runFlagsFirst is run with --db-path ahead of args, so args may end in
// "-- <credential-id>" the way the desktop app calls the command.
func (e *sessionsTestEnv) runFlagsFirst(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	root := newRootCmd()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(append([]string{"--db-path", e.dbPath}, args...))
	err := root.Execute()
	if err != nil {
		errOut.WriteString(err.Error())
	}
	return out.String(), errOut.String(), exitCodeFor(err)
}

func assertNoSecrets(t *testing.T, out, errOut string) {
	t.Helper()
	for _, s := range []string{resAccess, resRefresh, resRenewed, resSlack} {
		if strings.Contains(out+errOut, s) {
			t.Fatalf("output leaks %q:\nstdout: %s\nstderr: %s", s, out, errOut)
		}
	}
}

func TestConnectResourcesListsEachType(t *testing.T) {
	e := newSessionsTestEnv(t)
	want := resAccess
	fakeProviders(t, &want)
	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	google := e.seedConnection(t, "work", "google_sheets", map[string]interface{}{
		"access_token": resAccess, "refresh_token": resRefresh, "expires_at": future,
	})
	slack := e.seedConnection(t, "work", "slack", map[string]interface{}{"bot_token": resSlack})

	cases := []struct {
		name, cred, bearer string
		args               []string
		want               string
	}{
		{"sheets", google, resAccess, []string{"--platform", "google_sheets", "--type", "spreadsheets", "--query=Q3 'plan'"},
			`{"items":[{"id":"sheet1","metadata":{"modified_time":"2026-09-02T10:00:00Z"},"name":"Q3 'plan'"}]}`},
		{"drive folders", google, resAccess, []string{"--platform", "google_drive", "--type", "folders"},
			`{"items":[{"id":"fold1","metadata":{"modified_time":"2026-09-01T10:00:00Z"},"name":"Reports"}]}`},
		{"gmail labels", google, resAccess, []string{"--platform", "gmail", "--type", "labels"},
			`{"items":[{"id":"INBOX","name":"INBOX"},{"id":"Label_1","name":"Clients"}]}`},
		{"slack channels", slack, resSlack, []string{"--platform", "slack", "--type", "channels"},
			`{"items":[{"id":"C1","name":"#general"}]}`},
		{"slack users", slack, resSlack, []string{"--platform", "slack", "--type", "users"},
			`{"items":[{"id":"U1","name":"Ada Lovelace"},{"id":"U2","name":"bob"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want = tc.bearer
			args := append([]string{"--profile", "work", "--json", "connect", "resources"}, tc.args...)
			out, errOut, code := e.runFlagsFirst(t, append(args, "--", tc.cred)...)
			if code != 0 {
				t.Fatalf("code %d, stderr %s", code, errOut)
			}
			assertNoSecrets(t, out, errOut)
			if got := compactJSON(t, out); got != tc.want {
				t.Fatalf("stdout = %s\nwant     %s", got, tc.want)
			}
		})
	}
}

func TestConnectResourcesCreate(t *testing.T) {
	e := newSessionsTestEnv(t)
	want := resAccess
	fakeProviders(t, &want)
	id := e.seedConnection(t, "work", "google_sheets", map[string]interface{}{"access_token": resAccess})

	for _, tc := range []struct{ platform, typ, want string }{
		{"google_sheets", "spreadsheets", `{"item":{"id":"sheet-new","name":"Budget \"26\""}}`},
		{"google_drive", "folders", `{"item":{"id":"fold-new","name":"Budget \"26\""}}`},
	} {
		out, errOut, code := e.runFlagsFirst(t, "--profile", "work", "--json", "connect", "resources", "create",
			"--platform", tc.platform, "--type", tc.typ, `--name=Budget "26"`, "--", id)
		if code != 0 {
			t.Fatalf("%s: code %d, stderr %s", tc.platform, code, errOut)
		}
		assertNoSecrets(t, out, errOut)
		if got := compactJSON(t, out); got != tc.want {
			t.Fatalf("%s: stdout = %s, want %s", tc.platform, got, tc.want)
		}
	}

	if _, errOut, code := e.runFlagsFirst(t, "--profile", "work", "--json", "connect", "resources", "create",
		"--platform", "slack", "--name", "x", id); code != 3 || !strings.Contains(errOut, `create not supported for platform "slack"`) {
		t.Fatalf("slack create: code %d, stderr %s", code, errOut)
	}
	if _, _, code := e.runFlagsFirst(t, "--profile", "work", "--json", "connect", "resources", "create",
		"--platform", "google_sheets", id); code != 3 {
		t.Fatalf("create without --name: code %d, want 3", code)
	}
}

// A credential of another profile is indistinguishable from an unknown one:
// exit 2 and no provider call.
func TestConnectResourcesScopesToActiveProfile(t *testing.T) {
	e := newSessionsTestEnv(t)
	want := resAccess
	seen := fakeProviders(t, &want)
	id := e.seedConnection(t, "work", "google_sheets", map[string]interface{}{"access_token": resAccess})

	for _, args := range [][]string{
		{"--profile", "default", "--json", "connect", "resources", "--platform", "gmail", "--type", "labels", id},
		{"--profile", "work", "--json", "connect", "resources", "--platform", "gmail", "--type", "labels", "no-such-id"},
		{"--profile", "default", "--json", "connect", "resources", "create", "--platform", "google_sheets", "--name", "x", id},
	} {
		out, errOut, code := e.runFlagsFirst(t, args...)
		if code != 2 || !strings.Contains(errOut, "credential lookup:") {
			t.Fatalf("%v: code %d, stderr %s", args, code, errOut)
		}
		assertNoSecrets(t, out, errOut)
	}
	if len(*seen) != 0 {
		t.Fatalf("provider called for a foreign credential: %v", *seen)
	}
	if _, errOut, code := e.runFlagsFirst(t, "--profile", "work", "--json", "connect", "resources",
		"--platform", "dropbox", "--type", "files", id); code != 3 || !strings.Contains(errOut, `platform "dropbox" not supported`) {
		t.Fatalf("unsupported platform: code %d, stderr %s", code, errOut)
	}
}

// An access token expiring within 60 s is exchanged first; the new token is
// used and persisted, and no token reaches stdout or stderr.
func TestConnectResourcesRefreshesExpiringToken(t *testing.T) {
	e := newSessionsTestEnv(t)
	want := resRenewed
	seen := fakeProviders(t, &want)

	var exchanged []string
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		exchanged = append(exchanged, r.FormValue("refresh_token"))
		fmt.Fprintf(w, `{"access_token":%q,"token_type":"Bearer","expires_in":3600}`, resRenewed)
	}))
	defer tokenSrv.Close()
	withTokenURL(t, "google_sheets", tokenSrv.URL)
	t.Setenv("MONOAGENT_GOOGLE_SHEETS_CLIENT_ID", "test-client")

	soon := time.Now().UTC().Add(30 * time.Second).Format(time.RFC3339)
	id := e.seedConnection(t, "work", "google_sheets", map[string]interface{}{
		"access_token": resAccess, "refresh_token": resRefresh, "expires_at": soon,
	})

	out, errOut, code := e.runFlagsFirst(t, "--profile", "work", "--json", "connect", "resources",
		"--platform", "gmail", "--type", "labels", "--", id)
	if code != 0 {
		t.Fatalf("code %d, stderr %s", code, errOut)
	}
	assertNoSecrets(t, out, errOut)
	if len(exchanged) != 1 || exchanged[0] != resRefresh {
		t.Fatalf("refresh exchanges = %v", exchanged)
	}
	if len(*seen) != 1 {
		t.Fatalf("provider calls = %v", *seen)
	}
	stored, err := connections.NewStore(e.db.DB).Get(context.Background(), id, "work")
	if err != nil || stored == nil || stored.Data["access_token"] != resRenewed {
		t.Fatalf("refreshed token not persisted: %v", err)
	}
}

// A failed refresh falls back to the existing token, and a provider error
// echoing the token back is scrubbed from stderr (exit 4).
func TestConnectResourcesScrubsSecretsFromErrors(t *testing.T) {
	e := newSessionsTestEnv(t)
	want := "a-different-bearer"
	fakeProviders(t, &want)

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"invalid_grant","detail":"refresh token %s revoked"}`, r.FormValue("refresh_token"))
	}))
	defer tokenSrv.Close()
	withTokenURL(t, "google_sheets", tokenSrv.URL)
	t.Setenv("MONOAGENT_GOOGLE_SHEETS_CLIENT_ID", "test-client")

	past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	id := e.seedConnection(t, "work", "google_sheets", map[string]interface{}{
		"access_token": resAccess, "refresh_token": resRefresh, "expires_at": past,
	})

	out, errOut, code := e.runFlagsFirst(t, "--profile", "work", "--json", "connect", "resources",
		"--platform", "google_sheets", "--type", "spreadsheets", "--", id)
	if code != 4 {
		t.Fatalf("code %d, want 4; stderr %s", code, errOut)
	}
	assertNoSecrets(t, out, errOut)
	for _, s := range []string{"token refresh failed", "google API returned 401", "[redacted]"} {
		if !strings.Contains(errOut, s) {
			t.Fatalf("stderr lacks %q: %s", s, errOut)
		}
	}
}

// withTokenURL points platform's OAuth token endpoint at url for the test.
func withTokenURL(t *testing.T, platform, url string) {
	t.Helper()
	orig := connections.Registry[platform]
	p := orig
	oauth := *orig.OAuth
	oauth.TokenURL = url
	p.OAuth = &oauth
	connections.Registry[platform] = p
	t.Cleanup(func() { connections.Registry[platform] = orig })
}

func compactJSON(t *testing.T, s string) string {
	t.Helper()
	var v interface{}
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("not JSON: %q", s)
	}
	b, _ := json.Marshal(v)
	return string(b)
}
