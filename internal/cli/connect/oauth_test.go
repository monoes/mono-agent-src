package connect

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/connections"
)

// --field values are checked against the platform's OAuth connect fields
// (or OAuth 1.0a form) before anything opens: an unknown key names the
// allowed ones.
func TestConnectOAuthFieldFlags(t *testing.T) {
	e := newSessionsTestEnv(t)
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"salesforce", "--field", "tenant=x"}, "salesforce's OAuth takes environment"},
		{[]string{"github", "--field", "a=b"}, "github's OAuth takes no fields"},
		{[]string{"salesforce", "--field", "environment"}, "use key=value"},
		{[]string{"generic_oauth1", "--field", "client_id=x"}, "access_token_url, authorization_url, base_url, consumer_key, consumer_secret, request_token_url, signature_method"},
		{[]string{"generic_oauth1", "--scopes", "mail"}, "uses OAuth 1.0a"},
		// No terminal (--json): the missing required fields are named.
		{[]string{"generic_oauth1", "--field", "request_token_url=http://127.0.0.1:1/r"}, "needs --field authorization_url=… --field access_token_url=… --field consumer_key=… --field consumer_secret=…"},
		{[]string{"smtp"}, "does not support OAuth"},
	}
	for _, c := range cases {
		_, errOut, code := e.run(t, "", append([]string{"--json", "connect", "oauth"}, c.args...)...)
		if code != 3 || !strings.Contains(errOut, c.want) {
			t.Errorf("%v: code %d, stderr %s; want 3 and %q", c.args, code, errOut, c.want)
		}
	}
}

// An OAuth 1.0a-only platform runs the OAuth 1.0a flow with the --field
// consumer key and secret; a failure keeps the secret out of the message.
func TestConnectOAuthRunsOAuth1Flow(t *testing.T) {
	const secret = "consumer-secret-value"
	var authz string
	prov := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authz = r.Header.Get("Authorization")
		http.Error(w, "rejected consumer "+secret, http.StatusUnauthorized)
	}))
	defer prov.Close()
	connections.Registry["oauth1-cli-test"] = connections.PlatformDef{
		ID: "oauth1-cli-test", Name: "OAuth1 CLI Test", Methods: []connections.AuthMethod{connections.MethodOAuth1},
		Fields: map[connections.AuthMethod][]connections.CredentialField{connections.MethodOAuth1: {
			{Key: connections.OAuth1ConsumerKey, Label: "Key", Required: true},
			{Key: connections.OAuth1ConsumerSecret, Label: "Secret", Secret: true, Required: true},
		}},
		OAuth1: &connections.OAuth1Config{RequestTokenURL: prov.URL + "/request", AuthURL: prov.URL + "/authorize", AccessTokenURL: prov.URL + "/access"},
	}
	t.Cleanup(func() { delete(connections.Registry, "oauth1-cli-test") })

	e := newSessionsTestEnv(t)
	out, errOut, code := e.run(t, "", "--json", "connect", "oauth", "oauth1-cli-test",
		"--field", "consumer_key=ck-1", "--field", "consumer_secret="+secret)
	if code != 4 || !strings.Contains(errOut, "oauth1 request token") {
		t.Fatalf("code %d, stderr %s", code, errOut)
	}
	if strings.Contains(out+errOut, secret) {
		t.Fatalf("the consumer secret was printed:\n%s\n%s", out, errOut)
	}
	if !strings.HasPrefix(authz, "OAuth ") || !strings.Contains(authz, `oauth_consumer_key="ck-1"`) || !strings.Contains(authz, "oauth_callback=") {
		t.Fatalf("request-token Authorization = %q", authz)
	}
}

