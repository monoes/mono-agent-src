package apipkg

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/connections"
)

// recorded is one request seen by recordServer, with its body read.
type recorded struct {
	r    *http.Request
	body string
}

// recordServer reroutes all traffic to a server answering with fn.
func recordServer(t *testing.T, fn func(r *http.Request, body string, w http.ResponseWriter)) func() []recorded {
	t.Helper()
	var mu sync.Mutex
	var seen []recorded
	pagedServer(t, func(_ int, r *http.Request, w http.ResponseWriter) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, recorded{r, string(b)})
		mu.Unlock()
		fn(r, string(b), w)
	})
	return func() []recorded {
		mu.Lock()
		defer mu.Unlock()
		return append([]recorded(nil), seen...)
	}
}

// oauthParams parses an `Authorization: OAuth …` header.
func oauthParams(t *testing.T, h string) map[string]string {
	t.Helper()
	if !strings.HasPrefix(h, "OAuth ") {
		t.Fatalf("Authorization %q is not OAuth 1.0a", h)
	}
	out := map[string]string{}
	for _, part := range strings.Split(strings.TrimPrefix(h, "OAuth "), ", ") {
		k, v, _ := strings.Cut(part, "=")
		v, _ = url.PathUnescape(strings.Trim(v, `"`))
		out[k] = v
	}
	return out
}

func TestOAuth1InjectSignsQueryAndForm(t *testing.T) {
	p := mustParse(t, `{"$schema":"apipkg.v1","id":"o1","title":"O1","base_url":"https://api.o1.test/1",
	  "credentials":[{"platform":"o1","methods":["oauth1"],"fields":[
	    {"key":"consumer_key","label":"Key","required":true},{"key":"consumer_secret","label":"Secret","secret":true,"required":true},
	    {"key":"oauth_token","label":"Token","secret":true,"required":true},{"key":"oauth_token_secret","label":"Token secret","secret":true,"required":true}],
	   "oauth":{"request_token_url":"https://api.o1.test/oauth/request","auth_url":"https://api.o1.test/oauth/authorize","token_url":"https://api.o1.test/oauth/access","signature_method":"HMAC-SHA256"},
	   "inject":{"oauth1":{}}}],
	  "operations":[{"resource":"","operation":"post","title":"Post","method":"POST","path":"/status","body_type":"form",
	    "query":{"trim":"{{ p.trim }}"},"body":{"status":"{{ p.status }}"},
	    "fields":[{"key":"status","label":"Status","type":"text"},{"key":"trim","label":"Trim","type":"text"}]}]}`)
	seen := recordServer(t, func(r *http.Request, _ string, w http.ResponseWriter) { fmt.Fprint(w, `{"ok":true}`) })
	cr := map[string]any{"consumer_key": "ck", "consumer_secret": "cs", "oauth_token": "tk", "oauth_token_secret": "ts"}
	if _, err := runPackageOp(p, map[string]any{"operation": "post", "status": "Hi there!", "trim": "1"}, cr, nil); err != nil {
		t.Fatal(err)
	}
	got := seen()[0]
	h := oauthParams(t, got.r.Header.Get("Authorization"))
	if h["oauth_consumer_key"] != "ck" || h["oauth_token"] != "tk" || h["oauth_signature_method"] != "HMAC-SHA256" {
		t.Fatalf("header %v", h)
	}
	// Re-sign the same request with the same nonce/time: the signature
	// covers the original host, the query and the form body.
	ts, _ := strconv.ParseInt(h["oauth_timestamp"], 10, 64)
	want, _ := http.NewRequest("POST", "https://api.o1.test/1/status?trim=1", nil)
	want.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := connections.SignOAuth1(want, []byte(got.body), connections.OAuth1Params{ConsumerKey: "ck", ConsumerSecret: "cs",
		Token: "tk", TokenSecret: "ts", SignatureMethod: "HMAC-SHA256", Nonce: h["oauth_nonce"], Timestamp: ts}); err != nil {
		t.Fatal(err)
	}
	if w := oauthParams(t, want.Header.Get("Authorization"))["oauth_signature"]; w != h["oauth_signature"] {
		t.Errorf("signature %s, want %s (body %q)", h["oauth_signature"], w, got.body)
	}
}

