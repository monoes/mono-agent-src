package browserctl

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/extension"
)

// A bridge that cannot prove it holds the token (older than this CLI) gives ONE
// clear error at once: not the "no bridge" text, no wait, and no token sent.
func TestFindBridgeOlderBridgeIsNamedAtOnce(t *testing.T) {
	token, err := extension.CurrentToken()
	if err != nil {
		t.Fatal(err)
	}
	var sawToken bool
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Monoagent-Extension-Token") != "" {
			sawToken = true
		}
		_ = json.NewEncoder(w).Encode(extension.Status{Service: extension.ServiceName})
	}))
	defer old.Close()
	t.Setenv(extension.ExtensionPortEnv, old.URL[strings.LastIndex(old.URL, ":")+1:])
	start := time.Now()
	_, err = FindBridge()
	if time.Since(start) > 2*time.Second {
		t.Errorf("FindBridge took %s", time.Since(start))
	}
	if !errors.Is(err, extension.ErrBridgeNoProof) || errors.Is(err, ErrNoBridge) || !strings.Contains(err.Error(), "restart it") {
		t.Fatalf("err = %v, want the older-bridge error", err)
	}
	if sawToken || token == "" {
		t.Fatal("the token reached an older bridge")
	}
}
