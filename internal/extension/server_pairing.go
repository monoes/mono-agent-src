package extension

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// pairingNonceEntry is one outstanding auto-pairing nonce.
type pairingNonceEntry struct {
	token     string
	expiresAt time.Time
}

// pairingNonceTTL bounds how long an auto-pairing nonce stays valid — long
// enough to cover the OS actually opening a browser tab, short enough that
// a stale nonce (tab left open, browser opened slowly) can't be reused
// later.
const pairingNonceTTL = 2 * time.Minute

// CreatePairingNonce issues a short-lived, single-use nonce that exchanges
// for the real extension token via handlePairExchange — see PairingURL in
// exec.go for why: it lets the auto-pairing flow put a URL in the user's
// browser (and history) without ever putting the long-lived pairing token
// there. Opportunistically prunes expired nonces so this map can't grow
// unbounded across a long-running daemon's lifetime.
func (s *Server) CreatePairingNonce() (string, error) {
	if s.token == "" {
		return "", fmt.Errorf("extension server: not ready (no token yet)")
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate pairing nonce: %w", err)
	}
	nonce := hex.EncodeToString(buf)

	s.pairingMu.Lock()
	defer s.pairingMu.Unlock()
	if s.pairingNonces == nil {
		s.pairingNonces = make(map[string]pairingNonceEntry)
	}
	now := time.Now()
	for n, e := range s.pairingNonces {
		if now.After(e.expiresAt) {
			delete(s.pairingNonces, n)
		}
	}
	s.pairingNonces[nonce] = pairingNonceEntry{token: s.token, expiresAt: now.Add(pairingNonceTTL)}
	return nonce, nil
}

// exchangePairingNonce consumes a nonce (valid exactly once) and returns the
// real token it maps to.
func (s *Server) exchangePairingNonce(nonce string) (string, bool) {
	s.pairingMu.Lock()
	defer s.pairingMu.Unlock()
	entry, ok := s.pairingNonces[nonce]
	if ok {
		delete(s.pairingNonces, nonce) // single-use regardless of expiry outcome
	}
	if !ok || time.Now().After(entry.expiresAt) {
		return "", false
	}
	return entry.token, true
}

// handlePairPage serves the auto-pairing landing page the CLI opens in the
// user's browser while ensureExtensionConnected waits for a connection.
// Pairing itself happens via the content script manifest.json declares for
// this exact path (chrome-extension/pair_bridge.js) — this handler only
// needs to render feedback; it never sees or handles the token itself.
func (s *Server) handlePairPage(w http.ResponseWriter, r *http.Request) {
	if !checkOrigin(r) || !loopbackHost(r.Host) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.URL.Query().Get("n") == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8"><p>Missing pairing code.</p>`))
		return
	}
	_, _ = w.Write([]byte(pairPageHTML))
}

// handlePairExchange consumes a one-time nonce (query param "n") and
// returns the real pairing token as {"token": "..."}, or 404 if the nonce
// is missing, unknown, already used, or expired. Applies the same Origin
// policy as the WebSocket endpoint (checkOrigin) — same-machine callers
// only. That check is still no stronger than the WebSocket's own (any
// chrome-extension:// or loopback origin passes); the nonce's single-use,
// short TTL is what actually bounds exposure beyond that shared baseline.
func (s *Server) handlePairExchange(w http.ResponseWriter, r *http.Request) {
	if !checkOrigin(r) || !loopbackHost(r.Host) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	nonce := r.URL.Query().Get("n")
	token, ok := s.exchangePairingNonce(nonce)
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "unknown or expired pairing code"})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"token": token})
}

// pairPageHTML is the static body handlePairPage serves. All the real work
// happens in the injected content script; this is purely user feedback.
const pairPageHTML = `<!doctype html>
<html>
<head>
<meta charset="utf-8">
<title>MonoAgent Bridge — Pairing</title>
<style>
  body { font-family: -apple-system, system-ui, sans-serif; background: #0b0f14; color: #e2e8f0;
         display: flex; align-items: center; justify-content: center; height: 100vh; margin: 0; }
  #box { text-align: center; }
  #status { font-size: 15px; margin-top: 12px; color: #94a3b8; }
</style>
</head>
<body>
  <div id="box">
    <div style="font-size:20px;font-weight:600;">MonoAgent Bridge</div>
    <div id="status">Pairing…</div>
  </div>
  <script>
    // The real work is done by the extension's own content script
    // (pair_bridge.js), injected automatically because this exact path is
    // declared in manifest.json's content_scripts. It posts progress back
    // to this page via window.postMessage so this tab can self-close.
    let heardFromBridge = false;
    window.addEventListener("message", (ev) => {
      if (ev.source !== window || !ev.data || ev.data.source !== "monoagent-pair-bridge") return;
      heardFromBridge = true;
      const status = document.getElementById("status");
      if (ev.data.ok) {
        status.textContent = "Paired! You can close this tab.";
        setTimeout(() => window.close(), 1500);
      } else {
        status.textContent = "Pairing failed: " + (ev.data.error || "unknown error") +
          " — paste the token manually in the extension popup instead.";
      }
    });
    // If the content script never fires at all — extension disabled, not
    // yet reloaded after an update (content_scripts changes need a manual
    // reload), or this page was opened in a browser without the extension
    // installed — the page would otherwise say "Pairing…" forever with no
    // indication anything is wrong. Surface that after a few seconds
    // instead of hanging silently.
    setTimeout(() => {
      if (heardFromBridge) return;
      document.getElementById("status").textContent =
        "Still waiting — make sure the MonoAgent Bridge extension is installed, enabled, " +
        "and reloaded at chrome://extensions (or the equivalent in your browser), " +
        "or paste the token manually in the extension popup instead.";
    }, 8000);
  </script>
</body>
</html>`
