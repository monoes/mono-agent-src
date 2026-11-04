package account_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/library/libraryfake"
)

// metadataServer answers the metadata path with status and body, and everything else 404.
func metadataServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/oauth-authorization-server" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Metadata that says nothing usable is no reason to fail a sign-in: the defaults of the server stand,
// and no error is reported, because an HTTP answer did come back.
func TestDiscoverFallsBackToTheDefaultsWhenTheMetadataIsNoUse(t *testing.T) {
	endpoints := `{"authorization_endpoint":"/a","token_endpoint":"/t","revocation_endpoint":"/r"}`
	for _, c := range []struct {
		name   string
		status int
		body   string
	}{
		{"not found", 404, endpoints},
		{"a server error with endpoints in its body", 500, endpoints},
		{"a redirect", 302, endpoints},
		{"not JSON", 200, "<html>welcome</html>"},
		{"a JSON array", 200, "[]"},
		{"an empty object", 200, "{}"},
		{"endpoints of the wrong type", 200, `{"authorization_endpoint":5,"token_endpoint":["x"]}`},
		{"empty strings", 200, `{"authorization_endpoint":"","token_endpoint":"","revocation_endpoint":""}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := metadataServer(t, c.status, c.body)
			ep, err := account.DiscoverEndpoints(context.Background(), &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, srv.URL)
			if err != nil || ep.AuthorizationEndpoint != srv.URL+"/api/auth/oauth2/authorize" ||
				ep.TokenEndpoint != srv.URL+"/api/auth/oauth2/token" || ep.RevocationEndpoint != srv.URL+"/api/auth/oauth2/revoke" {
				t.Fatalf("endpoints %+v, %v; want the three defaults and no error", ep, err)
			}
		})
	}
}

// The branches of pinToBase that look at the endpoint's own shape. Each row is served as the token
// endpoint, and the answer is checked for it alone.
func TestDiscoverReadsEachShapeOfEndpoint(t *testing.T) {
	const def = "/api/auth/oauth2/token"
	for _, c := range []struct{ name, endpoint, want string }{
		{"on the base host with a path and a query: kept as it stands", "{base}/custom/token?a=b", "{base}/custom/token?a=b"},
		{"on the base host with no path: the default", "{base}", "{base}" + def},
		{"on the base host with only a slash", "{base}/", "{base}/"},
		{"on another host, same scheme: its path on the base host", "http://other.example/custom/token?a=b", "{base}/custom/token?a=b"},
		{"on another port of the base host: its path on the base host", "http://127.0.0.1:1/custom/token", "{base}/custom/token"},
		{"on another host with no path: the default", "http://other.example", "{base}" + def},
		{"a URL that does not parse: the default", "http://[::1/token", "{base}" + def},
		{"a control character in the URL: the default", "http://other.example/\x7f", "{base}" + def},
		{"user info in front of the base host is kept off other hosts", "http://u:p@other.example/token", "{base}/token"},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				base := "http://" + r.Host
				e := strings.ReplaceAll(c.endpoint, "{base}", base)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"token_endpoint":` + quote(e) + `}`))
			}))
			defer srv.Close()
			ep, err := account.DiscoverEndpoints(context.Background(), srv.Client(), srv.URL)
			want := strings.ReplaceAll(c.want, "{base}", srv.URL)
			if err != nil || ep.TokenEndpoint != want {
				t.Fatalf("token endpoint %q, %v; want %q", ep.TokenEndpoint, err, want)
			}
		})
	}
}

