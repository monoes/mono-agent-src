package connections

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

// OAuthResult holds the token data returned after a successful OAuth flow.
type OAuthResult struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	ExpiresIn    int    `json:"expires_in,omitempty"`
	Scope        string `json:"scope,omitempty"`
	// InstanceURL is Salesforce-specific: the per-org API base URL returned
	// alongside the token (e.g. https://yourInstance.salesforce.com). Empty
	// for every other provider.
	InstanceURL string `json:"instance_url,omitempty"`
	// Extra is the whole token response, for OAuthConfig.TokenFields.
	Extra map[string]any `json:"-"`
}

// copyTokenFields stores the named extra token-response keys on data.
func copyTokenFields(data map[string]interface{}, fields []string, extra map[string]any) {
	for _, f := range fields {
		if v, ok := extra[f]; ok && v != nil {
			data[f] = v
		}
	}
}

// RunOAuthFlow opens the browser to the provider's auth URL, starts a local
// callback server, waits for the redirect, exchanges the code for a token.
// Timeout defaults to 5 minutes if zero.
// progress, if non-nil, is called with (msg, kind) at key steps; kind is "info", "success", or "error".
func RunOAuthFlow(ctx context.Context, cfg OAuthConfig, timeout time.Duration, progress func(msg, kind string)) (*OAuthResult, error) {
	if timeout == 0 {
		timeout = 5 * time.Minute
	}

	state, err := randomState()
	if err != nil {
		return nil, fmt.Errorf("randomState: %w", err)
	}

	codeVerifier, err := generateCodeVerifier()
	if err != nil {
		return nil, fmt.Errorf("generateCodeVerifier: %w", err)
	}
	codeChallenge := computeCodeChallenge(codeVerifier)

	port := cfg.CallbackPort
	if port == 0 {
		port = 9876
	}

	redirectURI := fmt.Sprintf("http://localhost:%d/callback", port)

	authURL, err := buildAuthURL(cfg, redirectURI, state, codeChallenge)
	if err != nil {
		return nil, fmt.Errorf("buildAuthURL: %w", err)
	}

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	// Sends must never block: codeCh/errCh are cap-1 and read exactly once,
	// so a second /callback request (browser retry, prefetch, stray local
	// request) would otherwise wedge its handler forever on the send and
	// hang srv.Shutdown — and thus RunOAuthFlow — indefinitely.
	sendErr := func(e error) {
		select {
		case errCh <- e:
		default:
		}
	}
	sendCode := func(c string) {
		select {
		case codeCh <- c:
		default:
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()

		if gotState := q.Get("state"); subtle.ConstantTimeCompare([]byte(gotState), []byte(state)) != 1 {
			sendErr(fmt.Errorf("state mismatch"))
			http.Error(w, "state mismatch", http.StatusBadRequest)
			return
		}

		if providerErr := q.Get("error"); providerErr != "" {
			desc := q.Get("error_description")
			if desc != "" {
				sendErr(fmt.Errorf("provider error: %s — %s", providerErr, desc))
			} else {
				sendErr(fmt.Errorf("provider error: %s", providerErr))
			}
			http.Error(w, providerErr, http.StatusBadRequest)
			return
		}

		code := q.Get("code")
		if code == "" {
			sendErr(fmt.Errorf("missing code in callback"))
			http.Error(w, "missing code", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!DOCTYPE html><html><body><h2>&#x2713; Connected! You can close this tab.</h2></body></html>`)
		sendCode(code)
	})

	srv := &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", port),
		Handler: mux,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			sendErr(fmt.Errorf("http server: %w", err))
		}
	}()

	flowNote(progress, "Opening browser: "+authURL)
	flowNote(progress, fmt.Sprintf("Waiting for authorization on http://localhost:%d/callback", port))

	if err := openBrowser(authURL); err != nil {
		flowNote(progress, "Could not open browser automatically. Please open this URL manually: "+authURL)
	}
	if progress != nil {
		progress("Waiting for you to authorize in the browser…", "info")
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var code string
	select {
	case code = <-codeCh:
		if progress != nil {
			progress("Code received — requesting token from provider…", "info")
		}
		// success
	case flowErr := <-errCh:
		_ = srv.Shutdown(context.Background())
		return nil, flowErr
	case <-timeoutCtx.Done():
		_ = srv.Shutdown(context.Background())
		return nil, fmt.Errorf("oauth flow timed out after %s", timeout)
	}

	_ = srv.Shutdown(context.Background())

	return exchangeCode(cfg, code, redirectURI, codeVerifier)
}

// buildAuthURL builds the authorization URL with all required query params.
func buildAuthURL(cfg OAuthConfig, redirectURI, state, codeChallenge string) (string, error) {
	u, err := url.Parse(cfg.AuthURL)
	if err != nil {
		return "", fmt.Errorf("parse AuthURL: %w", err)
	}

	q := u.Query()
	q.Set("client_id", cfg.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	q.Set("response_type", "code")
	if len(cfg.Scopes) > 0 {
		q.Set("scope", strings.Join(cfg.Scopes, " "))
	}
	if codeChallenge != "" {
		q.Set("code_challenge", codeChallenge)
		q.Set("code_challenge_method", "S256")
	}
	// Platform-specific extra parameters (e.g., access_type=offline for Google).
	for k, v := range cfg.ExtraParams {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()

	return u.String(), nil
}

// exchangeCode exchanges an authorization code for tokens via POST to TokenURL.
func exchangeCode(cfg OAuthConfig, code, redirectURI, codeVerifier string) (*OAuthResult, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("client_id", cfg.ClientID)
	form.Set("client_secret", cfg.ClientSecret)
	if codeVerifier != "" {
		form.Set("code_verifier", codeVerifier)
	}

	body, status, err := PostTokenRequestWithAudienceFallback(cfg.PlatformID, cfg.TokenURL, form)
	if err != nil {
		return nil, fmt.Errorf("token request: %w", err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("token endpoint returned status %d: %s", status, string(body))
	}

	var result OAuthResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}
	_ = json.Unmarshal(body, &result.Extra)

	if result.AccessToken == "" {
		return nil, fmt.Errorf("token response missing access_token")
	}

	return &result, nil
}

// PostTokenRequestWithAudienceFallback POSTs a token request and, if
// Microsoft rejects it with AADSTS9002331/9002332 ("this app is
// personal-account-only, use /consumers" or its organizational-only
// counterpart), retries once against the corresponding tenant segment.
// This lets a single hardcoded /common/ TokenURL work for both
// personal-only and org-only Azure app registrations without per-connection
// configuration — different connections for the same platform may use
// different Azure apps with different audience settings.
// Exported so every OAuth token-refresh call site (CLI, GUI, workflow
// engine) shares this one fallback implementation instead of each keeping
// its own copy that silently misses the fix.
func PostTokenRequestWithAudienceFallback(platformID, tokenURL string, form url.Values) ([]byte, int, error) {
	body, status, err := postForm(platformID, tokenURL, form)
	if err != nil {
		return nil, 0, err
	}
	if status == http.StatusOK || !strings.Contains(tokenURL, "/common/") {
		return body, status, nil
	}
	var errResp struct {
		ErrorCodes []int `json:"error_codes"`
	}
	_ = json.Unmarshal(body, &errResp)
	for _, code := range errResp.ErrorCodes {
		if code == 9002331 || code == 9002332 {
			altSegment := "/consumers/"
			if code == 9002332 {
				altSegment = "/organizations/"
			}
			altURL := strings.Replace(tokenURL, "/common/", altSegment, 1)
			return postForm(platformID, altURL, form)
		}
	}
	return body, status, nil
}

// tokenHTTPClient bounds every token exchange/refresh so a blackholed
// provider token endpoint can't hang the caller (workflow node, connect
// flow) forever — http.DefaultClient has no timeout.
var tokenHTTPClient = &http.Client{Timeout: 30 * time.Second}

// usesBasicClientAuth reports whether platformID's token endpoint requires
// client credentials via HTTP Basic Auth (RFC 6749 §2.3.1) instead of as
// form body fields. Reddit rejects client_id/client_secret sent as form
// fields — https://github.com/reddit-archive/reddit/wiki/OAuth2 requires
// Basic Auth for every client type, including "installed" apps with an
// empty client_secret. Every other provider here keeps the current
// form-field path unchanged.
func usesBasicClientAuth(platformID string) bool {
	return platformID == "reddit"
}

// postForm POSTs a token request. Notion is handled by a separate path
// (postNotionJSON) since its token endpoint rejects this function's
// form-urlencoded body entirely; every other provider, including the
// Basic-Auth branch for Reddit, still sends a form-urlencoded body.
func postForm(platformID, tokenURL string, form url.Values) ([]byte, int, error) {
	if platformID == "notion" {
		return postNotionJSON(tokenURL, form)
	}

	body := form
	var basicUser, basicPass string
	useBasic := usesBasicClientAuth(platformID)
	if useBasic {
		basicUser, basicPass = form.Get("client_id"), form.Get("client_secret")
		body = url.Values{}
		for k, v := range form {
			if k == "client_id" || k == "client_secret" {
				continue
			}
			body[k] = v
		}
	}

	req, err := http.NewRequest(http.MethodPost, tokenURL, strings.NewReader(body.Encode()))
	if err != nil {
		return nil, 0, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if useBasic {
		req.SetBasicAuth(basicUser, basicPass)
	}

	resp, err := tokenHTTPClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("reading response: %w", err)
	}
	return respBody, resp.StatusCode, nil
}

// postNotionJSON exchanges a code (or refresh_token) via Notion's OAuth
// token endpoint, which — unlike every other provider registered here —
// requires a JSON body and Basic Auth, and rejects client_id/client_secret
// as body fields entirely: https://developers.notion.com/docs/authorization
func postNotionJSON(tokenURL string, form url.Values) ([]byte, int, error) {
	payload := map[string]string{"grant_type": form.Get("grant_type")}
	for _, k := range []string{"code", "redirect_uri", "refresh_token"} {
		if v := form.Get(k); v != "" {
			payload[k] = v
		}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal notion token request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, tokenURL, bytes.NewReader(data))
	if err != nil {
		return nil, 0, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(form.Get("client_id"), form.Get("client_secret"))

	resp, err := tokenHTTPClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("reading response: %w", err)
	}
	return body, resp.StatusCode, nil
}

// randomState generates a cryptographically random base64 state string (16 bytes).
func randomState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand.Read: %w", err)
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

// generateCodeVerifier generates a cryptographically random base64url string
// of 43–128 characters for use as a PKCE code_verifier (RFC 7636).
func generateCodeVerifier() (string, error) {
	b := make([]byte, 32) // 32 bytes → 43 base64url chars
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand.Read: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// computeCodeChallenge returns base64url(SHA-256(verifier)) per RFC 7636 S256.
func computeCodeChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// flowNote reports a step of the browser flow (the URL to open) through
// progress, or on stderr without one: stdout carries only the command's
// result, which is one JSON document under --json.
func flowNote(progress func(msg, kind string), msg string) {
	if progress != nil {
		progress(msg, "info")
		return
	}
	fmt.Fprintln(os.Stderr, "→ "+msg)
}

// openBrowser opens the URL in the default browser using `open` (macOS).
func openBrowser(u string) error {
	return exec.Command("open", u).Start()
}
