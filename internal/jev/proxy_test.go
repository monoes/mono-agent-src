package jev

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// The key goes to the host itself, not to a proxy the environment names:
// HTTP_PROXY / HTTPS_PROXY are not read by the keyed client. (Go never proxies a
// loopback destination, so the target here is a name that only a proxy could
// reach.)
func TestTheKeyedClientIgnoresTheProxyEnvironment(t *testing.T) {
	var hits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(proxy.Close)
	for _, k := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("TYPESAFE_BASE_URL", "")
	c, err := NewClient("proxy-probe-key", "")
	if err != nil {
		t.Fatal(err)
	}
	// The standard library reads the proxy environment once per process, so the
	// transport is checked itself as well as by what a proxy sees.
	if tr, ok := c.HTTP.Transport.(*http.Transport); !ok || tr.Proxy != nil {
		t.Fatalf("the keyed client's transport reads a proxy: %#v", c.HTTP.Transport)
	}
	c.BaseURL = "http://typesafe.test.invalid"
	_, _ = c.Models(context.Background())
	_, _ = c.Ask(context.Background(), nil, map[string]Question{"q": {Type: TypeChoice, Criteria: map[string]any{"a": "A", "b": "B"}}})
	if hits.Load() != 0 {
		t.Fatalf("the proxy got %d request(s) carrying the key", hits.Load())
	}
}
