package extensioncli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/extension"
)

// `extension pair --json` is JSON, and writes to the command's own output stream.
func TestExtensionPairJSONPrintsTheToken(t *testing.T) {
	pairTestHome(t)
	out, err := runPairCmdJSON(t, true)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := extension.CurrentToken()
	var got struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Token != token || token == "" {
		t.Fatalf("pair --json = %q (%v), want a JSON object holding the token %q", out, err, token)
	}
}

// `extension reset --json` reports the revocation as JSON on the command's output stream.
func TestExtensionResetJSON(t *testing.T) {
	gateHome(t)
	d := testDeps(true)
	d.Refusal = func() string { return "" }
	d.RecoveryRefusal = func() string { return "" }
	out, err := runExt(t, d, "reset")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Reset bool `json:"reset"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); err != nil || !got.Reset {
		t.Fatalf("reset --json = %q (%v)", out, err)
	}
}
