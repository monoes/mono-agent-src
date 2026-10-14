package connections

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// oauthHeader parses an `Authorization: OAuth k="v", …` header.
func oauthHeader(t *testing.T, h string) map[string]string {
	t.Helper()
	if !strings.HasPrefix(h, "OAuth ") {
		t.Fatalf("Authorization %q is not an OAuth header", h)
	}
	out := map[string]string{}
	for _, part := range strings.Split(strings.TrimPrefix(h, "OAuth "), ", ") {
		k, v, _ := strings.Cut(part, "=")
		v, err := url.PathUnescape(strings.Trim(v, `"`))
		if err != nil {
			t.Fatalf("header part %q: %v", part, err)
		}
		out[k] = v
	}
	return out
}

func TestSignOAuth1KnownVectors(t *testing.T) {
	cases := []struct {
		name, method, url, body string
		p                       OAuth1Params
		want                    string
	}{
		{
			// OAuth Core 1.0 Appendix A.5 (the photos example).
			name: "core-1.0", method: "GET", url: "http://photos.example.net/photos?file=vacation.jpg&size=original",
			p: OAuth1Params{ConsumerKey: "dpf43f3p2l4k3l03", ConsumerSecret: "kd94hf93k423kf44",
				Token: "nnch734d00sl2jdk", TokenSecret: "pfkkdhi9sl3r4s00", Nonce: "kllo9940pd9333jh", Timestamp: 1191242096},
			want: "tR3+Ty81lMeYAr/Fid0kMTYa/WM=",
		},
		{
			// Twitter's documented signing example (form body parameters).
			name: "twitter", method: "POST", url: "https://api.twitter.com/1.1/statuses/update.json?include_entities=true",
			body: "status=" + url.QueryEscape("Hello Ladies + Gentlemen, a signed OAuth request!"),
			p: OAuth1Params{ConsumerKey: "xvz1evFS4wEEPTGEFPHBog", ConsumerSecret: "kAcSOqF21Fu85e7zjz7ZN2U4ZRhfV3WpwPAoE3Z7kBw",
				Token: "370773112-GmHxMAgYyLbNEtIKZeRNFsMKPR9EyMZeS9weJAEb", TokenSecret: "LswwdoUaIvS8ltyTt5jkRh4J50vUPVVHtR2YPi5kE",
				Nonce: "kYjzVBB8Y0ZFabxSWbWovY3uYSQ2pTgmZeNu2VS4cg", Timestamp: 1318622958},
			want: "hCtSmYh+iHYCEqBWrE7C7hYmtUk=",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, _ := http.NewRequest(c.method, c.url, strings.NewReader(c.body))
			if c.body != "" {
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			}
			if err := SignOAuth1(req, []byte(c.body), c.p); err != nil {
				t.Fatal(err)
			}
			h := oauthHeader(t, req.Header.Get("Authorization"))
			if h["oauth_signature"] != c.want {
				t.Errorf("signature = %q, want %q", h["oauth_signature"], c.want)
			}
			if h["oauth_consumer_key"] != c.p.ConsumerKey || h["oauth_token"] != c.p.Token || h["oauth_version"] != "1.0" {
				t.Errorf("header = %v", h)
			}
		})
	}
}

func TestSignOAuth1MethodsAndErrors(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://api.example.com:443/a?b=1", nil)
	if err := SignOAuth1(req, nil, OAuth1Params{ConsumerKey: "k", ConsumerSecret: "c&s", TokenSecret: "t", SignatureMethod: "PLAINTEXT", Realm: "Photos"}); err != nil {
		t.Fatal(err)
	}
	auth := req.Header.Get("Authorization")
	h := oauthHeader(t, auth)
	if h["oauth_signature"] != "c%26s&t" || h["oauth_signature_method"] != "PLAINTEXT" || !strings.Contains(auth, `realm="Photos"`) {
		t.Errorf("PLAINTEXT header = %s", auth)
	}
	if h["oauth_nonce"] == "" || h["oauth_timestamp"] == "" {
		t.Errorf("nonce/timestamp not generated: %v", h)
	}
	if _, ok := h["oauth_token"]; ok {
		t.Errorf("oauth_token sent without a token: %v", h)
	}
	for _, m := range []string{"HMAC-SHA256", "hmac-sha512"} {
		req, _ := http.NewRequest("GET", "https://x.test/", nil)
		if err := SignOAuth1(req, nil, OAuth1Params{ConsumerKey: "k", SignatureMethod: m}); err != nil {
			t.Errorf("%s: %v", m, err)
		}
	}
	if err := SignOAuth1(req, nil, OAuth1Params{ConsumerKey: "k", SignatureMethod: "RSA-SHA1"}); err == nil || !strings.Contains(err.Error(), "RSA-SHA1") {
		t.Errorf("RSA-SHA1: err = %v", err)
	}
	if err := SignOAuth1(req, nil, OAuth1Params{}); err == nil || !strings.Contains(err.Error(), "consumer key") {
		t.Errorf("no key: err = %v", err)
	}
	if got := oauthBaseURL(req.URL); got != "https://api.example.com/a" {
		t.Errorf("base URL %q", got)
	}
	u, _ := url.Parse("HTTP://Example.COM:8080/a%20b?x=1")
	if got := oauthBaseURL(u); got != "http://example.com:8080/a%20b" {
		t.Errorf("base URL %q", got)
	}
}

// fakeOAuth1Provider is a provider's request-token and access-token
// endpoints; the browser step is simulated by calling the callback.
type fakeOAuth1Provider struct {
	*httptest.Server
	mu      sync.Mutex
	headers []map[string]string
}

