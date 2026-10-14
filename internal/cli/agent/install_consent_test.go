package agent

import (
	"bytes"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/agentinstall"
)

// --approve-script approves exactly the URL that was shown: a recipe that
// now names another script is refused, not run.
func TestScriptConsent(t *testing.T) {
	t.Parallel()
	shown := "https://hermes.example/install.sh"
	r := agentinstall.Recipe{Kind: agentinstall.KindScript, ScriptURL: shown, Shell: "bash"}
	changed := agentinstall.Recipe{Kind: agentinstall.KindScript, ScriptURL: "https://other.example/install.sh", Shell: "bash"}
	var out bytes.Buffer

	if !scriptConsent(false, shown, false, strings.NewReader(""), &out)(r) {
		t.Error("the approved URL was refused")
	}
	if scriptConsent(false, shown, false, strings.NewReader(""), &out)(changed) {
		t.Error("a different script ran on another URL's approval")
	}
	if !scriptConsent(true, "", false, strings.NewReader(""), &out)(changed) {
		t.Error("--yes did not approve")
	}
	if scriptConsent(false, "", false, strings.NewReader("y\n"), &out)(r) {
		t.Error("ran with nobody to ask")
	}
	out.Reset()
	if !scriptConsent(false, "", true, strings.NewReader("y\n"), &out)(r) || !strings.Contains(out.String(), shown) {
		t.Errorf("an asked yes: prompt %q", out.String())
	}
}
