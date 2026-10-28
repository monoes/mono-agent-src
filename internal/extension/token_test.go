package extension

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"testing"
)

// withTempHome points os.UserHomeDir (via $HOME) at a temp dir for the
// duration of the test, so token.go's file operations never touch the
// real ~/.monoagent state.
// setHome points both variables os.UserHomeDir reads (HOME on Unix, USERPROFILE on Windows) at dir.
func setHome(t testing.TB, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}

func withTempHome(t *testing.T) {
	t.Helper()
	setHome(t, t.TempDir())
}

func TestGenerateAndLoadToken(t *testing.T) {
	withTempHome(t)

	generated, err := generateToken()
	if err != nil {
		t.Fatalf("generateToken: %v", err)
	}
	if generated == "" {
		t.Fatal("generateToken returned an empty value")
	}

	loaded, err := loadToken()
	if err != nil {
		t.Fatalf("loadToken: %v", err)
	}
	if loaded != generated {
		t.Fatalf("loadToken returned a different value than generateToken wrote")
	}

	path, err := tokenPath()
	if err != nil {
		t.Fatalf("tokenPath: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat token file: %v", err)
	}
	if perm := info.Mode().Perm(); runtime.GOOS != "windows" && perm != 0o600 {
		t.Fatalf("token file mode = %o, want 0600", perm)
	}
}

func TestGenerateTokenIsRandomPerCall(t *testing.T) {
	withTempHome(t)

	first, err := generateToken()
	if err != nil {
		t.Fatalf("generateToken: %v", err)
	}
	second, err := generateToken()
	if err != nil {
		t.Fatalf("generateToken: %v", err)
	}
	if first == second {
		t.Fatal("generateToken produced the same value twice")
	}
}

func relayRequestBody(t *testing.T) []byte {
	t.Helper()
	body, err := json.Marshal(&Command{Type: CmdEval, Params: map[string]interface{}{"js": "1+1"}})
	if err != nil {
		t.Fatalf("marshal command: %v", err)
	}
	return body
}

func TestHandleRelayRejectsMissingOrWrongValue(t *testing.T) {
	withTempHome(t)
	expected, err := generateToken()
	if err != nil {
		t.Fatalf("generateToken: %v", err)
	}
	other, err := generateToken()
	if err != nil {
		t.Fatalf("generateToken: %v", err)
	}
	s := &Server{}
	s.token = expected

	cases := []struct {
		name    string
		headerV string
		setHdr  bool
	}{
		{"missing", "", false},
		{"mismatched", other, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/monoagent/relay", bytes.NewReader(relayRequestBody(t)))
			req.Host = "127.0.0.1:9222" // a local client; httptest defaults to example.com
			if tc.setHdr {
				req.Header.Set(tokenHeader, tc.headerV)
			}
			w := httptest.NewRecorder()

			s.handleRelay(w, req)

			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", w.Code, http.StatusUnauthorized)
			}
		})
	}
}

func TestHandleRelayAcceptsMatchingValue(t *testing.T) {
	withTempHome(t)
	expected, err := generateToken()
	if err != nil {
		t.Fatalf("generateToken: %v", err)
	}
	s := &Server{}
	s.token = expected
	s.pending = make(map[string]chan *Response)

	req := httptest.NewRequest(http.MethodPost, "/monoagent/relay", bytes.NewReader(relayRequestBody(t)))
	req.Host = "127.0.0.1:9222" // a local client; httptest defaults to example.com
	req.Header.Set(tokenHeader, expected)
	w := httptest.NewRecorder()

	s.handleRelay(w, req)

	// No extension is connected in this test, so the request should get past
	// the auth check and fail for that unrelated reason (never unauthorized).
	if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
		t.Fatalf("matching value was rejected: %d", w.Code)
	}
}

// The relay only serves same-machine callers: a CLI-style request (no
// Origin, loopback Host) is served; a web Origin or a DNS-rebound Host is
// refused before the token is checked.
func TestHandleRelayOriginAndHostGuard(t *testing.T) {
	withTempHome(t)
	token, err := generateToken()
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{token: token, pending: make(map[string]chan *Response)}
	serve := func(mutate func(*http.Request)) int {
		req := httptest.NewRequest(http.MethodPost, "/monoagent/relay", bytes.NewReader(relayRequestBody(t)))
		req.Host = "127.0.0.1:9222"
		req.Header.Set(tokenHeader, token)
		mutate(req)
		w := httptest.NewRecorder()
		s.handleRelay(w, req)
		return w.Code
	}
	if got := serve(func(*http.Request) {}); got == http.StatusForbidden || got == http.StatusUnauthorized {
		t.Fatalf("CLI-style request (no Origin) refused: %d", got)
	}
	for _, host := range []string{"localhost:9222", "[::1]:9222", "[::1]", "127.0.0.1", "localhost"} {
		if got := serve(func(r *http.Request) { r.Host = host }); got == http.StatusForbidden {
			t.Fatalf("loopback Host %q refused", host)
		}
	}
	for _, host := range []string{"evil.test", "[::2]:9222", "127.0.0.2:9222", "192.168.1.5:9222", "localhost.evil.test:9222"} {
		if got := serve(func(r *http.Request) { r.Host = host }); got != http.StatusForbidden {
			t.Fatalf("non-loopback Host %q: %d, want 403", host, got)
		}
	}
	if got := serve(func(r *http.Request) { r.Header.Set("Origin", "https://evil.test") }); got != http.StatusForbidden {
		t.Fatalf("web Origin: %d, want 403", got)
	}
	if got := serve(func(r *http.Request) { r.Host = "evil.test:9222" }); got != http.StatusForbidden {
		t.Fatalf("rebound Host: %d, want 403", got)
	}
	// Refused even without a token: the guard runs first.
	if got := serve(func(r *http.Request) {
		r.Header.Set("Origin", "https://evil.test")
		r.Header.Del(tokenHeader)
	}); got != http.StatusForbidden {
		t.Fatalf("web Origin without token: %d, want 403", got)
	}
}
