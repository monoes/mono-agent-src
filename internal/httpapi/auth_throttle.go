package httpapi

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/tlsserve"
)

// Failed-auth backoff and the token cache: every request that names a profile reads and
// decrypts that profile's token from the vault, so on a bind beyond loopback an
// unauthenticated caller could make the server do vault reads at its own request rate.
//
//   - On a listener beyond loopback (Serve turns it on), a remote address that has failed authFailLimit times within authFailWindow is
//     answered 429 (Retry-After) before any vault read, for a time that doubles with each
//     further failure up to authBackoffMax. A good request does not clear it. Loopback
//     callers are exempt: the CLI, the GUI and the extension share 127.0.0.1, and one
//     misconfigured client must not lock out the others.
//   - The decrypted token of a profile is kept for tokenCacheTTL, so repeated requests
//     are not a vault read each. A token rotated with `secret` commands stays valid for
//     up to that long.
const (
	authFailLimit  = 10
	authFailWindow = time.Minute
	authBackoffMax = time.Minute
	authTrackedMax = 4096
	tokenCacheTTL  = 5 * time.Second
)

type authFailure struct {
	count        int
	windowStart  time.Time
	blockedUntil time.Time
}

type authThrottle struct {
	now func() time.Time
	// on is set by Serve for a listener beyond loopback; off, nothing is throttled
	// (every caller of a loopback bind is local).
	on bool
	mu sync.Mutex
	by map[string]*authFailure
}

func newAuthThrottle() *authThrottle {
	return &authThrottle{now: time.Now, by: map[string]*authFailure{}}
}

// remoteOf is the caller's address without its port, "" for a loopback caller (never
// throttled).
func remoteOf(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if tlsserve.IsLoopbackAddr(host) {
		return ""
	}
	return host
}

// wait is how long the caller must wait, zero when it may be served.
func (t *authThrottle) wait(host string) time.Duration {
	if t == nil || host == "" || !t.on {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if f := t.by[host]; f != nil {
		if d := f.blockedUntil.Sub(t.now()); d > 0 {
			return d
		}
	}
	return 0
}

// fail records a refused request of host.
func (t *authThrottle) fail(host string) {
	if t == nil || host == "" || !t.on {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	f := t.by[host]
	if f == nil {
		if len(t.by) >= authTrackedMax {
			for h, o := range t.by { // make room: forget the addresses that are not blocked
				if !o.blockedUntil.After(now) {
					delete(t.by, h)
				}
			}
			if len(t.by) >= authTrackedMax {
				return
			}
		}
		f = &authFailure{windowStart: now}
		t.by[host] = f
	}
	if now.Sub(f.windowStart) > authFailWindow && !f.blockedUntil.After(now) {
		f.count, f.windowStart = 0, now
	}
	f.count++
	if over := f.count - authFailLimit; over >= 0 {
		d := time.Second << min(over, 6)
		f.blockedUntil = now.Add(min(d, authBackoffMax))
	}
}

// deny is the one 401 of every credential failure, counted against the caller's address.
func (s *Server) deny(w http.ResponseWriter, r *http.Request) {
	s.throttle.fail(remoteOf(r))
	writeUnauthorized(w)
}

// throttled answers 429 when the caller is backing off, and reports whether it did.
func (s *Server) throttled(w http.ResponseWriter, r *http.Request) bool {
	d := s.throttle.wait(remoteOf(r))
	if d <= 0 {
		return false
	}
	w.Header().Set("Retry-After", strconv.Itoa(int(d.Seconds())+1))
	writeError(w, http.StatusTooManyRequests, "too many failed authentications: retry later")
	return true
}

type cachedToken struct {
	tok     string
	expires time.Time
}

// tokenCache wraps a token reader with a short-lived cache per profile. Errors are not
// kept.
func tokenCache(read func(ctx context.Context, profileID string) (string, error), now func() time.Time) func(ctx context.Context, profileID string) (string, error) {
	var mu sync.Mutex
	kept := map[string]cachedToken{}
	return func(ctx context.Context, profileID string) (string, error) {
		mu.Lock()
		c, ok := kept[profileID]
		mu.Unlock()
		if ok && now().Before(c.expires) {
			return c.tok, nil
		}
		tok, err := read(ctx, profileID)
		if err != nil || tok == "" { // a profile with no token yet is looked at again
			return tok, err
		}
		mu.Lock()
		kept[profileID] = cachedToken{tok, now().Add(tokenCacheTTL)}
		mu.Unlock()
		return tok, nil
	}
}
