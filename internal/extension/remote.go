package extension

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/browser"
	"github.com/monoes/mono-agent/internal/capture"
)

// RemoteSender dispatches Commands through another local process's Server
// (typically the daemon) over its HTTP relay, instead of holding a WebSocket
// connection to the extension itself. This lets a short-lived CLI invocation
// share an already-connected extension session rather than racing another
// process for the fixed extension port.
type RemoteSender struct {
	baseURL string
	token   string
	// client has no Timeout of its own: every relayed command has its own
	// deadline (the command's timeout plus relaySlack, see SendCommand). A
	// fixed client cap silently cut every command longer than it — a
	// three-minute pick_element died at 90s, reported as a dead bridge.
	client *http.Client
	// slack overrides relaySlack (tests).
	slack time.Duration
}

// relaySlack is how much longer than the command's own timeout the relay
// request may take: the relaying process waits the full timeout for the
// extension and still has to write the reply back.
const relaySlack = 10 * time.Second

func (r *RemoteSender) requestSlack() time.Duration {
	if r.slack > 0 {
		return r.slack
	}
	return relaySlack
}

// NewRemoteSender creates a sender that relays through the server at baseURL
// (e.g. "http://127.0.0.1:9222"). Callers only construct this after Probe
// confirms a server is already listening there, so its token file is
// already written; if the token can't be read, requests are sent without
// one and the server's handleRelay rejects them with a clear 401 rather
// than proceeding unauthenticated.
func NewRemoteSender(baseURL string) *RemoteSender {
	token, _ := loadToken()
	return &RemoteSender{baseURL: baseURL, token: token, client: &http.Client{}}
}

// Probe reports whether a Server is actually listening and reachable at
// baseURL. Used to decide whether to relay through an existing process or
// fall back to starting a local server. It goes through FetchStatus so
// "something answered" is never mistaken for "a bridge answered": relaying
// commands into a Chrome CDP (or anything else that happens to hold the
// port) would wait forever for a reply that cannot come.
func Probe(baseURL string) bool {
	_, err := FetchStatus(baseURL)
	return err == nil
}

func (r *RemoteSender) SendCommand(cmd *Command, timeout time.Duration) (*Response, error) {
	return r.SendCommandTo(Target{}, cmd, timeout)
}

// SendCommandTo relays cmd to the browser t resolves to. An old bridge
// ignores the target parameters and uses its one browser.
func (r *RemoteSender) SendCommandTo(t Target, cmd *Command, timeout time.Duration) (*Response, error) {
	body, err := json.Marshal(cmd)
	if err != nil {
		return nil, fmt.Errorf("marshal command: %w", err)
	}
	if timeout <= 0 {
		timeout = defaultTimeout // what handleRelay applies to a missing timeout
	}
	url := fmt.Sprintf("%s/monoagent/relay?timeout_ms=%d", r.baseURL, timeout.Milliseconds())
	if enc := targetValues(t).Encode(); enc != "" {
		url += "&" + enc
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout+r.requestSlack())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tokenHeader, r.token)
	httpResp, err := r.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			// Same wording as Server.SendCommand's own timeout, so callers
			// classify both the same way.
			return nil, fmt.Errorf("command %s timed out after %s (relayed)", cmd.Type, timeout)
		}
		return nil, fmt.Errorf("relay request: %w", err)
	}
	defer httpResp.Body.Close()

	resp, err := decodeRelayResponse(httpResp)
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return resp, fmt.Errorf("extension error: %s", resp.Error)
	}
	return resp, nil
}

// ErrRelayUnauthorized is a relay request the bridge refused (401/403):
// this process's pairing token is not the bridge's — a different HOME, or
// the token was reset since.
var ErrRelayUnauthorized = errors.New("the bridge rejected this client: pairing token mismatch — run `monoagentcli extension status` / re-pair")

// ErrBridgeTooOld means the bridge answering predates multi-browser routing
// (no /monoagent/resolve). Commands still work but go to its one browser;
// restarting the bridge (the daemon, or `monoagentcli extension serve`)
// on the current version turns routing on.
var ErrBridgeTooOld = errors.New("the running bridge predates per-profile browsers — restart it (the daemon or `monoagentcli extension serve`) to route by profile")

// routeProbeTimeout bounds one resolve/browsers request: loopback, and the
// bridge answers from memory.
const routeProbeTimeout = 2 * time.Second

// relayBodyPreview bounds how much of an unexpected relay body is quoted.
const relayBodyPreview = 200

// decodeRelayResponse reads a relay reply, checking the HTTP status before
// the body: a refused or failed request answers with plain text
// ("unauthorized"), and decoding that as JSON produced
// "decode relay response: invalid character 'u'…", which named nothing
// anyone could act on. A non-2xx reply that still carries a JSON Response
// (the relay reporting the extension's own failure) is returned as is.
func decodeRelayResponse(httpResp *http.Response) (*Response, error) {
	if httpResp.StatusCode == http.StatusUnauthorized || httpResp.StatusCode == http.StatusForbidden {
		return nil, ErrRelayUnauthorized
	}
	body, err := io.ReadAll(io.LimitReader(httpResp.Body, maxMessageSize))
	if err != nil {
		return nil, fmt.Errorf("read relay response: %w", err)
	}
	var resp Response
	decodeErr := json.Unmarshal(body, &resp)
	if httpResp.StatusCode < 200 || httpResp.StatusCode > 299 {
		if decodeErr == nil && (resp.ID != "" || resp.Error != "") {
			return &resp, nil
		}
		return nil, fmt.Errorf("relay request failed: %s: %s", httpResp.Status, previewBody(body))
	}
	if decodeErr != nil {
		return nil, fmt.Errorf("decode relay response: %w", decodeErr)
	}
	return &resp, nil
}

