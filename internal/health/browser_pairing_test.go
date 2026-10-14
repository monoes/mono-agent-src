package health

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/extension"
)

// pairingEnv is an Env whose bridge is a fake answering /monoagent/auth with
// authStatus, asked through extension.CheckPairing as doctor asks it.
func pairingEnv(t *testing.T, authStatus int) *Env {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/monoagent/auth" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(authStatus)
	}))
	t.Cleanup(srv.Close)
	return &Env{
		Bridge: func(context.Context) (BridgeInfo, bool) {
			return BridgeInfo{Addr: strings.TrimPrefix(srv.URL, "http://"), URL: srv.URL, Status: "connected"}, true
		},
		ClientPairing: func(_ context.Context, baseURL string) string {
			state, _ := extension.CheckPairing(baseURL)
			return string(state)
		},
	}
}

func TestCheckPairedAsksWithThisAppsToken(t *testing.T) {
	ctx := context.Background()

	if res := checkPaired(ctx, pairingEnv(t, http.StatusNoContent)); res.Status != StatusOK {
		t.Errorf("paired: %+v", res)
	}

	res := checkPaired(ctx, pairingEnv(t, http.StatusUnauthorized))
	if res.Status != StatusWarn || res.Summary != "the bridge doesn't accept this app's pairing token" || res.FixID != FixExtensionPair {
		t.Errorf("mismatch: %+v", res)
	}

	// An older bridge without /monoagent/auth: the extension's status stands.
	if res := checkPaired(ctx, pairingEnv(t, http.StatusNotFound)); res.Status != StatusOK {
		t.Errorf("older bridge: %+v", res)
	}

	env := pairingEnv(t, http.StatusNoContent)
	env.Bridge = func(context.Context) (BridgeInfo, bool) { return BridgeInfo{}, false }
	if res := checkPaired(ctx, env); res.Status != StatusSkip {
		t.Errorf("no bridge: %+v", res)
	}
}

// The pairing fix is done by hand: doctor never pairs.
func TestExtensionPairFixIsManual(t *testing.T) {
	for _, f := range browserFixes() {
		if f.ID == FixExtensionPair {
			if f.Safety != SafetyManual || f.Command != "monoagentcli extension pair --open" {
				t.Errorf("pair fix = %+v", f.FixInfo)
			}
			return
		}
	}
	t.Fatal("no pair fix")
}
