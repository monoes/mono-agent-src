package extension

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBridge is a loopback listener on an ephemeral port that answers the
// status shape, computing its proof with proofKey (or not at all), and
// records every request in order.
type fakeBridge struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []recordedReq
}

type recordedReq struct {
	method, path, query, token string
}

func newFakeBridge(t *testing.T, proof func(nonce, port string) string) *fakeBridge {
	f := &fakeBridge{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.reqs = append(f.reqs, recordedReq{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get(tokenHeader)})
		f.mu.Unlock()
		if r.URL.Path == healthPath {
			st := Status{Service: ServiceName, Status: StatusWaiting}
			if n := r.URL.Query().Get(nonceParam); n != "" && proof != nil {
				u, _ := url.Parse(f.URL)
				st.Proof = proof(n, u.Port())
			}
			_ = json.NewEncoder(w).Encode(st)
			return
		}
		_ = json.NewEncoder(w).Encode(Response{ID: "x", Success: true})
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeBridge) tokenSeen() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.reqs {
		if r.token != "" {
			return true
		}
	}
	return false
}

func relayOnce(base string) error {
	_, err := NewRemoteSender(base).SendCommand(&Command{Type: CmdCreateTab}, time.Second)
	return err
}

func TestProof_VerificationTable(t *testing.T) {
	withTempHome(t)
	token, err := generateToken()
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var recorded []string // proofs the right-token bridge gave for earlier nonces
	right := func(n, port string) string {
		p := proofFor(token, n, healthPath, port)
		mu.Lock()
		recorded = append(recorded, p)
		mu.Unlock()
		return p
	}
	// a proof for another endpoint path / another port, with the right token
	otherPath := func(n, port string) string { return proofFor(token, n, "/monoagent/relay", port) }
	otherPort := func(n, port string) string { return proofFor(token, n, healthPath, "1") }
	cases := []struct {
		name    string
		proof   func(n, port string) string
		wantErr error // nil: relays; else what stops it
	}{
		{"right token", right, nil},
		{"wrong token", func(n, port string) string { return proofFor("not-the-token", n, healthPath, port) }, ErrBridgeBadProof},
		{"proof for another path", otherPath, ErrBridgeBadProof},
		{"proof for another port (a forwarded answer)", otherPort, ErrBridgeBadProof},
		{"replayed proof (a stale nonce's)", func(n, port string) string {
			mu.Lock()
			defer mu.Unlock()
			if len(recorded) == 0 {
				return proofFor(token, "00000000000000000000000000000000", healthPath, port)
			}
			return recorded[0]
		}, ErrBridgeBadProof},
		{"missing proof, overridden port (old bridge)", nil, ErrBridgeNoProof},
		{"garbage proof", func(n, port string) string { return "zz" }, ErrBridgeBadProof},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeBridge(t, c.proof)
			// the replay case needs an earlier genuine exchange recorded
			if strings.HasPrefix(c.name, "replayed") {
				g := newFakeBridge(t, right)
				_ = relayOnce(g.URL)
			}
			err := relayOnce(f.URL)
			if c.wantErr == nil {
				if err != nil {
					t.Fatalf("a bridge that holds the token: %v", err)
				}
				if !f.tokenSeen() {
					t.Fatal("proof verified but the token never went out")
				}
				return
			}
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("err = %v, want %v", err, c.wantErr)
			}
			if f.tokenSeen() {
				t.Fatal("the token reached a listener that did not prove it holds it")
			}
			if Probe(f.URL) {
				t.Error("Probe accepts a listener that did not prove it holds the token")
			}
		})
	}
}

func TestProof_OldBridgeRuleIsDefaultPortsAndOperatorOnly(t *testing.T) {
	t.Setenv(ExtensionPortEnv, "")
	t.Cleanup(func() { SetPortEnvPolicy(nil) })
	for _, base := range []string{"http://127.0.0.1:9222", "http://127.0.0.1:9323"} {
		if !oldBridgeAccepted(base) {
			t.Errorf("operator, default port, no override: %s must be accepted (transitional)", base)
		}
	}
	if oldBridgeAccepted("http://127.0.0.1:9400") {
		t.Error("an old bridge on a non-default port was accepted")
	}
	t.Setenv(ExtensionPortEnv, "9222")
	if oldBridgeAccepted("http://127.0.0.1:9222") {
		t.Error("an old bridge was accepted although the port is overridden by env")
	}
	t.Setenv(ExtensionPortEnv, "")
	SetPortEnvPolicy(func() bool { return false })
	if oldBridgeAccepted("http://127.0.0.1:9222") {
		t.Error("an old bridge was accepted for a process that is not the operator's terminal")
	}
}