// --stdin-json takes field values (the consumer secret) on stdin instead of
// argv; a --field of the same key wins, and bad stdin never echoes it.
func TestConnectOAuthStdinJSON(t *testing.T) {
	const secret = "stdin-consumer-secret"
	var authz string
	prov := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authz = r.Header.Get("Authorization")
		http.Error(w, "rejected", http.StatusUnauthorized)
	}))
	defer prov.Close()

	e := newSessionsTestEnv(t)
	stdin := `{"consumer_key":"ck-stdin","consumer_secret":"` + secret + `","request_token_url":"` + prov.URL + `/request"}`
	out, errOut, code := e.run(t, stdin, "--json", "connect", "oauth", "generic_oauth1", "--stdin-json",
		"--field", "consumer_key=ck-flag", "--field", "authorization_url="+prov.URL+"/authorize",
		"--field", "access_token_url="+prov.URL+"/access")
	if code != 4 || !strings.Contains(errOut, "oauth1 request token") {
		t.Fatalf("code %d, stderr %s", code, errOut)
	}
	if strings.Contains(out+errOut, secret) {
		t.Fatalf("the consumer secret was printed:\n%s\n%s", out, errOut)
	}
	if !strings.Contains(authz, `oauth_consumer_key="ck-flag"`) {
		t.Fatalf("request-token Authorization = %q", authz)
	}

	for _, c := range []struct{ stdin, want string }{
		{`not json ` + secret, "stdin must be a JSON object"},
		{`{"client_id":"` + secret + `"}`, "generic_oauth1's OAuth takes"},
		{`{"":"` + secret + `"}`, "empty key"},
	} {
		out, errOut, code := e.run(t, c.stdin, "--json", "connect", "oauth", "generic_oauth1", "--stdin-json")
		if code != 3 || !strings.Contains(errOut, c.want) || strings.Contains(out+errOut, secret) {
			t.Errorf("stdin %q: code %d, stderr %s; want 3 and %q without the secret", c.stdin, code, errOut, c.want)
		}
	}
}

// A completed `connect oauth --json` prints exactly one JSON document on
// stdout (process stdout included); the authorize URL is a progress line on
// stderr.
func TestConnectOAuthJSONStdoutIsOneDocument(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no browser opener: the "user" below answers the callback
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	prov := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/request":
			go func() { // the user approves in the browser, which redirects to the callback
				for i := 0; i < 100; i++ {
					resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/callback?oauth_token=tmp&oauth_verifier=v", port))
					if err == nil {
						resp.Body.Close()
						return
					}
					time.Sleep(20 * time.Millisecond)
				}
			}()
			fmt.Fprint(w, "oauth_token=tmp&oauth_token_secret=tmps&oauth_callback_confirmed=true")
		case "/access":
			fmt.Fprint(w, "oauth_token=acc&oauth_token_secret=accs")
		}
	}))
	defer prov.Close()
	connections.Registry["oauth1-json-test"] = connections.PlatformDef{
		ID: "oauth1-json-test", Name: "OAuth1 JSON Test", Methods: []connections.AuthMethod{connections.MethodOAuth1},
		Fields: map[connections.AuthMethod][]connections.CredentialField{connections.MethodOAuth1: {
			{Key: connections.OAuth1ConsumerKey, Label: "Key", Required: true},
			{Key: connections.OAuth1ConsumerSecret, Label: "Secret", Secret: true, Required: true},
		}},
		OAuth1: &connections.OAuth1Config{RequestTokenURL: prov.URL + "/request", AuthURL: prov.URL + "/authorize",
			AccessTokenURL: prov.URL + "/access", CallbackPort: port},
	}
	t.Cleanup(func() { delete(connections.Registry, "oauth1-json-test") })

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldOut := os.Stdout
	os.Stdout = w
	e := newSessionsTestEnv(t)
	out, errOut, code := e.run(t, `{"consumer_secret":"cs-1"}`, "--json", "connect", "oauth", "oauth1-json-test", "--field", "consumer_key=ck-1", "--stdin-json")
	w.Close()
	os.Stdout = oldOut
	printed, _ := io.ReadAll(r)
	if code != 0 {
		t.Fatalf("code %d, stderr %s", code, errOut)
	}
	if len(printed) != 0 {
		t.Errorf("the flow wrote to process stdout: %q", printed)
	}
	dec := json.NewDecoder(strings.NewReader(out))
	var conn map[string]any
	if err := dec.Decode(&conn); err != nil || conn["platform"] != "oauth1-json-test" || dec.More() {
		t.Fatalf("stdout is not one connection document: %q (%v)", out, err)
	}
	if !strings.Contains(errOut, `{"message":"Opening browser: `+prov.URL+`/authorize?oauth_token=tmp","kind":"info"}`) {
		t.Errorf("stderr lacks the authorize URL progress line:\n%s", errOut)
	}
}

