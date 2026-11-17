package extension

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// The routing endpoints: which browsers are attached, and which one a
// profile resolves to. Both need the relay token, because the answer names
// the user's profiles and browser labels. /monoagent/health, which is open,
// only says how many browsers there are.

// routeError is how the bridge reports a routing failure over HTTP, and how
// RemoteSender turns it back into the same Go error.
type routeError struct {
	Code    string   `json:"code"`
	Message string   `json:"error"`
	Profile string   `json:"profile,omitempty"`
	BoundTo []string `json:"boundTo,omitempty"`
}

const (
	routeCodeNoExtension = "no_extension"
	routeCodeNoBrowser   = "no_browser"
	routeCodeBrowserGone = "browser_gone"
)

func routeErrorFor(err error) (int, routeError) {
	var nb *NoBrowserError
	switch {
	case errors.As(err, &nb):
		return http.StatusNotFound, routeError{Code: routeCodeNoBrowser, Message: err.Error(), Profile: nb.Profile, BoundTo: nb.BoundTo}
	case errors.Is(err, ErrNoExtension):
		return http.StatusServiceUnavailable, routeError{Code: routeCodeNoExtension, Message: err.Error()}
	default:
		return http.StatusNotFound, routeError{Code: routeCodeBrowserGone, Message: err.Error()}
	}
}

func (e routeError) err() error {
	switch e.Code {
	case routeCodeNoBrowser:
		return &NoBrowserError{Profile: e.Profile, BoundTo: e.BoundTo}
	case routeCodeNoExtension:
		// The server's words (they say what to do next); the sentinel still matches.
		if rest := strings.TrimPrefix(e.Message, ErrNoExtension.Error()); rest != "" {
			return fmt.Errorf("%w%s", ErrNoExtension, rest)
		}
		return ErrNoExtension
	default:
		return fmt.Errorf("%w (%s)", ErrBrowserGone, e.Message)
	}
}

// targetFromQuery reads the profile/instance a relayed request is for.
func targetFromQuery(q url.Values) Target {
	return Target{Profile: strings.TrimSpace(q.Get("profile")), Instance: strings.TrimSpace(q.Get("instance"))}
}

// targetValues is the inverse, for the client side.
func targetValues(t Target) url.Values {
	v := url.Values{}
	if t.Profile != "" {
		v.Set("profile", t.Profile)
	}
	if t.Instance != "" {
		v.Set("instance", t.Instance)
	}
	return v
}

// routeAllowed applies the relay's gate: same machine, right token.
func (s *Server) routeAllowed(w http.ResponseWriter, r *http.Request) bool {
	if !checkOrigin(r) || !loopbackHost(r.Host) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	if s.token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get(tokenHeader)), []byte(s.token)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

// handleResolve serves GET /monoagent/resolve.
func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.routeAllowed(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	info, err := s.ResolveTarget(targetFromQuery(r.URL.Query()))
	if err != nil {
		code, body := routeErrorFor(err)
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(body)
		return
	}
	_ = json.NewEncoder(w).Encode(info)
}

// handleBrowsers serves GET /monoagent/browsers.
func (s *Server) handleBrowsers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.routeAllowed(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"browsers": s.Browsers()})
}