func TestProof_TokenNeverGoesBeforeTheProofVerifies(t *testing.T) {
	withTempHome(t)
	token, _ := generateToken()
	f := newFakeBridge(t, func(n, port string) string { return proofFor(token, n, healthPath, port) })
	if err := relayOnce(f.URL); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	if len(f.reqs) < 2 {
		t.Fatalf("requests = %+v", f.reqs)
	}
	first := f.reqs[0]
	if first.method != http.MethodGet || first.path != healthPath || !strings.HasPrefix(first.query, nonceParam+"=") || first.token != "" {
		t.Fatalf("the first request must be the status request with a nonce and no token, got %+v", first)
	}
	if f.reqs[1].path != "/monoagent/relay" || f.reqs[1].token != token {
		t.Fatalf("second request = %+v", f.reqs[1])
	}
	firstQuery := f.reqs[0].query
	f.mu.Unlock()
	// fresh nonce per probe
	_ = relayOnce(f.URL)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reqs) < 3 || firstQuery == f.reqs[2].query {
		t.Error("the nonce repeats between probes")
	}
}

func TestProof_ServerAnswersOnlyAWellFormedNonceAndNeverTheToken(t *testing.T) {
	srv, base := startPairingTestServer(t)
	token, _ := loadToken()
	get := func(q string) (int, string) {
		resp, err := http.Get(base + healthPath + q)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	_ = srv
	for _, bad := range []string{"?nonce=", "?nonce=abc", "?nonce=" + strings.Repeat("a", 65), "?nonce=" + strings.Repeat("g", 32), "?nonce=%00%00%00%00%00%00%00%00%00%00%00%00%00%00%00%00"} {
		if code, body := get(bad); code != http.StatusBadRequest || strings.Contains(body, "proof") {
			t.Errorf("%s: %d %q, want a plain 400", bad, code, body)
		}
	}
	nonce := strings.Repeat("ab", 16)
	code, body := get("?nonce=" + nonce)
	if code != 200 || strings.Contains(body, token) {
		t.Fatalf("%d, body leaks the token or fails: %q", code, body)
	}
	var st Status
	_ = json.Unmarshal([]byte(body), &st)
	u, _ := url.Parse(base)
	if st.Proof != proofFor(token, nonce, healthPath, u.Port()) {
		t.Errorf("proof = %q", st.Proof)
	}
	if code, body := get(""); code != 200 || strings.Contains(body, "proof") {
		t.Errorf("no nonce asked, yet %d %q (old clients and the side panel must see the old answer)", code, body)
	}
	if !Probe(base) {
		t.Error("Probe rejects a real bridge")
	}
}

func TestPortOverride_IgnoredWithAWarningUnlessOperator(t *testing.T) {
	t.Cleanup(func() { SetPortEnvPolicy(nil) })
	t.Setenv(ExtensionPortEnv, "9400")
	SetPortEnvPolicy(func() bool { return true })
	if got := PortOverride(); got != "9400" {
		t.Fatalf("the operator's override = %q, want 9400", got)
	}
	SetPortEnvPolicy(func() bool { return false })
	warnPortOnce = sync.Once{}
	r, w, _ := os.Pipe()
	old := os.Stderr
	os.Stderr = w
	got := PortOverride()
	os.Stderr = old
	w.Close()
	msg, _ := io.ReadAll(r)
	if got != "" {
		t.Fatalf("a non-operator's override = %q, want it ignored", got)
	}
	if !strings.Contains(string(msg), "ignoring "+ExtensionPortEnv) {
		t.Errorf("no warning on stderr: %q", msg)
	}
	if addr, err := resolveListenAddr("127.0.0.1:9222"); err != nil || addr != "127.0.0.1:9222" {
		t.Errorf("a non-operator's bind = %q, %v: the override must not apply", addr, err)
	}
}