func TestTrelloOAuth1Credential(t *testing.T) {
	p := catalogPkg(t, "trello")
	seen := recordServer(t, func(r *http.Request, _ string, w http.ResponseWriter) { fmt.Fprint(w, `{"id":"c1"}`) })
	cr := map[string]any{"consumer_key": "ck", "consumer_secret": "cs", "oauth_token": "tk", "oauth_token_secret": "ts"}
	if _, err := runPackageOp(p, map[string]any{"resource": "card", "operation": "create", "listId": "L1", "name": "Card"}, cr, nil); err != nil {
		t.Fatal(err)
	}
	r := seen()[0].r
	if q := r.URL.Query(); q.Get("key") != "" || q.Get("token") != "" {
		t.Errorf("api-key query sent with an OAuth 1.0a connection: %v", q)
	}
	if h := oauthParams(t, r.Header.Get("Authorization")); h["oauth_consumer_key"] != "ck" || h["oauth_token"] != "tk" || h["oauth_signature_method"] != "HMAC-SHA1" {
		t.Errorf("header %v", h)
	}
}

func TestOAuth1PlatformRegistration(t *testing.T) {
	p := catalogPkg(t, "trello")
	def, ok := platformDef(p, &p.Credentials[1])
	if !ok || def.ID != "trello_oauth1" || def.OAuth1 == nil {
		t.Fatalf("def = %+v", def)
	}
	if fmt.Sprint(def.Methods) != "[oauth1 apikey]" {
		t.Errorf("methods %v", def.Methods)
	}
	if len(def.Fields[connections.MethodOAuth1]) != 2 || len(def.Fields[connections.MethodAPIKey]) != 4 {
		t.Errorf("fields %v", def.Fields)
	}
	if def.OAuth1.RequestTokenURL != "https://trello.com/1/OAuthGetRequestToken" || def.OAuth1.AccessTokenURL != "https://trello.com/1/OAuthGetAccessToken" {
		t.Errorf("oauth1 %+v", def.OAuth1)
	}
	bad := `{"$schema":"apipkg.v1","id":"bad","title":"Bad","base_url":"https://x.test",
	  "credentials":[{"platform":"bad","methods":["oauth1"],"fields":[{"key":"consumer_key","label":"K"}],"inject":{"oauth1":{}}}],
	  "operations":[{"resource":"","operation":"get","title":"Get","method":"GET","path":"/"}]}`
	if _, err := Parse([]byte(bad), "bad"); err == nil || !strings.Contains(err.Error(), "request_token_url") {
		t.Errorf("oauth1 without endpoints: err = %v", err)
	}
}

