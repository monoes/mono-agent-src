package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/monomind"
)

// fakeAPIMonomind points monomind at a script that lists three runtimes (the
// shapes a real monomind 2.22 reports): claude (chat-only), codex
// (sandboxed) and antigravity (unconfined).
func fakeAPIMonomind(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake monomind is a shell script")
	}
	script := `#!/bin/sh
if [ "$1" = "--version" ] && [ "$2" = "--json" ]; then
  echo '{"v":1,"version":"2.22.0","min_caller":"1.0.0","capabilities":["agent-exec","agent-scan","org-json-v1","agent-models","agent-exec-sandbox"]}'
  exit 0
fi
if [ "$1" = "agent" ] && [ "$2" = "scan" ]; then
  echo '{"v":1,"agents":[
    {"id":"claude","installed":true,"binary":"/usr/local/bin/claude","version":"2.1.0","install_hint":"","native_sandbox":"monomind","sandbox_modes":["read-only","workspace-write","full"]},
    {"id":"codex","installed":true,"binary":"/usr/local/bin/codex","version":null,"install_hint":"","native_sandbox":"full","sandbox_modes":["read-only","workspace-write","full"]},
    {"id":"antigravity","installed":true,"binary":"/usr/local/bin/agy","version":"1.2.14","install_hint":"","native_sandbox":"none","sandbox_modes":["restricted","full"]}]}'
  exit 0
fi
if [ "$1" = "agent" ] && [ "$2" = "models" ]; then
  case "$4" in
    claude) echo '{"v":1,"runtime":"claude","supported":true,"models":[{"id":"default","label":"Default"}]}' ;;
    codex) echo '{"v":1,"runtime":"codex","supported":true,"models":[{"id":"gpt-6-astra","label":"GPT-6-Astra"}]}' ;;
    antigravity) echo '{"v":1,"runtime":"antigravity","supported":true,"models":[{"id":"gemini-3.8-flash-high","label":"Gemini 3.8 Flash"}]}' ;;
  esac
  exit 0
fi
exit 2
`
	bin := filepath.Join(t.TempDir(), "monomind")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(monomind.EnvOverride, bin)
	monomind.ResetCapabilityCache()
	t.Cleanup(monomind.ResetCapabilityCache)
}

func decodeModels(t *testing.T, out string) apiModelsJSON {
	t.Helper()
	var got apiModelsJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not an api models document: %v\n%s", err, out)
	}
	return got
}

func allowedByID(m apiModelsJSON) map[string]bool {
	out := map[string]bool{}
	for _, x := range m.Models {
		out[x.ID] = x.Allowed
	}
	return out
}

func contextAllowedByID(m apiModelsJSON) map[string]bool {
	out := map[string]bool{}
	for _, x := range m.Models {
		out[x.ID] = x.ContextAllowed
	}
	return out
}