// connect fields prints a connection's non-secret form values for a
// reconnect form — never a secret field or a token.
func TestConnectFieldsOmitsSecrets(t *testing.T) {
	e := newSessionsTestEnv(t)
	conn := &connections.Connection{Platform: "generic_oauth1", Method: connections.MethodOAuth1, ProfileID: "default", Data: map[string]interface{}{
		"request_token_url": "https://api.example.com/rt", "authorization_url": "https://api.example.com/auth",
		"access_token_url": "https://api.example.com/at", "consumer_key": "ck-9",
		"consumer_secret": "cs-secret-9", "oauth_token": "tok-secret-9", "oauth_token_secret": "toks-secret-9",
	}}
	if err := connections.NewStore(e.db.DB).Save(context.Background(), conn); err != nil {
		t.Fatal(err)
	}
	out, errOut, code := e.run(t, "", "--json", "connect", "fields", conn.ID)
	var v struct {
		ID     string            `json:"id"`
		Method string            `json:"method"`
		Fields map[string]string `json:"fields"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &v) != nil {
		t.Fatalf("code %d, stdout %q, stderr %s", code, out, errOut)
	}
	want := map[string]string{"request_token_url": "https://api.example.com/rt", "authorization_url": "https://api.example.com/auth",
		"access_token_url": "https://api.example.com/at", "consumer_key": "ck-9"}
	if v.ID != conn.ID || v.Method != "oauth1" || fmt.Sprint(v.Fields) != fmt.Sprint(want) {
		t.Errorf("fields = %+v", v)
	}
	if strings.Contains(out, "secret-9") {
		t.Errorf("a secret was printed: %s", out)
	}
	if _, _, code := e.run(t, "", "--json", "connect", "fields", "nope"); code != 2 {
		t.Errorf("unknown id: code %d, want 2", code)
	}
	if _, _, code := e.run(t, "", "--profile", "work", "--json", "connect", "fields", conn.ID); code != 2 {
		t.Errorf("another profile's connection: code %d, want 2", code)
	}
}

// connect show describes a platform's methods, fields and OAuth details.
func TestConnectShow(t *testing.T) {
	e := newSessionsTestEnv(t)
	var v platformView
	out, errOut, code := e.run(t, "", "--json", "connect", "show", "generic_oauth1")
	if code != 0 || json.Unmarshal([]byte(out), &v) != nil {
		t.Fatalf("code %d, stdout %q, stderr %s", code, out, errOut)
	}
	if strings.Join(v.Methods, ",") != "oauth1,apikey" || v.OAuth1 == nil || v.OAuth1.RequestTokenURL != "" || len(v.Fields["oauth1"]) != 7 {
		t.Fatalf("view = %+v", v)
	}
	if !strings.Contains(v.Connect[0], "connect oauth generic_oauth1 --field request_token_url=…") {
		t.Errorf("connect = %v", v.Connect)
	}

	out, _, _ = e.run(t, "", "--json", "connect", "show", "salesforce")
	v = platformView{}
	if json.Unmarshal([]byte(out), &v) != nil || v.OAuth == nil || !v.OAuth.PerTenant ||
		len(v.OAuth.ConnectFields) != 1 || v.OAuth.ConnectFields[0].Key != "environment" || v.OAuth1 != nil {
		t.Fatalf("salesforce view = %s", out)
	}

	out, _, code = e.run(t, "", "connect", "show", "generic_oauth1")
	if code != 0 || !strings.Contains(out, "consumer_secret") || !strings.Contains(out, "(required, secret)") {
		t.Errorf("text = %s", out)
	}
	if _, _, code := e.run(t, "", "connect", "show", "nope"); code != 3 {
		t.Errorf("unknown platform: code %d", code)
	}
}

// connect list --all --json carries the OAuth connect fields and OAuth
// 1.0a endpoints, never the hook funcs.
func TestConnectListAllCarriesOAuthDetails(t *testing.T) {
	e := newSessionsTestEnv(t)
	out, errOut, code := e.run(t, "", "--json", "connect", "list", "--all")
	var all []map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &all) != nil {
		t.Fatalf("code %d, stderr %s", code, errOut)
	}
	byID := map[string]map[string]any{}
	for _, p := range all {
		byID[fmt.Sprint(p["ID"])] = p
	}
	sf, _ := byID["salesforce"]["OAuth"].(map[string]any)
	if cf, _ := sf["ConnectFields"].([]any); len(cf) != 1 {
		t.Errorf("salesforce OAuth = %v", sf)
	}
	if _, ok := byID["generic_oauth1"]["OAuth1"].(map[string]any); !ok {
		t.Errorf("generic_oauth1 = %v", byID["generic_oauth1"])
	}
}