func TestPerTenantOAuthEndpoints(t *testing.T) {
	cases := []struct {
		pkg, platform string
		data          map[string]any
		auth, token   string
		scopes        string
		fields        string
	}{
		{"zendesk", "zendesk_oauth", map[string]any{"subdomain": "acme"},
			"https://acme.zendesk.com/oauth/authorizations/new", "https://acme.zendesk.com/oauth/tokens", "[read write]", "[subdomain]"},
		{"mautic", "mautic_oauth", map[string]any{"url": "https://m.example.com/"},
			"https://m.example.com/oauth/v2/authorize", "https://m.example.com/oauth/v2/token", "[]", "[url]"},
		{"gitlab", "gitlab", map[string]any{},
			"https://gitlab.com/oauth/authorize", "https://gitlab.com/oauth/token", "[api]", "[server]"},
		{"gitlab", "gitlab", map[string]any{"server": "https://git.example.com/"},
			"https://git.example.com/oauth/authorize", "https://git.example.com/oauth/token", "[api]", "[server]"},
		{"zoho_crm", "zoho_crm", map[string]any{"access_token_url": "https://accounts.zoho.eu/oauth/v2/token"},
			"https://accounts.zoho.eu/oauth/v2/auth", "https://accounts.zoho.eu/oauth/v2/token", "[]", "[access_token_url]"},
		{"microsoft_dynamics_crm", "microsoft_dynamics_oauth", map[string]any{"subdomain": "contoso", "region": "crm4.dynamics.com", "tenant_id": "t1"},
			"https://login.microsoftonline.com/t1/oauth2/v2.0/authorize", "https://login.microsoftonline.com/t1/oauth2/v2.0/token",
			"[https://contoso.crm4.dynamics.com/user_impersonation offline_access]", "[subdomain region tenant_id]"},
		{"microsoft_dataverse", "microsoft_dataverse_oauth", map[string]any{"environment_url": "https://org.crm.dynamics.com/"},
			"https://login.microsoftonline.com/organizations/oauth2/v2.0/authorize", "https://login.microsoftonline.com/organizations/oauth2/v2.0/token",
			"[https://org.crm.dynamics.com/user_impersonation offline_access]", "[environment_url tenant_id]"},
	}
	for _, c := range cases {
		p := catalogPkg(t, c.pkg)
		var cr *Credential
		for i := range p.Credentials {
			if p.Credentials[i].Platform == c.platform {
				cr = &p.Credentials[i]
			}
		}
		def, ok := platformDef(p, cr)
		if !ok || def.OAuth == nil || def.OAuth.Tailor == nil {
			t.Fatalf("%s: no tailored OAuth: %+v", c.platform, def)
		}
		var keys []string
		for _, f := range def.OAuth.ConnectFields {
			keys = append(keys, f.Key)
		}
		if fmt.Sprint(keys) != c.fields {
			t.Errorf("%s: connect fields %v, want %s", c.platform, keys, c.fields)
		}
		cfg, err := def.OAuth.Tailor(*def.OAuth, c.data)
		if err != nil {
			t.Fatalf("%s: %v", c.platform, err)
		}
		if cfg.AuthURL != c.auth || cfg.TokenURL != c.token || fmt.Sprint(cfg.Scopes) != c.scopes {
			t.Errorf("%s %v: got %s | %s | %v", c.platform, c.data, cfg.AuthURL, cfg.TokenURL, cfg.Scopes)
		}
	}
	// Zoho's scope string in extra_params is passed through unchanged.
	p := catalogPkg(t, "zoho_crm")
	def, _ := platformDef(p, &p.Credentials[0])
	cfg, _ := def.OAuth.Tailor(*def.OAuth, map[string]any{})
	if cfg.ExtraParams["access_type"] != "offline" || !strings.HasPrefix(cfg.ExtraParams["scope"], "ZohoCRM.") || cfg.TokenURL != "https://accounts.zoho.com/oauth/v2/token" {
		t.Errorf("zoho default: %+v", cfg)
	}
}

func TestDynamicsDelegatedConnectionPicksOAuthCredential(t *testing.T) {
	p := catalogPkg(t, "microsoft_dynamics_crm")
	cr := map[string]any{"access_token": "user-token", "subdomain": "contoso", "region": "crm.dynamics.com"}
	if got := p.selectCredential(cr); got.Platform != "microsoft_dynamics_oauth" {
		t.Errorf("picked %s", got.Platform)
	}
}

func mailchimpMetaServer(t *testing.T) func() []recorded {
	return recordServer(t, func(r *http.Request, _ string, w http.ResponseWriter) {
		if r.URL.Path == "/oauth2/metadata" {
			if r.Header.Get("Authorization") != "OAuth tok" {
				http.Error(w, `{"error":"invalid"}`, http.StatusUnauthorized)
				return
			}
			fmt.Fprint(w, `{"dc":"us6","api_endpoint":"https://us6.api.mailchimp.com","login":{"email":"a@b.c"}}`)
			return
		}
		fmt.Fprint(w, `{"id":"m1"}`)
	})
}

