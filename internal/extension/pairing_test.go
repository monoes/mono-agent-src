package extension

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// startPairingTestServer mirrors startAuthTestServer (server_test.go) but
// returns the *Server itself (needed for CreatePairingNonce) and the HTTP
// base URL rather than the WS URL.
func startPairingTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	setHome(t, t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())

	port := freePort(t)
	t.Setenv(ExtensionPortEnv, strconv.Itoa(port))

	srv := NewServer("127.0.0.1:9222", zerolog.Nop())
	srv.StartAsync(context.Background())
	t.Cleanup(func() { srv.Close() }) //nolint:errcheck

	base := "http://127.0.0.1:" + strconv.Itoa(port)
	deadline := time.Now().Add(3 * time.Second)
	for !Probe(base) {
		if time.Now().After(deadline) {
			t.Fatalf("server never became healthy at %s", base)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return srv, base
}

func TestPairingNonce_ExchangeReturnsTokenOnce(t *testing.T) {
	srv, base := startPairingTestServer(t)

	nonce, err := srv.CreatePairingNonce()
	if err != nil {
		t.Fatalf("CreatePairingNonce: %v", err)
	}

	resp, err := http.Get(base + "/monoagent/pair/exchange?n=" + nonce)
	if err != nil {
		t.Fatalf("GET exchange: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Token == "" || body.Token != srv.token {
		t.Fatalf("token = %q, want the server's real token", body.Token)
	}

	// Single-use: the same nonce must not exchange again.
	resp2, err := http.Get(base + "/monoagent/pair/exchange?n=" + nonce)
	if err != nil {
		t.Fatalf("GET exchange (second): %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("second exchange status = %d, want 404 (nonce must be single-use)", resp2.StatusCode)
	}
}

func TestPairingNonce_UnknownNonceRejected(t *testing.T) {
	_, base := startPairingTestServer(t)

	resp, err := http.Get(base + "/monoagent/pair/exchange?n=does-not-exist")
	if err != nil {
		t.Fatalf("GET exchange: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestPairingNonce_ExpiredNonceRejected(t *testing.T) {
	srv, base := startPairingTestServer(t)

	nonce, err := srv.CreatePairingNonce()
	if err != nil {
		t.Fatalf("CreatePairingNonce: %v", err)
	}
	// Force expiry rather than sleeping pairingNonceTTL (2 minutes) in a test.
	srv.pairingMu.Lock()
	entry := srv.pairingNonces[nonce]
	entry.expiresAt = time.Now().Add(-time.Second)
	srv.pairingNonces[nonce] = entry
	srv.pairingMu.Unlock()

	resp, err := http.Get(base + "/monoagent/pair/exchange?n=" + nonce)
	if err != nil {
		t.Fatalf("GET exchange: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (expired nonce)", resp.StatusCode)
	}
}

func TestPairPage_RequiresNonceParam(t *testing.T) {
	_, base := startPairingTestServer(t)

	resp, err := http.Get(base + "/monoagent/pair")
	if err != nil {
		t.Fatalf("GET pair: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 without ?n=", resp.StatusCode)
	}
}

func TestPairPage_ServesWithValidNonceParam(t *testing.T) {
	srv, base := startPairingTestServer(t)
	nonce, err := srv.CreatePairingNonce()
	if err != nil {
		t.Fatalf("CreatePairingNonce: %v", err)
	}

	resp, err := http.Get(base + "/monoagent/pair?n=" + nonce)
	if err != nil {
		t.Fatalf("GET pair: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

// TestPairingEndpoints_RejectExternalOrigin is a regression test: an
// earlier version applied checkOrigin to the WebSocket endpoint only, while
// handlePairPage/handlePairExchange had no origin check at all — reachable
// by an origin the WS would flatly reject. Both must apply the same policy.
func TestPairingEndpoints_RejectExternalOrigin(t *testing.T) {
	srv, base := startPairingTestServer(t)
	nonce, err := srv.CreatePairingNonce()
	if err != nil {
		t.Fatalf("CreatePairingNonce: %v", err)
	}

	client := &http.Client{}
	req, _ := http.NewRequest(http.MethodGet, base+"/monoagent/pair?n="+nonce, nil)
	req.Header.Set("Origin", "https://evil.com")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET pair with external origin: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("pair page status = %d, want 403 for an external Origin", resp.StatusCode)
	}

	req2, _ := http.NewRequest(http.MethodGet, base+"/monoagent/pair/exchange?n="+nonce, nil)
	req2.Header.Set("Origin", "https://evil.com")
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatalf("GET exchange with external origin: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("exchange status = %d, want 403 for an external Origin", resp2.StatusCode)
	}

	// The nonce must still be intact (rejected before ever being consumed) —
	// a legitimate same-origin request right after should still succeed.
	resp3, err := http.Get(base + "/monoagent/pair/exchange?n=" + nonce)
	if err != nil {
		t.Fatalf("GET exchange (legitimate, no Origin header): %v", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("legitimate exchange status = %d, want 200 (nonce should not have been consumed by the rejected request)", resp3.StatusCode)
	}
}

func TestServerBridge_PairingURL(t *testing.T) {
	srv, base := startPairingTestServer(t)
	bridge := &ServerBridge{Server: srv}

	url, ok := bridge.PairingURL()
	if !ok {
		t.Fatalf("PairingURL: not ready after server started successfully")
	}
	wantPrefix := base + "/monoagent/pair?n="
	if len(url) <= len(wantPrefix) || url[:len(wantPrefix)] != wantPrefix {
		t.Fatalf("PairingURL = %q, want prefix %q", url, wantPrefix)
	}
}

func TestRemoteBridge_DoesNotImplementPairingURL(t *testing.T) {
	// RemoteBridge (relay-through-another-process) must not offer pairing —
	// see PairingURL's doc comment for why: the process that owns the
	// server already offered it.
	var bridge interface{} = &RemoteBridge{Sender: NewRemoteSender("http://127.0.0.1:1")}
	if _, ok := bridge.(interface{ PairingURL() (string, bool) }); ok {
		t.Fatalf("RemoteBridge must not implement PairingURL")
	}
}

// TestCreatePairingNonce_NotReadyBeforeTokenExists covers the guard that
// stops a caller from minting a nonce for a token that doesn't exist yet —
// a freshly constructed Server (never Start'ed) has no token, so a nonce
// created against it would exchange for an empty string, which the
// extension would then send as its auth frame and get rejected by
// authenticate() with no clear indication why.
func TestCreatePairingNonce_NotReadyBeforeTokenExists(t *testing.T) {
	srv := NewServer("127.0.0.1:0", zerolog.Nop())
	if _, err := srv.CreatePairingNonce(); err == nil {
		t.Fatalf("expected an error creating a pairing nonce before the server has a token")
	}
}

// TestCreatePairingNonce_ConcurrentCreateAndExchange exercises pairingMu
// under real concurrent load (run with -race) — CreatePairingNonce's
// opportunistic expired-nonce pruning walks and deletes from the same map
// other goroutines are inserting into and exchangePairingNonce is deleting
// from, which is exactly the shape of bug a lock held for only part of
// that work would let slip through.
func TestCreatePairingNonce_ConcurrentCreateAndExchange(t *testing.T) {
	srv, base := startPairingTestServer(t)

	const n = 50
	nonces := make(chan string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			nonce, err := srv.CreatePairingNonce()
			if err != nil {
				t.Errorf("CreatePairingNonce: %v", err)
				return
			}
			nonces <- nonce
		}()
	}
	wg.Wait()
	close(nonces)

	seen := make(map[string]bool)
	var wg2 sync.WaitGroup
	for nonce := range nonces {
		if seen[nonce] {
			t.Fatalf("CreatePairingNonce produced a duplicate nonce: %s", nonce)
		}
		seen[nonce] = true
		wg2.Add(1)
		go func(nonce string) {
			defer wg2.Done()
			resp, err := http.Get(base + "/monoagent/pair/exchange?n=" + nonce)
			if err != nil {
				t.Errorf("GET exchange: %v", err)
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("exchange status = %d, want 200", resp.StatusCode)
			}
		}(nonce)
	}
	wg2.Wait()
}

// fakeConnChecker/fakePairingBridge let tryOpenPairingPage's own retry and
// give-up logic (cmd/monoagentcli/chrome_helper.go) be exercised without a
// real Server — that logic isn't reachable from this package directly, but
// the contract it depends on (PairingURL returning (_, false) until ready,
// RemoteBridge-shaped bridges never implementing PairingURL at all) is
// exactly what's asserted above and in TestServerBridge_PairingURL /
// TestRemoteBridge_DoesNotImplementPairingURL. See
// cmd/monoagentcli/chrome_helper_test.go for the CLI-side behavior test
// that exercises tryOpenPairingPage itself.
