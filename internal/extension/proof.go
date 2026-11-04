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

// proofFor is the proof for a status request carrying nonce, answered on port.
func proofFor(token, nonce, path, port string) string {
	m := hmac.New(sha256.New, []byte(token))
	m.Write([]byte(proofVersion + "\n" + nonce + "\n" + path + "\n" + port))
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
	// ErrBridgeNoProof: the status answered without a proof (an older bridge).
	ErrBridgeNoProof = errors.New("the bridge is older and cannot prove it holds your token: restart it (the daemon, or `systemctl --user restart` its service, or `monoagentcli extension serve`) so it runs the current version")
	// ErrBridgeBadProof: the listener answered the status shape but its proof is wrong.
	ErrBridgeBadProof = errors.New("the listener answering as the bridge did not prove it holds your pairing token: not sending the token to it")
)

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

// oldBridgeAccepted is the transitional rule for a bridge that sends no
// proof: only the operator's own terminal, only the default ports, and only
// when no port override is in play.
func oldBridgeAccepted(baseURL string) bool {
	if !OperatorProcess() || strings.TrimSpace(os.Getenv(ExtensionPortEnv)) != "" {
		return false
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	p := u.Port()
	return p == DefaultExtensionPort || p == FallbackExtensionPort
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
		return ErrBridgeNoProof
	}
	port := u.Port()
	if port == "" {
		_, port, _ = net.SplitHostPort(u.Host)
	}
	want := proofFor(token, nonce, healthPath, port)
	if !hmac.Equal([]byte(want), []byte(strings.ToLower(st.Proof))) {
		return ErrBridgeBadProof
	}
	return nil
}

// proveBridge is verifyBridge plus the transitional rule for old bridges.
func proveBridge(client *http.Client, baseURL, token string) error {
	err := verifyBridge(client, baseURL, token)
	if errors.Is(err, ErrBridgeNoProof) && oldBridgeAccepted(baseURL) {
		return nil
	}
	return err
}