func TestMailchimpMetadataAtConnect(t *testing.T) {
	seen := mailchimpMetaServer(t)
	p := catalogPkg(t, "mailchimp")
	def, _ := platformDef(p, &p.Credentials[1])
	if def.OAuth == nil || def.OAuth.AfterToken == nil {
		t.Fatalf("no metadata hook: %+v", def.OAuth)
	}
	data := map[string]any{"access_token": "tok"}
	if err := def.OAuth.AfterToken(context.Background(), data); err != nil {
		t.Fatal(err)
	}
	if data["dc"] != "us6" || data["api_endpoint"] != "https://us6.api.mailchimp.com" {
		t.Errorf("data %v", data)
	}
	if r := seen()[0].r; r.URL.Path != "/oauth2/metadata" || r.Method != "GET" {
		t.Errorf("metadata request %s %s", r.Method, r.URL)
	}
	if err := def.OAuth.AfterToken(context.Background(), map[string]any{"access_token": "bad"}); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("bad token: err = %v", err)
	}
}

// capturingHost wraps the rerouting transport to remember each original host.
type capturingHost struct {
	next  http.RoundTripper
	mu    sync.Mutex
	hosts []string
}

func (c *capturingHost) RoundTrip(r *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.hosts = append(c.hosts, r.URL.Host)
	c.mu.Unlock()
	return c.next.RoundTrip(r)
}

func TestMailchimpMetadataAtRunTime(t *testing.T) {
	seen := mailchimpMetaServer(t)
	hosts := &capturingHost{next: httpClient.Transport}
	httpClient = &http.Client{Transport: hosts}
	p := catalogPkg(t, "mailchimp")
	params := map[string]any{"resource": "member", "operation": "create", "list": "L1", "email": "a@b.c", "status": "subscribed"}
	cr := map[string]any{"access_token": "tok"} // an OAuth connection without dc
	for i := 0; i < 2; i++ {
		if _, err := runPackageOp(p, params, cr, nil); err != nil {
			t.Fatal(err)
		}
	}
	if fmt.Sprint(hosts.hosts) != "[login.mailchimp.com us6.api.mailchimp.com us6.api.mailchimp.com]" {
		t.Errorf("hosts %v (the lookup must run once per token)", hosts.hosts)
	}
	if r := seen()[1].r; r.Header.Get("Authorization") != "Bearer tok" || r.URL.Path != "/3.0/lists/L1/members" {
		t.Errorf("main request %s %v", r.URL, r.Header)
	}
	// An API key carries its own data centre: no lookup.
	hosts.hosts = nil
	if _, err := runPackageOp(p, params, map[string]any{"api_key": "abc-us9"}, nil); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(hosts.hosts) != "[us9.api.mailchimp.com]" {
		t.Errorf("api key hosts %v", hosts.hosts)
	}
}