// previewBody is a short, single-line quote of a reply body.
func previewBody(b []byte) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	if len(s) > relayBodyPreview {
		s = s[:relayBodyPreview] + "…"
	}
	if s == "" {
		return "(empty body)"
	}
	return s
}

// IsConnected reports whether the remote server currently has a live
// extension connection.
func (r *RemoteSender) IsConnected() bool {
	st, err := FetchStatus(r.baseURL)
	return err == nil && st.Connected
}

// Status returns the remote bridge's full self-description — what it bound,
// how long it has been up, and whether it is connected, waiting or turning
// the extension away unpaired. See status.go.
func (r *RemoteSender) Status() (Status, error) {
	return FetchStatus(r.baseURL)
}

// CreateTab asks the remote server's default browser to open a new tab.
func (r *RemoteSender) CreateTab(url string) (int, error) { return r.CreateTabFor(Target{}, url) }

// CreateTabFor opens a tab in the browser t resolves to.
func (r *RemoteSender) CreateTabFor(t Target, url string) (int, error) {
	resp, err := r.SendCommandTo(t, &Command{
		Type:   CmdCreateTab,
		Params: map[string]interface{}{"url": url},
	}, 30*time.Second)
	if err != nil {
		return 0, err
	}
	return parseTabID(resp)
}

// CloseTab asks the remote server's default browser to close a tab.
func (r *RemoteSender) CloseTab(tabID int) error { return r.CloseTabFor(Target{}, tabID) }

// CloseTabFor closes a tab in the browser t resolves to.
func (r *RemoteSender) CloseTabFor(t Target, tabID int) error {
	_, err := r.SendCommandTo(t, &Command{Type: CmdCloseTab, TabID: tabID}, 30*time.Second)
	return err
}

// ResolveTarget asks the bridge which browser t resolves to right now.
func (r *RemoteSender) ResolveTarget(t Target) (ConnInfo, error) {
	var info ConnInfo
	err := r.getRoute("/monoagent/resolve?"+targetValues(t).Encode(), &info)
	return info, err
}

// Browsers lists the browsers attached to the bridge, newest first.
func (r *RemoteSender) Browsers() ([]ConnInfo, error) {
	var body struct {
		Browsers []ConnInfo `json:"browsers"`
	}
	err := r.getRoute("/monoagent/browsers", &body)
	return body.Browsers, err
}

// SetBinding makes profiles the set of profiles one browser runs (none
// unbinds it). It sends the first one as the legacy `profile` too, so an
// extension from before sets still binds that one. An empty label leaves the
// browser's label alone.
func (r *RemoteSender) SetBinding(instance string, profiles []string, label string) error {
	profile := ""
	if len(profiles) > 0 {
		profile = profiles[0]
	}
	if profiles == nil {
		profiles = []string{}
	}
	params := map[string]interface{}{"profile": profile, "profiles": profiles}
	if label != "" {
		params["label"] = label
	}
	_, err := r.SendCommandTo(Target{Instance: instance}, &Command{Type: CmdSetBinding, Params: params}, 10*time.Second)
	return err
}

// getRoute does one authenticated GET against a routing endpoint and turns
// its error body back into the error the bridge raised.
func (r *RemoteSender) getRoute(path string, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), routeProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set(tokenHeader, r.token)
	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("bridge request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch resp.StatusCode {
	case http.StatusOK:
		return json.Unmarshal(body, out)
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrRelayUnauthorized
	}
	var re routeError
	if json.Unmarshal(body, &re) != nil || re.Code == "" {
		if resp.StatusCode == http.StatusNotFound {
			return ErrBridgeTooOld // Go's mux 404: no such endpoint
		}
		return fmt.Errorf("bridge answered %s: %s", resp.Status, previewBody(body))
	}
	return re.err()
}

// RemoteBridge adapts *RemoteSender to satisfy browser.ExtensionBridge, the
// same way ServerBridge adapts a locally-owned *Server.
type RemoteBridge struct {
	Sender *RemoteSender
}

var (
	_ browser.ExtensionBridge = (*RemoteBridge)(nil)
	_ Capturer                = (*RemoteBridge)(nil)
)

func (b *RemoteBridge) IsConnected() bool {
	return b.Sender.IsConnected()
}

func (b *RemoteBridge) CreateTab(url string) (int, error) {
	return b.Sender.CreateTab(url)
}

func (b *RemoteBridge) NewPage(tabID int) browser.PageInterface {
	return NewExtensionPage(b.Sender, tabID)
}

func (b *RemoteBridge) CloseTab(tabID int) error {
	return b.Sender.CloseTab(tabID)
}

// CapturePage relays a capture through the process that owns the extension
// connection; that process is the one that writes the envelope.
func (b *RemoteBridge) CapturePage(req CaptureRequest) (*capture.Result, error) {
	return b.Sender.CapturePage(req)
}
