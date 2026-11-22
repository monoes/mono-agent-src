package extension

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// A DNS-rebound page reaches the bridge with its own name in Host and no
// Origin on a same-origin GET. The health, pairing and cdp endpoints answer
// only a request that names this machine.
func TestEndpointsRefuseNonLoopbackHost(t *testing.T) {
	srv := NewServer("127.0.0.1:0", zerolog.Nop())
	cases := map[string]struct {
		path    string
		handler http.HandlerFunc
	}{
		"health":        {"/monoagent/health", srv.handleHealth},
		"pair page":     {"/monoagent/pair?n=abc", srv.handlePairPage},
		"pair exchange": {"/monoagent/pair/exchange?n=abc", srv.handlePairExchange},
		"cdp":           {"/monoagent/cdp", srv.handleCdpSocket},
	}
	for name, c := range cases {
		req := httptest.NewRequest(http.MethodGet, c.path, nil)
		req.Host = "rebound.example:9222"
		rec := httptest.NewRecorder()
		c.handler(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s with a foreign Host: status %d, want 403", name, rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/monoagent/health", nil)
	req.Host = "127.0.0.1:9222"
	rec := httptest.NewRecorder()
	srv.handleHealth(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("health with a loopback Host: status %d, want 200", rec.Code)
	}
}

func TestRelayTimeoutIsBounded(t *testing.T) {
	cases := map[string]time.Duration{
		"":        defaultTimeout,
		"5000":    5 * time.Second,
		"-1":      defaultTimeout,
		"0":       defaultTimeout,
		"abc":     defaultTimeout,
		"600000":  10 * time.Minute,
		"600001":  defaultTimeout,
		"9999999": defaultTimeout,
	}
	for in, want := range cases {
		if got := relayTimeout(in); got != want {
			t.Errorf("relayTimeout(%q) = %v, want %v", in, got, want)
		}
	}
}
