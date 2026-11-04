package extension

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Opening the auto-pairing page against a bridge another process runs.
//
// A process that owns the bridge mints a pairing nonce in-process
// (ServerBridge.PairingURL) and opens /monoagent/pair?n=... in the browser.
// `extension pair --open` usually runs while some other process (`extension
// serve`, the always-on service) holds the bridge, so it asks that bridge
// for the URL over POST /monoagent/pair/nonce. The endpoint is gated like the
// relay: same-machine callers only (no web Origin, loopback Host), and the
// caller must present the pairing token in X-Monoagent-Extension-Token. A
// caller with the token gains nothing it did not have; the nonce it gets is
// the same single-use, short-lived one the in-process flow issues.

// ErrPairingPageUnsupported means the bridge predates /monoagent/pair/nonce.
var ErrPairingPageUnsupported = errors.New("this bridge cannot open a pairing page (it predates the feature)")

// ErrPairingTokenRefused means the bridge refused this client's token, so
// it holds a different one (another HOME, or the token was reset).
var ErrPairingTokenRefused = errors.New("the bridge refused this client's pairing token")

// handlePairNonce serves POST /monoagent/pair/nonce: {"url": "<pairing page>"}.
func (s *Server) handlePairNonce(w http.ResponseWriter, r *http.Request) {
	if !checkOrigin(r) || !loopbackHost(r.Host) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get(tokenHeader)), []byte(s.token)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	addr, ok := s.Addr()
	if !ok {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	nonce, err := s.CreatePairingNonce()
	if err != nil {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"url": "http://" + addr + "/monoagent/pair?n=" + nonce})
}

// RequestPairingURL asks the bridge at baseURL ("http://127.0.0.1:9222")
// for a one-time pairing page URL, authenticating with this client's token.
func RequestPairingURL(baseURL string) (string, error) {
	token, err := loadToken()
	if err != nil {
		return "", fmt.Errorf("read the pairing token: %w", err)
	}
	switch err := proveBridge(newBridgeClient(0), baseURL, token); {
	case err == nil:
	case errors.Is(err, ErrBridgeBadProof):
		return "", ErrPairingTokenRefused
	default:
		return "", err
	}
	req, err := http.NewRequest(http.MethodPost, baseURL+"/monoagent/pair/nonce", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set(tokenHeader, token)
	resp, err := newBridgeClient(statusProbeTimeout).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		return "", ErrPairingPageUnsupported
	case http.StatusUnauthorized:
		return "", ErrPairingTokenRefused
	default:
		return "", fmt.Errorf("the bridge answered %s", resp.Status)
	}
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("read the bridge's answer: %w", err)
	}
	// Only ever open the bridge's own loopback pairing page.
	if !strings.HasPrefix(body.URL, "http://") || !strings.Contains(body.URL, "/monoagent/pair?n=") ||
		!loopbackHost(strings.SplitN(strings.TrimPrefix(body.URL, "http://"), "/", 2)[0]) {
		return "", fmt.Errorf("the bridge answered an unexpected pairing URL")
	}
	return body.URL, nil
}