func TestAWSAssumeRole(t *testing.T) {
	stsCache = sync.Map{}
	var stsCalls int
	seen := recordServer(t, func(r *http.Request, body string, w http.ResponseWriter) {
		if strings.Contains(body, "Action=AssumeRole") {
			stsCalls++
			if strings.Contains(body, "deny") {
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprint(w, `<ErrorResponse><Error><Code>AccessDenied</Code></Error></ErrorResponse>`)
				return
			}
			w.Header().Set("Content-Type", "text/xml")
			fmt.Fprint(w, `<AssumeRoleResponse><AssumeRoleResult><Credentials>
<AccessKeyId>ASIATEMP</AccessKeyId><SecretAccessKey>tempsecret</SecretAccessKey><SessionToken>tempsession</SessionToken>
<Expiration>2099-01-01T00:00:00Z</Expiration></Credentials></AssumeRoleResult></AssumeRoleResponse>`)
			return
		}
		fmt.Fprint(w, `{"SendMessageResponse":{"SendMessageResult":{"MessageId":"x"}}}`)
	})
	p := catalogPkg(t, "aws_sqs")
	params := map[string]any{"operation": "sendMessage", "queue": "https://sqs.eu-west-1.amazonaws.com/123/jobs", "message": "hi", "sendInputData": false}
	cr := map[string]any{"access_key_id": "AKIABASE", "secret_access_key": "base", "region": "eu-west-1",
		"role_arn": "arn:aws:iam::123:role/r", "external_id": "ext-1"}
	for i := 0; i < 2; i++ {
		if _, err := runPackageOp(p, params, cr, nil); err != nil {
			t.Fatal(err)
		}
	}
	reqs := seen()
	if stsCalls != 1 || len(reqs) != 3 {
		t.Fatalf("%d STS calls, %d requests (temporary credentials must be cached)", stsCalls, len(reqs))
	}
	sts := reqs[0]
	form, _ := url.ParseQuery(sts.body)
	if form.Get("RoleArn") != "arn:aws:iam::123:role/r" || form.Get("ExternalId") != "ext-1" || form.Get("RoleSessionName") != "mono-agent" || form.Get("Version") != "2011-06-15" {
		t.Errorf("STS form %v", form)
	}
	if a := sts.r.Header.Get("Authorization"); !strings.Contains(a, "Credential=AKIABASE/") || !strings.Contains(a, "/eu-west-1/sts/aws4_request") {
		t.Errorf("STS signed with %q", a)
	}
	main := reqs[1].r
	if a := main.Header.Get("Authorization"); !strings.Contains(a, "Credential=ASIATEMP/") || !strings.Contains(a, "/sqs/aws4_request") {
		t.Errorf("main request signed with %q", a)
	}
	if main.Header.Get("X-Amz-Security-Token") != "tempsession" {
		t.Errorf("session token %q", main.Header.Get("X-Amz-Security-Token"))
	}

	// Without a role the keys sign directly; a refused role fails naming it.
	plain := map[string]any{"access_key_id": "AKIABASE", "secret_access_key": "base", "region": "eu-west-1"}
	if _, err := runPackageOp(p, params, plain, nil); err != nil {
		t.Fatal(err)
	}
	if a := seen()[3].r.Header.Get("Authorization"); !strings.Contains(a, "Credential=AKIABASE/") {
		t.Errorf("plain keys signed with %q", a)
	}
	deny := map[string]any{"access_key_id": "AKIABASE", "secret_access_key": "base", "role_arn": "arn:aws:iam::123:role/deny"}
	if _, err := runPackageOp(p, params, deny, nil); err == nil || !strings.Contains(err.Error(), "role/deny") || !strings.Contains(err.Error(), "403") {
		t.Errorf("denied role: err = %v", err)
	}
	if a := seen()[4].r.Header.Get("Authorization"); !strings.Contains(a, "/us-east-1/sts/aws4_request") {
		t.Errorf("no region: STS should be signed for the global endpoint, got %q", a)
	}
}

func TestWrapAndMimePartsFilters(t *testing.T) {
	sc := Scope{P: map[string]any{"s": "abcdefgh", "files": []any{
		map[string]any{"file_name": `a"b.txt`, "mime_type": "text/plain", "base64": "aGVsbG8=", "property": "data"},
		map[string]any{"base64": "AAEC", "property": "raw"},
	}}}
	cases := map[string]any{
		"{{ p.s | wrap:3 }}":                  "abc\r\ndef\r\ngh",
		"{{ p.s | wrap:8 }}":                  "abcdefgh",
		"{{ p.none | mime_parts:'b' }}":       nil,
		"{{ p.empty | default:'' | wrap:2 }}": "",
	}
	for tpl, want := range cases {
		if got, err := renderString(tpl, sc); err != nil || got != want {
			t.Errorf("%s = %#v (%v), want %#v", tpl, got, err, want)
		}
	}
	got, err := renderString("{{ p.files | mime_parts:'b' }}", sc)
	if err != nil {
		t.Fatal(err)
	}
	want := "--b\r\nContent-Type: text/plain; name=\"ab.txt\"\r\nContent-Disposition: attachment; filename=\"ab.txt\"\r\nContent-Transfer-Encoding: base64\r\n\r\naGVsbG8=\r\n" +
		"--b\r\nContent-Type: application/octet-stream; name=\"raw\"\r\nContent-Disposition: attachment; filename=\"raw\"\r\nContent-Transfer-Encoding: base64\r\n\r\nAAEC\r\n--b--\r\n"
	if got != want {
		t.Errorf("mime_parts =\n%q\nwant\n%q", got, want)
	}
	for _, tpl := range []string{"{{ p.s | wrap:0 }}", "{{ p.files | mime_parts:'' }}", "{{ p.s | mime_parts:'b' }}"} {
		if _, err := renderString(tpl, sc); err == nil {
			t.Errorf("%s: no error", tpl)
		}
	}
	if _, err := renderString("{{ p.files | mime_parts:'b' }}", Scope{P: map[string]any{"files": []any{map[string]any{"property": "gone"}}}}); err == nil || !strings.Contains(err.Error(), "gone") {
		t.Errorf("missing binary: err = %v", err)
	}
}

