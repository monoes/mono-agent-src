package extension

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRequestPairingURLMintsASingleUseNonce(t *testing.T) {
	_, _, _ = startCaptureServer(t) // isolated HOME; the bridge wrote its token there
	base := "http://127.0.0.1:" + os.Getenv(ExtensionPortEnv)

	u, err := RequestPairingURL(base)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(u, base+"/monoagent/pair?n=") {
		t.Fatalf("pairing URL = %q", u)
	}
	parsed, _ := url.Parse(u)
	nonce := parsed.Query().Get("n")
	token, _ := loadToken()

	exchange := func() (int, string) {
		resp, err := http.Get(base + "/monoagent/pair/exchange?n=" + nonce)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var body struct {
			Token string `json:"token"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode, body.Token
	}
	if code, got := exchange(); code != http.StatusOK || got != token {
		t.Fatalf("first exchange: %d %q", code, got)
	}
	if code, _ := exchange(); code != http.StatusNotFound {
		t.Fatalf("second exchange of the same nonce: %d, want 404", code)
	}
}

func TestPairNonceIsGatedLikeTheRelay(t *testing.T) {
	_, _, _ = startCaptureServer(t)
	base := "http://127.0.0.1:" + os.Getenv(ExtensionPortEnv)
	token, err := loadToken()
	if err != nil {
		t.Fatal(err)
	}
	do := func(method string, mutate func(*http.Request)) int {
		req, _ := http.NewRequest(method, base+"/monoagent/pair/nonce", nil)
		req.Header.Set(tokenHeader, token)
		mutate(req)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	none := func(*http.Request) {}
	if got := do(http.MethodPost, none); got != http.StatusOK {
		t.Fatalf("local caller with the token: %d", got)
	}
	if got := do(http.MethodPost, func(r *http.Request) { r.Header.Set("Origin", "https://evil.test") }); got != http.StatusForbidden {
		t.Fatalf("web origin: %d, want 403", got)
	}
	if got := do(http.MethodPost, func(r *http.Request) { r.Host = "evil.test:" + os.Getenv(ExtensionPortEnv) }); got != http.StatusForbidden {
		t.Fatalf("rebound host: %d, want 403", got)
	}
	if got := do(http.MethodPost, func(r *http.Request) { r.Header.Del(tokenHeader) }); got != http.StatusUnauthorized {
		t.Fatalf("no token: %d, want 401", got)
	}
	if got := do(http.MethodPost, func(r *http.Request) { r.Header.Set(tokenHeader, "wrong") }); got != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d, want 401", got)
	}
	if got := do(http.MethodGet, none); got != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d, want 405", got)
	}

	path, _ := tokenPath()
	if err := os.WriteFile(path, []byte("not-the-bridges-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RequestPairingURL(base); !errors.Is(err, ErrPairingTokenRefused) {
		t.Fatalf("mismatched token: %v", err)
	}
}

func TestRequestPairingURLOnAnOlderBridge(t *testing.T) {
	setHome(t, t.TempDir())
	if _, err := CurrentToken(); err != nil {
		t.Fatal(err)
	}
	old := httptest.NewServer(http.NotFoundHandler())
	defer old.Close()
	if _, err := RequestPairingURL(old.URL); !errors.Is(err, ErrPairingPageUnsupported) {
		t.Fatalf("older bridge: %v, want ErrPairingPageUnsupported", err)
	}

	odd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"url":"https://evil.test/monoagent/pair?n=1"}`))
	}))
	defer odd.Close()
	if _, err := RequestPairingURL(odd.URL); err == nil {
		t.Fatal("a non-loopback pairing URL was accepted")
	}
}

func TestPairNonceExpires(t *testing.T) {
	srv, _, _ := startCaptureServer(t)
	base := "http://127.0.0.1:" + os.Getenv(ExtensionPortEnv)
	u, err := RequestPairingURL(base)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(u)
	nonce := parsed.Query().Get("n")

	srv.pairingMu.Lock()
	e := srv.pairingNonces[nonce]
	if e.expiresAt.Sub(time.Now()) > pairingNonceTTL {
		t.Errorf("nonce lives longer than %s", pairingNonceTTL)
	}
	e.expiresAt = time.Now().Add(-time.Second)
	srv.pairingNonces[nonce] = e
	srv.pairingMu.Unlock()

	resp, err := http.Get(base + "/monoagent/pair/exchange?n=" + nonce)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expired nonce: %d, want 404", resp.StatusCode)
	}
}