func newFakeOAuth1Provider(t *testing.T) *fakeOAuth1Provider {
	f := &fakeOAuth1Provider{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := oauthHeader(t, r.Header.Get("Authorization"))
		f.mu.Lock()
		f.headers = append(f.headers, h)
		f.mu.Unlock()
		switch r.URL.Path {
		case "/request":
			fmt.Fprint(w, "oauth_token=temp&oauth_token_secret=tempsecret&oauth_callback_confirmed=true")
		case "/access":
			if h["oauth_token"] != "temp" || h["oauth_verifier"] != "v123" {
				http.Error(w, "bad verifier", http.StatusUnauthorized)
				return
			}
			fmt.Fprint(w, "oauth_token=final&oauth_token_secret=finalsecret&screen_name=ada")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// stubOAuth1Browser answers the authorization page by calling the callback
// with the given query (as the provider's redirect would).
func stubOAuth1Browser(t *testing.T, port int, query func(authURL *url.URL) string) *string {
	var opened string
	old := oauth1Browser
	oauth1Browser = func(u string) error {
		opened = u
		pu, _ := url.Parse(u)
		go func() {
			for i := 0; i < 50; i++ { // the callback server may still be starting
				resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/callback?%s", port, query(pu)))
				if err == nil {
					resp.Body.Close()
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
		}()
		return nil
	}
	t.Cleanup(func() { oauth1Browser = old })
	return &opened
}

func TestRunOAuth1Flow(t *testing.T) {
	prov := newFakeOAuth1Provider(t)
	port := freePort(t)
	opened := stubOAuth1Browser(t, port, func(u *url.URL) string {
		return "oauth_token=" + u.Query().Get("oauth_token") + "&oauth_verifier=v123"
	})
	cfg := OAuth1Config{RequestTokenURL: prov.URL + "/request", AuthURL: prov.URL + "/authorize?scope=read,write&name=mono",
		AccessTokenURL: prov.URL + "/access", CallbackPort: port}
	vals, err := RunOAuth1Flow(context.Background(), cfg, "ck", "cs", 10*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	if vals["oauth_token"] != "final" || vals["oauth_token_secret"] != "finalsecret" || vals["screen_name"] != "ada" {
		t.Errorf("result = %v", vals)
	}
	if !strings.Contains(*opened, "oauth_token=temp") || !strings.Contains(*opened, "scope=read%2Cwrite") {
		t.Errorf("opened %q", *opened)
	}
	if h := prov.headers[0]; h["oauth_callback"] != fmt.Sprintf("http://localhost:%d/callback", port) || h["oauth_consumer_key"] != "ck" {
		t.Errorf("request-token header = %v", h)
	}
}

func TestRunOAuth1FlowRejectsWrongToken(t *testing.T) {
	prov := newFakeOAuth1Provider(t)
	port := freePort(t)
	stubOAuth1Browser(t, port, func(*url.URL) string { return "oauth_token=other&oauth_verifier=v123" })
	cfg := OAuth1Config{RequestTokenURL: prov.URL + "/request", AuthURL: prov.URL + "/authorize", AccessTokenURL: prov.URL + "/access", CallbackPort: port}
	if _, err := RunOAuth1Flow(context.Background(), cfg, "ck", "cs", 10*time.Second, nil); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("err = %v", err)
	}
	if _, err := RunOAuth1Flow(context.Background(), OAuth1Config{}, "ck", "cs", time.Second, nil); err == nil {
		t.Error("missing endpoints: no error")
	}
}

func TestManagerConnectOAuth1SavesTokens(t *testing.T) {
	mgr, _ := newManagerDB(t)
	prov := newFakeOAuth1Provider(t)
	port := freePort(t)
	stubOAuth1Browser(t, port, func(u *url.URL) string {
		return "oauth_token=" + u.Query().Get("oauth_token") + "&oauth_verifier=v123"
	})
	Registry["oauth1-test"] = PlatformDef{
		ID: "oauth1-test", Name: "OAuth1 Test", Methods: []AuthMethod{MethodOAuth1, MethodAPIKey},
		Fields: map[AuthMethod][]CredentialField{MethodOAuth1: {
			{Key: OAuth1ConsumerKey, Label: "Key", Required: true},
			{Key: OAuth1ConsumerSecret, Label: "Secret", Secret: true, Required: true},
		}},
		OAuth1: &OAuth1Config{RequestTokenURL: prov.URL + "/request", AuthURL: prov.URL + "/authorize", AccessTokenURL: prov.URL + "/access", CallbackPort: port},
	}
	t.Cleanup(func() { delete(Registry, "oauth1-test") })

	if _, err := mgr.ConnectOAuth1(context.Background(), "oauth1-test", map[string]string{OAuth1ConsumerKey: "ck"}, nil, ""); err == nil || !strings.Contains(err.Error(), OAuth1ConsumerSecret) {
		t.Fatalf("missing secret: err = %v", err)
	}
	conn, err := mgr.ConnectOAuth1(context.Background(), "oauth1-test", map[string]string{OAuth1ConsumerKey: "ck", OAuth1ConsumerSecret: "cs"}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := mgr.Get(context.Background(), conn.ID)
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{OAuth1ConsumerKey: "ck", OAuth1Token: "final", OAuth1TokenSecret: "finalsecret", "screen_name": "ada"} {
		if getStr(got.Data, k) != want {
			t.Errorf("data[%s] = %v, want %s", k, got.Data[k], want)
		}
	}
	if got.Method != MethodOAuth1 || got.Data["oauth_callback_confirmed"] != nil {
		t.Errorf("connection = %+v", got)
	}
}