func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			b.WriteString(`\u00`)
			b.WriteString(string("0123456789abcdef"[r>>4]))
			b.WriteString(string("0123456789abcdef"[r&15]))
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// A caller that gives up (Ctrl-C, or a deadline of its own) is told so. The wait it ended was not the
// browser's silence, and an error that blames the browser sends the user looking in the wrong place.
func TestAuthorizeSaysWhenTheCallerGaveUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o := account.AuthorizeOptions{Label: "account login", Timeout: time.Minute, OnURL: func(string) { cancel() }}
	_, err := account.AuthorizeInBrowser(ctx, "https://monoes.example/authorize", nil, o)
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "no answer from the browser") {
		t.Fatalf("a cancelled sign-in: %v", err)
	}

	dctx, dcancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer dcancel()
	_, err = account.AuthorizeInBrowser(dctx, "https://monoes.example/authorize", nil, account.AuthorizeOptions{Timeout: time.Minute})
	if !errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "no answer from the browser") {
		t.Fatalf("a caller's own deadline: %v", err)
	}

	// Its own timeout is still the browser's silence.
	_, err = account.AuthorizeInBrowser(context.Background(), "https://monoes.example/authorize", nil, account.AuthorizeOptions{Timeout: 100 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "no answer from the browser within 100ms") || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the sign-in's own timeout: %v", err)
	}
}

// A browser that cannot be opened is not the end of the sign-in (the URL was shown), but when the wait
// then ends without an answer the error says why nobody answered.
func TestAuthorizeSaysWhenTheBrowserCouldNotBeOpened(t *testing.T) {
	o := account.AuthorizeOptions{Label: "account login", Timeout: 100 * time.Millisecond,
		Open: func(string) error { return errors.New("xdg-open: no display\x1b[31m") }}
	_, err := account.AuthorizeInBrowser(context.Background(), "https://monoes.example/authorize", nil, o)
	if err == nil || !strings.Contains(err.Error(), "no answer from the browser within 100ms") ||
		!strings.Contains(err.Error(), "the browser could not be opened: xdg-open: no display") || strings.Contains(err.Error(), "\x1b") {
		t.Fatalf("a browser that failed to open: %v", err)
	}

	// The user opens the URL by hand: the sign-in goes through despite the failed open.
	o = account.AuthorizeOptions{Timeout: 10 * time.Second, OnURL: func(u string) {
		uq, _ := url.Parse(u)
		inBrowser(t, func() { followRedirect(t, u, url.Values{"code": {"by-hand"}, "state": {uq.Query().Get("state")}}) })
	}, Open: func(string) error { return errors.New("no browser") }}
	res, err := account.AuthorizeInBrowser(context.Background(), "https://monoes.example/authorize", nil, o)
	if err != nil || res.Code != "by-hand" {
		t.Fatalf("a sign-in finished by hand: %v", err)
	}
}

// What a redirect carries is text from a URL: the terminal gets plain text, whatever it holds.
func TestAuthorizeRefusalShowsPlainText(t *testing.T) {
	o := account.AuthorizeOptions{Label: "account login", Timeout: 10 * time.Second, Open: func(u string) error {
		uq, _ := url.Parse(u)
		inBrowser(t, func() {
			followRedirect(t, u, url.Values{"error": {"\x1b]0;pwned\x07"}, "error_description": {"line\r\nbreak\x1b[2J" + strings.Repeat("x", 1000)}, "state": {uq.Query().Get("state")}})
		})
		return nil
	}}
	_, err := account.AuthorizeInBrowser(context.Background(), "https://monoes.example/authorize", nil, o)
	if err == nil || !strings.Contains(err.Error(), "monoes.me refused") {
		t.Fatalf("refusal: %v", err)
	}
	for _, r := range err.Error() {
		if r < 0x20 || r == 0x7f {
			t.Fatalf("a control character reached the error: %q", err.Error())
		}
	}
	if len(err.Error()) > 400 {
		t.Fatalf("an unbounded text reached the error: %d bytes", len(err.Error()))
	}
}