func TestGmailSendWithAttachments(t *testing.T) {
	seen := recordServer(t, func(r *http.Request, _ string, w http.ResponseWriter) { fmt.Fprint(w, `{"id":"m1"}`) })
	p := catalogPkg(t, "gmail_more")
	long := strings.Repeat("x", 120)
	pdf := strings.Repeat("A", 100) // base64 text of the second file
	input := map[string]any{"binary": map[string]any{
		"data": map[string]any{"data": "aGVsbG8=", "fileName": "hello.txt", "mimeType": "text/plain"},
		"doc":  map[string]any{"data": pdf, "fileName": "doc.pdf", "mimeType": "application/pdf"},
	}}
	params := map[string]any{"resource": "message", "operation": "send", "sendTo": "a@b.c", "subject": "Hi", "emailType": "text", "message": long,
		"options": map[string]any{"attachmentsUi": map[string]any{"attachmentsBinary": []any{map[string]any{"property": "data,doc"}}}}}
	if _, err := runPackageOp(p, params, map[string]any{"access_token": "t"}, input); err != nil {
		t.Fatal(err)
	}
	got := seen()[0]
	if got.r.URL.Path != "/upload/gmail/v1/users/me/messages/send" || got.r.Header.Get("Content-Type") != "message/rfc822" {
		t.Errorf("request %s %s", got.r.URL, got.r.Header.Get("Content-Type"))
	}
	body := got.body
	for _, want := range []string{
		"MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"monoagent_mixed\"\r\n\r\n--monoagent_mixed\r\nContent-Type: text/plain; charset=UTF-8\r\n",
		"Content-Type: text/plain; name=\"hello.txt\"\r\nContent-Disposition: attachment; filename=\"hello.txt\"\r\nContent-Transfer-Encoding: base64\r\n\r\naGVsbG8=\r\n",
		"Content-Type: application/pdf; name=\"doc.pdf\"\r\n",
		pdf[:76] + "\r\n" + pdf[76:] + "\r\n--monoagent_mixed--\r\n",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	for _, line := range strings.Split(body, "\r\n") {
		if len(line) > 998 || (len(line) > 76 && !strings.HasPrefix(line, "Subject:")) {
			t.Errorf("line longer than 76 characters: %q", line)
		}
	}

	// Without attachments the message stays a single text part.
	delete(params, "options")
	if _, err := runPackageOp(p, params, map[string]any{"access_token": "t"}, nil); err != nil {
		t.Fatal(err)
	}
	if b := seen()[1].body; strings.Contains(b, "multipart") || !strings.Contains(b, "Content-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: base64") {
		t.Errorf("plain message:\n%s", b)
	}
	// A named property the item lacks fails the item.
	params["options"] = map[string]any{"attachmentsUi": map[string]any{"attachmentsBinary": []any{map[string]any{"property": "nope"}}}}
	if _, err := runPackageOp(p, params, map[string]any{"access_token": "t"}, input); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("missing property: err = %v", err)
	}
}
