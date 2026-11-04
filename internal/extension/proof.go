package extension

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
)

// The bridge proves it holds the pairing token before a client sends it.
//
// A relay request carries the token in a header. /monoagent/health, which a
// client probes first, is unauthenticated, so any loopback listener that
// answers the status shape used to pass the probe and then receive the token
// on the first relay. Now the client sends a fresh nonce with the probe and
// the bridge answers with `proof`, HMAC-SHA256(token, "v1" LF nonce LF path
// LF port), over the endpoint path and the port the bridge itself is bound
// to. A listener that does not know the token cannot produce it; one that
// forwards the question to the real bridge gets a proof for the real port,
// not its own. The token never leaves the process until the proof verifies.

const (
	healthPath = "/monoagent/health"
	// nonceParam is the status request's query parameter. An older bridge
	// ignores it (and answers without a proof); the side panel never sends it.
	nonceParam         = "nonce"
	minNonce, maxNonce = 16, 64 // hex characters
	proofVersion       = "v1"
	proofNonceBytes    = 16
)

// proofFor is the proof for a status request carrying nonce, answered on port
// for a client that dialled host ("127.0.0.1" and "::1" are different hosts).
// The format is unreleased before this change, so no old/new pair exists: a
// bridge from before proofs sends none and is refused (ErrBridgeNoProof).
func proofFor(token, nonce, path, host, port string) string {
	m := hmac.New(sha256.New, []byte(token))
	m.Write([]byte(proofVersion + "\n" + nonce + "\n" + path + "\n" + strings.ToLower(host) + "\n" + port))
	return hex.EncodeToString(m.Sum(nil))
}

// validNonce accepts only 16-64 hex characters, so the endpoint HMACs
// nothing but a short fixed-format value (it is not an oracle for arbitrary
// data).
func validNonce(n string) bool {
	if len(n) < minNonce || len(n) > maxNonce {
		return false
	}
	_, err := hex.DecodeString(n + strings.Repeat("0", len(n)%2))
	return err == nil
}

// Why a bridge could not be proven.
var (
	// ErrBridgeNoProof: the status answered without a proof (an older bridge, or
	// a listener that is not the bridge). Never sent the token, on any port.
	ErrBridgeNoProof = errors.New("the bridge is older than this CLI (it cannot prove it is the real one): restart it: `systemctl --user restart monoagent-bridge` (or `monoagentcli update` restarts it)")
	// ErrBridgeBadProof: the listener answered the status shape but its proof is wrong.
	ErrBridgeBadProof = errors.New("the listener answering as the bridge did not prove it holds your pairing token: not sending the token to it")
)

// noProofError is ErrBridgeNoProof naming the port that answered.
type noProofError struct{ port string }

func (e *noProofError) Error() string {
	return "the bridge on " + e.port + " is older than this CLI (it cannot prove it is the real one): restart it: `systemctl --user restart monoagent-bridge` (or `monoagentcli update` restarts it)"
}
func (e *noProofError) Is(t error) bool { return t == ErrBridgeNoProof }

// PortEnvPolicy, when set, says whether this process may take the bridge
// port from ExtensionPortEnv. It lives behind a hook because the check
// (internal/operator) imports this package. The command line's operator
// package installs it; unset, the override is honoured.
var portEnvPolicy func() bool

// SetPortEnvPolicy installs the policy (internal/operator, at init); nil removes it.
func SetPortEnvPolicy(f func() bool) { portEnvPolicy = f }

// OperatorProcess reports whether this process is the operator's own
// terminal for the purpose of the bridge's env override.
func OperatorProcess() bool { return portEnvPolicy == nil || portEnvPolicy() }

var warnPortOnce sync.Once

// PortOverride is the port ExtensionPortEnv names, or "" when it is unset or
// this process is not the operator's own terminal. Only the operator may
// point the bridge client at another port: an agent or an in-turn process
// that could set the variable would send its relay (and so the pairing
// token) to a listener of its own choosing.
func PortOverride() string {
	p := strings.TrimSpace(os.Getenv(ExtensionPortEnv))
	if p == "" {
		return ""
	}
	if !OperatorProcess() {
		warnPortOnce.Do(func() {
			fmt.Fprintf(os.Stderr, "warning: ignoring %s=%s: only the operator's own terminal may point the extension bridge at another port; using the default ports\n", ExtensionPortEnv, p)
		})
		return ""
	}
	return p
}

// verifyBridge asks baseURL's status with a fresh nonce and checks the proof
// against token. No request it sends carries the token.
func verifyBridge(client *http.Client, baseURL, token string) error {
	buf := make([]byte, proofNonceBytes)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Errorf("nonce: %w", err)
	}
	nonce := hex.EncodeToString(buf)
	u, err := url.Parse(baseURL)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), statusProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+healthPath+"?"+nonceParam+"="+nonce, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("no bridge answering at %s: %w", baseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s is held by something that is not a monoagent bridge (HTTP %d)", baseURL, resp.StatusCode)
	}
	var st Status
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil || st.Service != ServiceName {
		return fmt.Errorf("%s is held by something that is not a monoagent bridge", baseURL)
	}
	if st.Proof == "" {
		return &noProofError{port: u.Port()}
	}
	port := u.Port()
	if port == "" {
		_, port, _ = net.SplitHostPort(u.Host)
	}
	want := proofFor(token, nonce, healthPath, u.Hostname(), port)
	if !hmac.Equal([]byte(want), []byte(strings.ToLower(st.Proof))) {
		return ErrBridgeBadProof
	}
	return nil
}

// proveBridge checks that the listener at baseURL holds token. There is no
// exception: a listener that sends no proof (an old bridge, or a squatter) never
// receives the token, on any port.
func proveBridge(client *http.Client, baseURL, token string) error {
	return verifyBridge(client, baseURL, token)
}

// requestHost is the host part of a request's Host header, without brackets.
func requestHost(r *http.Request) string {
	h := r.Host
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	return strings.Trim(h, "[]")
}

// PickBridge chooses which of the candidate bridge base URLs to talk to: every
// candidate is probed and one that PROVES it holds the token wins, so a squatter
// that never proves cannot hide the real bridge on another candidate. With none
// proving it returns false (callers then get the prover's error on use).
func PickBridge(addrs []string) (string, bool) {
	token, _ := loadToken()
	if token == "" {
		for _, a := range addrs {
			if _, err := FetchStatus(a); err == nil {
				return a, true
			}
		}
		return "", false
	}
	return pickBridge(addrs, token)
}

func pickBridge(addrs []string, token string) (string, bool) {
	for _, a := range addrs {
		if verifyBridge(&http.Client{}, a, token) == nil {
			return a, true
		}
	}
	return "", false
}
