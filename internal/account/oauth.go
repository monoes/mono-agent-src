package account

// The browser half of the OAuth 2.1 sign-in and the endpoint discovery that
// Login uses and internal/library's own sign-in delegates to: PKCE, a loopback
// listener (RFC 8252) and endpoints pinned to the base host. Standard library
// only, so the import rule of this package holds.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
)

// OAuthEndpoints is the part of the authorization server metadata (RFC 8414) MonoAgent uses.
type OAuthEndpoints struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	RevocationEndpoint    string `json:"revocation_endpoint"`
}

// Fallback endpoints (Better-Auth's oauth-provider defaults) when the server
// publishes no metadata.
const (
	oauthAuthorizePath = "/api/auth/oauth2/authorize"
	oauthTokenPath     = "/api/auth/oauth2/token"
	oauthRevokePath    = "/api/auth/oauth2/revoke"
)

// DiscoverEndpoints reads the server metadata at baseURL. It always returns usable
// endpoints. An endpoint on another host than baseURL is used by path on baseURL,
// so a misconfigured issuer can never receive a code verifier or a token. The
// error is set only when no HTTP answer came back at all, so a caller that must
// not wait twice for a dead network can stop there.
func DiscoverEndpoints(ctx context.Context, hc *http.Client, baseURL string) (*OAuthEndpoints, error) {
	m := &OAuthEndpoints{}
	var unreachable error
	if req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/.well-known/oauth-authorization-server", nil); err == nil {
		if resp, err := hc.Do(req); err != nil {
			unreachable = err
		} else {
			if resp.StatusCode == http.StatusOK {
				_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(m)
			}
			resp.Body.Close()
		}
	}
	m.AuthorizationEndpoint = pinToBase(baseURL, m.AuthorizationEndpoint, oauthAuthorizePath)
	m.TokenEndpoint = pinToBase(baseURL, m.TokenEndpoint, oauthTokenPath)
	m.RevocationEndpoint = pinToBase(baseURL, m.RevocationEndpoint, oauthRevokePath)
	return m, unreachable
}

