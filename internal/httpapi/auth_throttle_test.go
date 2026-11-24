package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAuthThrottleBacksOffARemoteAddressThatKeepsFailing(t *testing.T) {
	now := time.Unix(1000, 0)
	th := newAuthThrottle()
	th.on = true
	th.now = func() time.Time { return now }
	for i := 0; i < authFailLimit-1; i++ {
		th.fail("203.0.113.5")
	}
	if th.wait("203.0.113.5") != 0 {
		t.Fatal("blocked before the limit")
	}
	th.fail("203.0.113.5")
	d1 := th.wait("203.0.113.5")
	if d1 <= 0 {
		t.Fatal("not blocked at the limit")
	}
	if th.wait("203.0.113.6") != 0 {
		t.Error("another address is blocked")
	}
	now = now.Add(d1 + time.Millisecond)
	if th.wait("203.0.113.5") != 0 {
		t.Error("still blocked after the backoff")
	}
	th.fail("203.0.113.5")
	if d2 := th.wait("203.0.113.5"); d2 <= d1 {
		t.Errorf("backoff did not grow: %v then %v", d1, d2)
	}
	for i := 0; i < 100; i++ {
		th.fail("loopback-exempt-is-by-empty-host")
		th.fail("")
	}
	if th.wait("") != 0 {
		t.Error("the loopback caller (empty host) is throttled")
	}
}

// Once an address is backing off, its requests are answered 429 before any vault read;
// a loopback caller is never throttled.
func TestFailingRemoteAddressIsAnswered429WithoutAVaultRead(t *testing.T) {
	s := newTestServer(t, false)
	testToken(t, s, "default")
	s.throttle.on = true // what Serve does for a listener beyond loopback
	var reads int
	real := s.readToken
	s.readToken = func(ctx context.Context, id string) (string, error) {
		reads++
		return real(ctx, id)
	}
	call := func(remote string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/profiles/default/workflows", nil)
		req.Header.Set("Authorization", "Bearer wrong-token")
		req.RemoteAddr = remote
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec
	}
	for i := 0; i < authFailLimit; i++ {
		if rec := call("203.0.113.5:4000"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, rec.Code)
		}
	}
	reads = 0
	rec := call("203.0.113.5:4001")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" || reads != 0 {
		t.Errorf("after the limit: %d, Retry-After %q, %d vault reads", rec.Code, rec.Header().Get("Retry-After"), reads)
	}
	for i := 0; i < 3*authFailLimit; i++ {
		if rec := call("127.0.0.1:4000"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("loopback attempt %d: %d", i, rec.Code)
		}
	}
}

func TestTokenCacheReadsTheVaultOncePerTTL(t *testing.T) {
	now := time.Unix(1000, 0)
	reads := 0
	read := tokenCache(func(context.Context, string) (string, error) { reads++; return "tok", nil }, func() time.Time { return now })
	for i := 0; i < 5; i++ {
		if tok, err := read(context.Background(), "p"); tok != "tok" || err != nil {
			t.Fatalf("%q %v", tok, err)
		}
	}
	if reads != 1 {
		t.Errorf("%d reads within the TTL, want 1", reads)
	}
	now = now.Add(tokenCacheTTL + time.Millisecond)
	_, _ = read(context.Background(), "p")
	if reads != 2 {
		t.Errorf("%d reads after the TTL, want 2", reads)
	}
}