// The redirect listener is for the user's own browser: it is reachable on the loopback interface and
// on no other address of the machine.
func TestAuthorizeListensOnLoopbackOnly(t *testing.T) {
	var other net.IP
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() && ipn.IP.To4() != nil {
			other = ipn.IP
			break
		}
	}
	if other == nil {
		t.Skip("this machine has no non-loopback IPv4 address to try")
	}
	o := account.AuthorizeOptions{Timeout: 10 * time.Second, Open: func(u string) error {
		uq, _ := url.Parse(u)
		redirect, _ := url.Parse(uq.Query().Get("redirect_uri"))
		inBrowser(t, func() {
			conn, err := net.DialTimeout("tcp", net.JoinHostPort(other.String(), redirect.Port()), time.Second)
			if err == nil {
				conn.Close()
				t.Errorf("the redirect listener answers on %s", other)
			}
			followRedirect(t, u, url.Values{"code": {"c"}, "state": {uq.Query().Get("state")}})
		})
		return nil
	}}
	if _, err := account.AuthorizeInBrowser(context.Background(), "https://monoes.example/authorize", nil, o); err != nil {
		t.Fatal(err)
	}
}

// Logout's revocation is a form with the token and the client, sent to the revocation endpoint of the
// host, and it never holds the logout for longer than its own bound when monoes.me does not answer.
func TestLogoutRevocationSendsTheTokenAndTheClientAndGivesUpWhenNobodyAnswers(t *testing.T) {
	fake := libraryfake.New()
	defer fake.Close()
	c, store := newFakeClient(t, fake)
	signInAtFake(t, c)
	rt, _ := store.LoadRefresh()

	forms := make(chan url.Values, 1)
	hang := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/oauth2/revoke" {
			http.NotFound(w, r)
			return
		}
		_ = r.ParseForm()
		forms <- r.PostForm
		<-hang
	}))
	defer srv.Close()
	defer close(hang)

	start := time.Now()
	if err := account.NewClient(srv.URL, store).Logout(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The revocation's own bound is five seconds; this one only tells a logout that
	// gave up from one that waits for the hung server, which is forever.
	if took := time.Since(start); took > testWait {
		t.Fatalf("a revocation nobody answers held the logout for %v", took)
	}
	form := <-forms
	if form.Get("token") != rt || form.Get("client_id") != account.ClientID || len(form) != 2 {
		t.Fatalf("the revocation sent the fields %v; want the token and the client id only", keysOf(form))
	}
	leftByLogout(t, store)
}

func keysOf(v url.Values) []string {
	var ks []string
	for k := range v {
		ks = append(ks, k)
	}
	return ks
}

// The mark a logout leaves is the later of the one on file and now: a clock that was set back since
// must not lower it.
func TestLogoutKeepsAMarkThatIsAheadOfTheClock(t *testing.T) {
	fake := libraryfake.New()
	defer fake.Close()
	c, store := newFakeClient(t, fake)
	signInAtFake(t, c)
	sess, err := store.Load()
	if err != nil || sess == nil {
		t.Fatalf("no session: %v", err)
	}
	ahead := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	sess.HW = ahead
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	if err := c.Logout(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := leftByLogout(t, store); got.HW.Before(ahead) {
		t.Fatalf("the mark went back from %v to %v", ahead, got.HW)
	}
}

// An opener that refuses (the caller is not the user) ends the sign-in at once: nobody can finish it, so it
// does not wait out its timeout.
func TestAuthorizeEndsAtOnceWhenTheOpenIsRefused(t *testing.T) {
	refused := fmt.Errorf("%w: this process runs inside a coder turn", account.ErrBrowserRefused)
	o := account.AuthorizeOptions{Label: "account login", Timeout: 3 * time.Second, Open: func(string) error { return refused }}
	start := time.Now()
	_, err := account.AuthorizeInBrowser(context.Background(), "https://monoes.example/authorize", nil, o)
	if !errors.Is(err, account.ErrBrowserRefused) || strings.Contains(err.Error(), "no answer from the browser") {
		t.Fatalf("a refused open: %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("a refused open waited %v", d)
	}
}