// pinToBase keeps endpoint when it is on baseURL's host, and otherwise uses its path on baseURL,
// so whatever endpoint holds, the result is on baseURL's host.
func pinToBase(baseURL, endpoint, fallback string) string {
	if endpoint == "" {
		return baseURL + fallback
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Path == "" {
		return baseURL + fallback
	}
	base, _ := url.Parse(baseURL)
	if strings.EqualFold(u.Host, base.Host) && u.Scheme == base.Scheme {
		return endpoint
	}
	// url.Parse also takes references that do not start with a slash ("@evil.example/x",
	// ".evil.example/x"): joined to the base as they stand, they would move the host.
	p := u.EscapedPath()
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if u.RawQuery != "" {
		p += "?" + u.RawQuery
	}
	return baseURL + p
}

// ErrBrowserRefused is what AuthorizeOptions.Open wraps when the caller may not open a browser at all (an
// agent's turn): there is nobody to finish the sign-in, so AuthorizeInBrowser ends at once instead of waiting.
var ErrBrowserRefused = errors.New("opening a browser is the user's own")

// AuthorizeOptions controls AuthorizeInBrowser.
type AuthorizeOptions struct {
	Open        func(string) error // opens the authorization URL (the system browser); nil = don't
	OnURL       func(string)       // told the authorization URL before Open runs
	Timeout     time.Duration      // how long to wait for the redirect back from the browser (only that wait, not the token exchange after it); default 5 minutes
	Label       string             // prefixes the errors, e.g. "library login"; default "sign-in"
	SuccessText string             // what the browser tab says once the sign-in went through
}

// AuthorizeResult is what the browser half hands to the token exchange: the code,
// the redirect URI it was issued for and the PKCE verifier.
type AuthorizeResult struct{ Code, Redirect, Verifier string }

// AuthorizeInBrowser sends the user to endpoint with params plus response_type, redirect_uri,
// state and a PKCE S256 challenge, and waits on 127.0.0.1:<random> for the redirect
// back (RFC 8252). The caller exchanges the returned code at the token endpoint.
func AuthorizeInBrowser(ctx context.Context, endpoint string, params url.Values, o AuthorizeOptions) (*AuthorizeResult, error) {
	if o.Timeout <= 0 {
		o.Timeout = 5 * time.Minute
	}
	if o.Label == "" {
		o.Label = "sign-in"
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("%s: listen on loopback: %w", o.Label, err)
	}
	defer ln.Close()
	redirect := fmt.Sprintf("http://127.0.0.1:%d/callback", ln.Addr().(*net.TCPAddr).Port)
	verifier, state := randomURLSafe(48), randomURLSafe(24)
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{}
	for k, v := range params {
		q[k] = v
	}
	q.Set("response_type", "code")
	q.Set("redirect_uri", redirect)
	q.Set("state", state)
	q.Set("code_challenge", base64.RawURLEncoding.EncodeToString(sum[:]))
	q.Set("code_challenge_method", "S256")
	sep := "?"
	if strings.Contains(endpoint, "?") {
		sep = "&"
	}
	authURL := endpoint + sep + q.Encode()

	type result struct {
		code string
		err  error
	}
	done := make(chan result, 1)
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		v := r.URL.Query()
		var res result
		switch {
		case subtle.ConstantTimeCompare([]byte(v.Get("state")), []byte(state)) != 1:
			callbackPage(w, false, "This sign-in link is not for this login attempt.", "")
			return // keep waiting for the real redirect
		case v.Get("error") != "":
			// Both come off the URL of a request: what reaches the terminal is plain text only.
			code, desc := oauthCode(v.Get("error")), printable(v.Get("error_description"))
			if code == "" {
				code = "error"
			}
			res.err = fmt.Errorf("%s: monoes.me refused: %s %s", o.Label, code, desc)
			callbackPage(w, false, "monoes.me did not authorize MonoAgent: "+code, "")
		case v.Get("code") == "":
			res.err = fmt.Errorf("%s: the redirect carried no code", o.Label)
			callbackPage(w, false, "The sign-in redirect carried no code.", "")
		default:
			res.code = v.Get("code")
			callbackPage(w, true, "", o.SuccessText)
		}
		select {
		case done <- res:
		default:
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	// Shutdown, not Close: Close cuts connections that are still writing, so the
	// browser that delivered the code could get EOF instead of the "you can close
	// this tab" page.
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()

	if o.OnURL != nil {
		o.OnURL(authURL)
	}
	var openErr error
	if o.Open != nil {
		// The URL was shown, so the user can open it by hand and the wait goes on; a wait that
		// ends without an answer says that the browser could not be opened.
		openErr = o.Open(authURL)
	}
	select {
	case res := <-done:
		if res.err != nil {
			return nil, res.err
		}
		return &AuthorizeResult{Code: res.code, Redirect: redirect, Verifier: verifier}, nil
	case <-ctx.Done():
		note := ""
		if openErr != nil {
			note = " (the browser could not be opened: " + printable(openErr.Error()) + ")"
		}
		if perr := parent.Err(); perr != nil {
			// The caller gave up (Ctrl-C, its own deadline): that is not the browser's silence.
			return nil, fmt.Errorf("%s: stopped waiting for the browser%s: %w", o.Label, note, perr)
		}
		return nil, errors.New(o.Label + ": no answer from the browser within " + o.Timeout.String() + note)
	}
}

// printable drops control characters and cuts s to 200 runes, for text that came off the network.
func printable(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if !unicode.IsControl(r) {
			out = append(out, r)
		}
		if len(out) == 200 {
			break
		}
	}
	return string(out)
}

func callbackPage(w http.ResponseWriter, ok bool, msg, success string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	title, body := "Logged in to monoes.me", success
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		title, body = "Sign-in failed", msg
	}
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>%s</title><body style="font-family:system-ui;margin:3rem"><h1>%s</h1><p>%s</p>`,
		html.EscapeString(title), html.EscapeString(title), html.EscapeString(body))
}

func randomURLSafe(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
