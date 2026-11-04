package operator

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/cli/bridge"
	"github.com/monoes/mono-agent/internal/extension"
	"github.com/monoes/mono-agent/internal/hilsend"
	"github.com/monoes/mono-agent/internal/workflow"
)

// rogueListener is an untrusted loopback listener speaking the bridge's status
// shape (no proof: it does not know the token) and recording every request.
type rogueListener struct {
	*httptest.Server
	mu   sync.Mutex
	seen []http.Header
}

func newRogue(t *testing.T) *rogueListener {
	r := &rogueListener{}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.seen = append(r.seen, req.Header.Clone())
		r.mu.Unlock()
		if strings.HasSuffix(req.URL.Path, "/health") {
			_ = json.NewEncoder(w).Encode(extension.Status{Service: extension.ServiceName, Status: extension.StatusWaiting})
			return
		}
		_ = json.NewEncoder(w).Encode(extension.Response{ID: "x", Success: true})
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *rogueListener) port() string { return r.URL[strings.LastIndex(r.URL, ":")+1:] }

func (r *rogueListener) tokenSeen(token string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, h := range r.seen {
		for _, vs := range h {
			for _, v := range vs {
				if strings.Contains(v, token) {
					return true
				}
			}
		}
	}
	return false
}

// A process that names a loopback listener of its own in MONOAGENT_EXTENSION_PORT
// must never get the pairing token to it, whichever kind of process it is. A
// process that is not the operator's terminal never probes the named port; and
// whatever reaches the listener (a relay built straight on its URL) is stopped
// because the listener cannot prove it holds the token. Never touches the real
// bridge ports: a non-operator's probe list is checked, not probed.
func TestPortEnvNeverDeliversTheTokenToAnUntrustedListener(t *testing.T) {
	token, err := extension.CurrentToken()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		setup    func(t *testing.T)
		operator bool
	}{
		{"operator", func(t *testing.T) {}, true},
		{"workflow", func(t *testing.T) { t.Setenv(workflow.EnvWorkflowRun, "run-1") }, false},
		{"chat", func(t *testing.T) { t.Setenv(browserctl.EnvAICaller, "chat") }, false},
		{"agent", func(t *testing.T) { t.Setenv(hilsend.EnvAgentContext, "1") }, false},
		{"cleared-marker coder turn", func(t *testing.T) {
			SetTurnVerdict(func() string { return "a coder turn" })
			t.Cleanup(func() { SetTurnVerdict(nil) })
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rogue := newRogue(t)
			c.setup(t)
			t.Setenv(extension.ExtensionPortEnv, rogue.port())
			addrs := bridge.ProbeAddrs()
			onRogue := len(addrs) == 1 && addrs[0] == rogue.URL
			if !c.operator && onRogue {
				// the old behaviour: a marked process followed the variable
				if s, err := browserctl.FindBridge(); err == nil {
					_, _ = s.SendCommandTo(extension.Target{}, &extension.Command{Type: extension.CmdCreateTab}, time.Second)
				}
			}
			if c.operator {
				if !onRogue {
					t.Fatalf("the operator's own override is not honoured: %v", addrs)
				}
				if s, err := browserctl.FindBridge(); err == nil {
					_, _ = s.SendCommandTo(extension.Target{}, &extension.Command{Type: extension.CmdCreateTab}, time.Second)
				}
			}
			// and a relay built on the listener's URL, however it got there
			s := extension.NewRemoteSender(rogue.URL)
			_, _ = s.SendCommand(&extension.Command{Type: extension.CmdCreateTab}, time.Second)
			_ = s.SetBinding("i", nil, "")
			if rogue.tokenSeen(token) {
				t.Fatalf("the pairing token was delivered to an untrusted listener (%s)", c.name)
			}
		})
	}
}
