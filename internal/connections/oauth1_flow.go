package connections

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// oauth1Browser opens the authorization page (tests replace it).
var oauth1Browser = openBrowser

// RunOAuth1Flow runs the OAuth 1.0a three-legged flow: it gets a request
// token (with a localhost callback), opens the provider's authorization
// page, waits for the callback's verifier and exchanges it for an access
// token. It returns every key of the access-token response (oauth_token,
// oauth_token_secret, and extras such as screen_name). Timeout defaults to
// 5 minutes.
func RunOAuth1Flow(ctx context.Context, cfg OAuth1Config, consumerKey, consumerSecret string, timeout time.Duration, progress func(msg, kind string)) (map[string]string, error) {
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	if cfg.RequestTokenURL == "" || cfg.AuthURL == "" || cfg.AccessTokenURL == "" {
		return nil, fmt.Errorf("oauth1: the platform needs a request token, authorization and access token URL")
	}
	port := cfg.CallbackPort
	if port == 0 {
		port = 9876
	}
	callback := fmt.Sprintf("http://localhost:%d/callback", port)

	temp, err := oauth1Post(cfg.RequestTokenURL, OAuth1Params{
		ConsumerKey: consumerKey, ConsumerSecret: consumerSecret,
		SignatureMethod: cfg.SignatureMethod, Extra: map[string]string{"oauth_callback": callback},
	})
	if err != nil {
		return nil, fmt.Errorf("oauth1 request token: %w", err)
	}
	tempToken, tempSecret := temp.Get("oauth_token"), temp.Get("oauth_token_secret")

	authURL, err := url.Parse(cfg.AuthURL)
	if err != nil {
		return nil, fmt.Errorf("oauth1: parse authorization URL: %w", err)
	}
	q := authURL.Query()
	q.Set("oauth_token", tempToken)
	authURL.RawQuery = q.Encode()

	verifierCh := make(chan string, 1)
	errCh := make(chan error, 1)
	send := func(v string, e error) {
		if e != nil {
			select {
			case errCh <- e:
			default:
			}
			return
		}
		select {
		case verifierCh <- v:
		default:
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("denied") != "" {
			send("", fmt.Errorf("authorization was denied"))
			http.Error(w, "denied", http.StatusBadRequest)
			return
		}
		if subtle.ConstantTimeCompare([]byte(q.Get("oauth_token")), []byte(tempToken)) != 1 {
			send("", fmt.Errorf("oauth_token mismatch in callback"))
			http.Error(w, "token mismatch", http.StatusBadRequest)
			return
		}
		// Some providers (Trello) send no verifier when it is not needed.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!DOCTYPE html><html><body><h2>&#x2713; Connected! You can close this tab.</h2></body></html>`)
		send(q.Get("oauth_verifier"), nil)
	})
	srv := &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", port), Handler: mux}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			send("", fmt.Errorf("http server: %w", err))
		}
	}()
	defer func() { _ = srv.Shutdown(context.Background()) }()

	fmt.Printf("→ Opening browser: %s\n", authURL)
	if err := oauth1Browser(authURL.String()); err != nil {
		fmt.Printf("→ Could not open browser automatically. Please open this URL manually:\n  %s\n", authURL)
	}
	if progress != nil {
		progress("Waiting for you to authorize in the browser…", "info")
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var verifier string
	select {
	case verifier = <-verifierCh:
	case err := <-errCh:
		return nil, err
	case <-timeoutCtx.Done():
		return nil, fmt.Errorf("oauth1 flow timed out after %s", timeout)
	}
	if progress != nil {
		progress("Authorized — requesting the access token…", "info")
	}

	extra := map[string]string{}
	if verifier != "" {
		extra["oauth_verifier"] = verifier
	}
	vals, err := oauth1Post(cfg.AccessTokenURL, OAuth1Params{
		ConsumerKey: consumerKey, ConsumerSecret: consumerSecret,
		Token: tempToken, TokenSecret: tempSecret,
		SignatureMethod: cfg.SignatureMethod, Extra: extra,
	})
	if err != nil {
		return nil, fmt.Errorf("oauth1 access token: %w", err)
	}
	out := make(map[string]string, len(vals))
	for k := range vals {
		out[k] = vals.Get(k)
	}
	return out, nil
}