func TestAPIModelsMarksWhatEachPolicyAllows(t *testing.T) {
	db := newAPITestDB(t)
	fakeAPIMonomind(t)
	t.Setenv("MONOAGENT_API_CONFINEMENT", "")

	out, _, err := runAPI(t, db, "default", true, "models")
	if err != nil {
		t.Fatal(err)
	}
	loopback := decodeModels(t, out)
	if loopback.Policy.For != "loopback" || loopback.Policy.Confinement != "any" {
		t.Errorf("loopback policy: %+v", loopback.Policy)
	}
	// It evaluates this shell's flags and environment, not a running server: say so.
	if loopback.Policy.Source != "shell" {
		t.Errorf("policy.source = %q, want shell", loopback.Policy.Source)
	}
	classes := map[string]string{}
	for _, m := range loopback.Models {
		classes[m.ID] = m.Confinement
		if !m.Allowed {
			t.Errorf("%s must be allowed on a loopback listener", m.ID)
		}
	}
	if classes["claude/default"] != "chat-only" || classes["codex/gpt-6-astra"] != "sandboxed" ||
		classes["antigravity/default"] != "unconfined" || classes["codex/default"] != "sandboxed" {
		t.Errorf("classes: %v", classes)
	}

	// A key created with --context is held to chat-only unless raised.
	if loopback.Policy.ContextConfinement != "chat-only" {
		t.Errorf("context confinement: %+v", loopback.Policy)
	}
	if c := contextAllowedByID(loopback); !c["claude/default"] || c["codex/gpt-6-astra"] || c["antigravity/default"] {
		t.Errorf("models a context key may use by default: %v", c)
	}
	out, _, _ = runAPI(t, db, "default", true, "models", "--context-confinement", "sandboxed")
	raised := decodeModels(t, out)
	if c := contextAllowedByID(raised); !c["claude/default"] || !c["codex/gpt-6-astra"] || c["antigravity/default"] || raised.Policy.ContextConfinement != "sandboxed" {
		t.Errorf("--context-confinement sandboxed: %v %+v", c, raised.Policy)
	}
	// ... and never above what the listener serves.
	out, _, _ = runAPI(t, db, "default", true, "models", "--for", "network", "--context-confinement", "any")
	if capped := decodeModels(t, out); capped.Policy.ContextConfinement != "chat-only" || contextAllowedByID(capped)["codex/gpt-6-astra"] {
		t.Errorf("a network listener serves chat-only whatever the context maximum: %+v", capped.Policy)
	}

	out, _, _ = runAPI(t, db, "default", true, "models", "--for", "network")
	network := decodeModels(t, out)
	if network.Policy.Confinement != "chat-only" {
		t.Errorf("network policy: %+v", network.Policy)
	}
	if a := allowedByID(network); !a["claude/default"] || a["codex/gpt-6-astra"] || a["antigravity/default"] {
		t.Errorf("network allowed: %v", a)
	}

	out, _, _ = runAPI(t, db, "default", true, "models", "--for", "network", "--confinement", "sandboxed")
	if a := allowedByID(decodeModels(t, out)); !a["codex/gpt-6-astra"] || a["antigravity/default"] {
		t.Errorf("sandboxed override: %v", a)
	}

	// The environment sets the policy too, and the flag beats it.
	t.Setenv("MONOAGENT_API_CONFINEMENT", "chat-only")
	out, _, _ = runAPI(t, db, "default", true, "models")
	if a := allowedByID(decodeModels(t, out)); a["codex/gpt-6-astra"] {
		t.Errorf("MONOAGENT_API_CONFINEMENT=chat-only ignored: %v", a)
	}
	out, _, _ = runAPI(t, db, "default", true, "models", "--confinement", "any")
	if a := allowedByID(decodeModels(t, out)); !a["antigravity/default"] {
		t.Errorf("--confinement any must beat the environment: %v", a)
	}
}

func TestAPIModelsRejectsBadValuesWithExit3(t *testing.T) {
	db := newAPITestDB(t)
	fakeAPIMonomind(t)
	for _, args := range [][]string{{"models", "--for", "moon"}, {"models", "--confinement", "everything"}, {"models", "--context-confinement", "everything"}, {"models", "--auto-confinement", "everything"}} {
		if _, _, err := runAPI(t, db, "default", true, args...); exitCode(err) != 3 {
			t.Errorf("%v: exit %d (%v), want 3", args, exitCode(err), err)
		}
	}
}

func TestAPIModelsHumanTable(t *testing.T) {
	db := newAPITestDB(t)
	fakeAPIMonomind(t)
	t.Setenv("MONOAGENT_API_CONFINEMENT", "")
	out, _, err := runAPI(t, db, "default", false, "models", "--for", "network")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"MODEL", "CONFINEMENT", "CONTEXT KEY", "claude/default", "chat-only", "no (policy)", "yes", "this shell"} {
		if !strings.Contains(out, want) {
			t.Errorf("table lacks %q:\n%s", want, out)
		}
	}
}
