package modelname

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/testhome"
)

// Tests never touch the real ~/.monoagent.
func TestMain(m *testing.M) { testhome.Main(m) }

func catalogAt(t *testing.T, body string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "latest-models.json")
	if body != "" {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	prev := CatalogPath
	CatalogPath = func() string { return path }
	t.Cleanup(func() { CatalogPath = prev })
}

// firstPartyClaude clears what would point claude at another provider.
func firstPartyClaude(t *testing.T) {
	t.Helper()
	for _, k := range append([]string{"CLAUDE_CONFIG_DIR"}, OtherProviderEnv...) {
		t.Setenv(k, "")
	}
	prev := ManagedSettings
	ManagedSettings = func() []string { return nil }
	t.Cleanup(func() { ManagedSettings = prev })
}

func TestCurrentModel(t *testing.T) {
	catalogAt(t, `{"codex":["gpt-6.2-sol","gpt-6-luna"]}`)
	for _, c := range []struct{ runtime, in, want string }{
		{"claude", "claude-haiku-4-5-20251001", "claude-haiku-5-5"},
		{"claude", "claude-opus-4-8", "claude-opus-5-5"},
		{"claude", "claude-opus-4-7[1m]", "claude-opus-5-5"},
		{"claude", "claude-sonnet-4-20250514", "claude-sonnet-5-5"},
		{"claude", "claude-3-5-sonnet-20241022", "claude-sonnet-5-5"},
		{"claude", "claude-fable-5", "claude-fable-5-1"},
		{"claude", "claude-opus-5-5", "claude-opus-5-5"},
		{"claude", "claude-opus-6", "claude-opus-6"},
		{"claude", "opus", "opus"},
		{"claude", "", ""},
		{"claude", "us.anthropic.claude-opus-4-1", "us.anthropic.claude-opus-4-1"},
		{"codex", "gpt-5.6-luna", "gpt-6-luna"},
		{"codex", "gpt-6.1-sol", "gpt-6.2-sol"},
		// another runtime's own naming is not judged
		{"antigravity", "claude-opus-4-8", "claude-opus-4-8"},
	} {
		if got := CurrentModel(c.runtime, c.in); got != c.want {
			t.Errorf("CurrentModel(%s, %q) = %q, want %q", c.runtime, c.in, got, c.want)
		}
	}
}

func TestCatalogRaisesTheNewestModel(t *testing.T) {
	catalogAt(t, `{"claude":["opus","claude-opus-6-1","claude-haiku-4-5-20251001"],"_updatedAt":1}`)
	if got := CurrentModel("claude", "claude-opus-5-5"); got != "claude-opus-6-1" {
		t.Errorf("a newer listed model replaces the floor: got %q", got)
	}
	if got := CurrentModel("claude", "claude-haiku-4-5-20251001"); got != "claude-haiku-5-5" {
		t.Errorf("a stale catalog never pulls the floor back: got %q", got)
	}
}

func TestOtherRuntimesAreJudgedByTheirOwnListOnly(t *testing.T) {
	catalogAt(t, "")
	if got := CurrentModel("codex", "gpt-5.6-luna"); got != "gpt-5.6-luna" {
		t.Errorf("a codex model this machine never saw replaced: got %q", got)
	}
}

func TestOneModelUnderTwoNames(t *testing.T) {
	catalogAt(t, "")
	if !SameModel("claude", "claude-opus-5", "Claude-Opus-5-5") || SameModel("claude", "claude-opus-5-5", "claude-sonnet-5-5") {
		t.Error("SameModel reads a superseded id as its replacement")
	}
	firstPartyClaude(t)
	if !SameModel("claude", "opus", "claude-opus-5") || !SameModel("claude", "opus[1m]", "claude-opus-5-5") || SameModel("claude", "opus", "sonnet") {
		t.Error("an alias is its family's newest model")
	}
	if got := CurrentPin(" Claude/claude-opus-4-8@2.1 "); got != "claude/claude-opus-5-5" {
		t.Errorf("CurrentPin = %q", got)
	}
	for id, want := range map[string]string{"opus": "opus", "opus[1m]": "opus", "claude-opus-4-8": "opus", "kimi-k3": "kimi-k3"} {
		if got := ModelFamily("claude", id); got != want {
			t.Errorf("ModelFamily(%q) = %q, want %q", id, got, want)
		}
	}
	if !(LeadRank("opus") < LeadRank("claude-fable-5-1") && LeadRank("sonnet") < LeadRank("gpt-6.2-sol") && LeadRank("gpt-6.2-sol") < LeadRank("haiku")) {
		t.Error("LeadRank orders opus, fable, sonnet, others, small")
	}
	t.Setenv("ANTHROPIC_BASE_URL", "http://gateway")
	if SameModel("claude", "claude-opus-4-8", "claude-opus-5-5") {
		t.Error("a gateway's two ids are two models")
	}
}

// A lead and a judge that name one model differently are one pin, whichever side
// uses the older id or the alias.
func TestCurrentPinReadsEveryNameAsWhatRuns(t *testing.T) {
	catalogAt(t, "")
	firstPartyClaude(t)
	for _, same := range []string{"claude/opus", "claude/claude-opus-5@1", "Claude/Claude-Opus-5-5"} {
		if got := CurrentPin(same); got != "claude/claude-opus-5-5" {
			t.Errorf("CurrentPin(%q) = %q", same, got)
		}
	}
	if got := CurrentPin("a / b"); got != "a / b" {
		t.Errorf("a name this does not judge is left as written: %q", got)
	}
}
