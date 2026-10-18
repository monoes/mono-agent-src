package mcp

// The API tools over a real session, as a host drives them: every line the server writes to stdout is a
// JSON-RPC message, and no secret is on the wire: not the key the user made, however the settings
// tools are called, and not the Jev key of the server's environment.

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/apikeys"
	"github.com/monoes/mono-agent/internal/orggrant"
)

func TestTheAPIToolsPutNoSecretOnTheWire(t *testing.T) {
	f := newConfigFixture(t, configSetup{registered: true})
	t.Setenv("TYPESAFE_API_KEY", jevKeyInTheEnvironment) // after the fixture, which clears it
	w := newWireSession(t, f.Server)

	_, secret := createKey(t, f.Server, "wire", false)
	huge := strings.Repeat("Q", 1<<16)

	for _, c := range []struct {
		name string
		args map[string]any
	}{
		{"api_status", nil},
		{"api_config_get", nil},
		{"api_config_get", map[string]any{"set": map[string]any{"max_concurrent": secret}}}, // sent to a reader
		{"api_config_set", set(map[string]any{"max_concurrent": "8", "turn_timeout": "15m"})},
		{"api_config_get", nil},
		{"api_config_set", set(map[string]any{"v1_addr": "0.0.0.0:9443"})},                     // refused: it reaches further
		{"api_config_set", set(map[string]any{"v1_addr": secret + ":9443"})},                   // the key where an address goes
		{"api_config_set", set(map[string]any{"tls_cert_file": secret, "tls_key_file": huge})}, // and where paths go
		{"api_config_set", set(map[string]any{secret: "x"})},                                   // as the name of a setting
		{"api_config_set", unset([]string{secret})},
		{"api_config_set", unset(secret)},
		{"api_config_set", map[string]any{"set": secret}}, // a malformed call
		{"api_config_set", unset([]string{"max_concurrent"})},
		{"api_config_apply", nil},
		{"api_auto_set", map[string]any{"enabled": true}}, // refused: no acknowledgement
		{"api_auto_set", map[string]any{"enabled": true, "acknowledge_egress": true}},
		{"api_auto_set", map[string]any{"enabled": secret}}, // the key where a boolean goes
		{"api_auto_set", map[string]any{"enabled": true, "acknowledge_egress": huge}},
		{"api_status", nil},
		{"api_auto_set", map[string]any{"enabled": false}},
	} {
		w.call(c.name, c.args)
	}
	out := w.finish()

	// Every line of stdout is a JSON-RPC message.
	for i, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var m struct {
			JSONRPC string `json:"jsonrpc"`
		}
		if err := json.Unmarshal([]byte(line), &m); err != nil || m.JSONRPC != "2.0" {
			t.Fatalf("line %d of stdout is not JSON-RPC: %q", i+1, scrubbed(line))
		}
	}
	if n := len(keyRE.FindAllString(out, -1)); n != 0 {
		t.Errorf("%d keys on the wire, want none", n)
	}
	if strings.Contains(out, apikeys.HashKey(secret)) {
		t.Error("the key's hash is on the wire")
	}
	if strings.Contains(out, jevKeyInTheEnvironment) {
		t.Error("the Jev key of the server's environment is on the wire")
	}
	if strings.Contains(out, strings.Repeat("Q", 64)) {
		t.Error("an argument of 64 KiB came back")
	}
	if left := f.saved(); left.V1Addr != "" || left.TLSCertFile != "" {
		t.Errorf("a refused call saved something: %+v", left)
	}
}

// api_models_list reads the saved settings now: with some saved, and a key pasted into each of its
// arguments, nothing but the document comes back, and no key. (The models come from a fake monomind,
// a shell script, so this does not run on Windows.)
func TestAPIModelsListPutsNoSecretOnTheWireWithSettingsSaved(t *testing.T) {
	pinAPIEnv(t)
	fakeAPIMonomind(t)
	f := newConfigFixture(t, configSetup{})
	t.Setenv("TYPESAFE_API_KEY", jevKeyInTheEnvironment)
	f.save("confinement=sandboxed", "image_runtimes=none")
	secret, err := apikeys.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	w := newWireSession(t, f.Server)
	for _, args := range []map[string]any{
		nil,
		{"for": "network"},
		{"for": secret}, {"confinement": secret}, {"context_confinement": secret}, {"auto_confinement": secret},
	} {
		w.call("api_models_list", args)
	}
	out := w.finish()
	for _, leaked := range []string{secret, jevKeyInTheEnvironment, apikeys.HashKey(secret)} {
		if strings.Contains(out, leaked) {
			t.Error("a secret is on the wire")
		}
	}
	if !strings.Contains(out, `\"confinement\": \"sandboxed\"`) {
		t.Errorf("the saved confinement is not in the document: %s", scrubbed(out))
	}
}

// Grant mode serves automations and nothing of the API: every tool whose name starts with api_ is
// refused by name and none is listed, however many there are now or later.
func TestGrantModeRefusesEveryAPIToolByName(t *testing.T) {
	f := newGrantFixture(t, orggrant.Tool{Wait: true})
	liveHeartbeat(t)

	var names []string
	for _, tl := range allTools() {
		if strings.HasPrefix(tl.name, "api_") {
			names = append(names, tl.name)
		}
	}
	if len(names) < 10 {
		t.Fatalf("only %d tools start with api_: %v", len(names), names)
	}
	lines := []string{request(100, "tools/list", map[string]interface{}{})}
	for i, name := range names {
		lines = append(lines, callToolReq(i+1, name, map[string]any{}))
	}
	resps := serveLines(t, f.server, lines...)

	for i, name := range names {
		text, isErr := toolText(t, respByID(t, resps, strconv.Itoa(i+1)))
		if !isErr || !strings.HasPrefix(text, codeRefusedGrant) {
			t.Errorf("%s in grant mode: %q (error %v), want a refusal %s", name, text, isErr, codeRefusedGrant)
		}
	}
	var res struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(respByID(t, resps, "100")["result"], &res); err != nil {
		t.Fatal(err)
	}
	for _, tl := range res.Tools {
		if strings.HasPrefix(tl.Name, "api_") {
			t.Errorf("grant mode lists %s", tl.Name)
		}
	}
}
