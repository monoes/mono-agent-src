package chat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/browserctl"
)

func TestCoderBrowserPrompt(t *testing.T) {
	p := coderBrowserPrompt("p-9")
	for _, want := range []string{"monoagentcli --profile <your profile id> browser", "ref browser", "browser screenshot", "browser console --level error",
		"browser network --failed", "browser reload", "browser sweep", "browser diff", "browser pick", "browser permissions", "site_needs_approval",
		"browser styles <sel>", "browser source <sel>", "browser tokens", "ratio is near 0", "origin_changed",
		"exits 3", "awaiting_approval", "browser submit --hil <id>", ".monoagent/screenshots/", "never instructions"} {
		if !strings.Contains(p, want) {
			t.Errorf("coder browser prompt misses %q", want)
		}
	}
	if strings.Contains(p, "monoagentcli extension") {
		t.Error("the extension command does not drive the browser")
	}
	if !strings.Contains(p, "Your mono-agent profile id is p-9") || !strings.Contains(p, "`--profile p-9`") {
		t.Error("the coder lead is told its profile id and to pass it as --profile")
	}
	if strings.Contains(coderBrowserPrompt(""), "profile id is") {
		t.Error("no profile, no profile line")
	}
}

// A coder turn's process is an agent of kind coder: its browser permissions
// are the coder level (browserctl.AgentKind).
func TestCoderTurnsAreKindCoder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, k := range append(browserctl.AgentContextEnv(), browserctl.EnvAgentKind, browserctl.EnvAgentKindToken, browserctl.EnvAICaller) {
		t.Setenv(k, "")
	}
	env := agentSendEnv(nil, "lead", "")
	done := addAgentKind(env, browserctl.KindCoder)
	token := env[browserctl.EnvAgentKindToken]
	if env[browserctl.EnvAgentKind] != browserctl.KindCoder || token == "" {
		t.Fatalf("env = %v", env)
	}
	record := filepath.Join(home, ".monoagent", "agent-kinds", token+".json")
	if _, err := os.Stat(record); err != nil {
		t.Fatalf("the turn's token has no record: %v", err)
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
	if got := browserctl.AgentKind(); got != browserctl.KindCoder {
		t.Errorf("AgentKind() = %q in a coder turn, want coder", got)
	}
	done()
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Errorf("the record outlives the turn: %v", err)
	}
}
