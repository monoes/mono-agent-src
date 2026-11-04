package jevtest

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
)

// Probe is a loopback listener that keeps everything a request carries (its
// headers and body), so a test can ask whether a key ever reached it. It
// answers 401, which the client does not retry.
type Probe struct {
	*httptest.Server
	mu   sync.Mutex
	seen []string
}

// NewProbe starts a Probe and closes it on cleanup.
func NewProbe(t testing.TB) *Probe {
	t.Helper()
	p := &Probe{}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var sb strings.Builder
		for k, vs := range r.Header {
			sb.WriteString(k + ": " + strings.Join(vs, ",") + "\n")
		}
		sb.Write(b)
		p.mu.Lock()
		p.seen = append(p.seen, sb.String())
		p.mu.Unlock()
		http.Error(w, `{"error":"probe"}`, http.StatusUnauthorized)
	}))
	t.Cleanup(p.Close)
	return p
}

// Calls is how many requests arrived.
func (p *Probe) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.seen)
}

// Saw reports whether any request carried secret.
func (p *Probe) Saw(secret string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, s := range p.seen {
		if strings.Contains(s, secret) {
			return true
		}
	}
	return false
}

// TwoHosts stands up the TypeSafe host (jev.DefaultBaseURL for the test) and
// a second listener the caller's environment names (TYPESAFE_BASE_URL), and
// clears TYPESAFE_API_KEY: a stored key must reach the first, never the
// second; only a TYPESAFE_API_KEY from the operator's own terminal pairs with
// the second.
func TwoHosts(t *testing.T) (typesafe, envHost *Probe) {
	t.Helper()
	typesafe, envHost = NewProbe(t), NewProbe(t)
	prev := jev.DefaultBaseURL
	jev.DefaultBaseURL = typesafe.URL
	t.Cleanup(func() { jev.DefaultBaseURL = prev })
	t.Setenv("TYPESAFE_BASE_URL", envHost.URL)
	t.Setenv("TYPESAFE_API_KEY", "")
	return typesafe, envHost
}

// TypeSafeHost stands up the TypeSafe host (jev.DefaultBaseURL for the test)
// with no TYPESAFE_BASE_URL and no TYPESAFE_API_KEY: a stored key and its
// host, nothing else.
func TypeSafeHost(t *testing.T) *Probe {
	t.Helper()
	p := NewProbe(t)
	DefaultHost(t, p.URL)
	t.Setenv("TYPESAFE_BASE_URL", "")
	t.Setenv("TYPESAFE_API_KEY", "")
	return p
}
