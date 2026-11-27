package monomind

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/modelname"
)

func catalogAt(t *testing.T, body string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "latest-models.json")
	if body != "" {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	prev := modelname.CatalogPath
	modelname.CatalogPath = func() string { return path }
	t.Cleanup(func() { modelname.CatalogPath = prev })
}

func TestCurrentModelsDropsWhatTheListReplaces(t *testing.T) {
	catalogAt(t, "")
	firstPartyClaude(t)
	ids := func(ms []RuntimeModel) string {
		var out []string
		for _, m := range ms {
			out = append(out, m.ID)
		}
		return strings.Join(out, " ")
	}
	old := []RuntimeModel{
		{ID: "opus", ResolvedID: "claude-opus-5-5"},
		{ID: "haiku", ResolvedID: "claude-haiku-4-5-20251001"},
		{ID: "claude-haiku-4-5-20251001"},
		{ID: "claude-opus-4-8"},
		{ID: "claude-sonnet-4-6"},
	}
	// Nothing in the list replaces haiku or sonnet: they stay, and a turn on
	// them runs the newest.
	if got := ids(currentModels("claude", old)); got != "opus haiku claude-haiku-4-5-20251001 claude-sonnet-4-6" {
		t.Errorf("without replacements = %s", got)
	}
	all := append(old, RuntimeModel{ID: "claude-haiku-5-5"}, RuntimeModel{ID: "sonnet"})
	if got := ids(currentModels("claude", all)); got != "opus claude-haiku-5-5 sonnet" {
		t.Errorf("with replacements = %s", got)
	}
	t.Setenv("ANTHROPIC_BASE_URL", "http://gateway")
	if got := currentModels("claude", all); len(got) != len(all) {
		t.Errorf("a gateway keeps its own list: %s", ids(got))
	}
}

// firstPartyClaude clears what would point claude at another provider.
func firstPartyClaude(t *testing.T) {
	t.Helper()
	for _, k := range append([]string{"CLAUDE_CONFIG_DIR"}, modelname.OtherProviderEnv...) {
		t.Setenv(k, "")
	}
	prev := modelname.ManagedSettings
	modelname.ManagedSettings = func() []string { return nil }
	t.Cleanup(func() { modelname.ManagedSettings = prev })
}

func TestBuiltinClaudeListHasNoSupersededModel(t *testing.T) {
	catalogAt(t, "")
	for _, m := range claudeModels {
		if n, old := Superseded("claude", m.ID); old {
			t.Errorf("built-in %s is replaced by %s", m.ID, n)
		}
	}
}

func TestWithLatestClaudeEnv(t *testing.T) {
	catalogAt(t, "")
	none := func(string) string { return "" }
	caller := map[string]string{"A": "1"}
	got := withLatestClaudeEnv(caller, none)
	if got["ANTHROPIC_DEFAULT_HAIKU_MODEL"] != "claude-haiku-5-5" || got["ANTHROPIC_DEFAULT_OPUS_MODEL"] != "claude-opus-5-5" || got["A"] != "1" {
		t.Errorf("env = %v", got)
	}
	if len(caller) != 1 {
		t.Errorf("the caller's map changed: %v", caller)
	}
	got = withLatestClaudeEnv(nil, func(k string) string {
		return map[string]string{"ANTHROPIC_DEFAULT_HAIKU_MODEL": "claude-haiku-4-5", "ANTHROPIC_DEFAULT_OPUS_MODEL": "claude-opus-5-5[1m]"}[k]
	})
	if got["ANTHROPIC_DEFAULT_HAIKU_MODEL"] != "claude-haiku-5-5" {
		t.Errorf("an old inherited pin is replaced: %v", got)
	}
	if _, set := got["ANTHROPIC_DEFAULT_OPUS_MODEL"]; set {
		t.Errorf("a current inherited pin stays: %v", got)
	}
}

func TestUseLatestModels(t *testing.T) {
	catalogAt(t, "")
	firstPartyClaude(t)
	home := t.TempDir()
	ambient := func(k string) string { return map[string]string{"CLAUDE_CONFIG_DIR": home}[k] }
	turn := func(cwd string, env map[string]string) ExecOptions {
		o := ExecOptions{Runtime: "claude", Model: "claude-opus-4-8", Cwd: cwd, Env: env}
		useLatestModels(&o, ambient)
		return o
	}
	if o := turn("", nil); o.Model != "claude-opus-5-5" || o.Env["ANTHROPIC_DEFAULT_HAIKU_MODEL"] != "claude-haiku-5-5" {
		t.Errorf("first-party turn = %s %v", o.Model, o.Env)
	}
	untouched := func(why string, o ExecOptions) {
		t.Helper()
		if o.Model != "claude-opus-4-8" || o.Env["ANTHROPIC_DEFAULT_HAIKU_MODEL"] != "" {
			t.Errorf("%s keeps its own models: %s %v", why, o.Model, o.Env)
		}
	}
	untouched("a gateway named in the turn's env", turn("", map[string]string{"ANTHROPIC_BASE_URL": "http://gateway"}))
	cwd := t.TempDir()
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(cwd, ".claude", "settings.local.json"), `{"env":{"CLAUDE_CODE_USE_BEDROCK":"1"}}`)
	untouched("a folder set to Bedrock", turn(cwd, nil))
	managed := filepath.Join(t.TempDir(), "managed-settings.json")
	write(managed, `{"env":{"CLAUDE_CODE_USE_VERTEX":"1"}}`)
	modelname.ManagedSettings = func() []string { return []string{managed} }
	untouched("an administrator's Vertex setting", turn("", nil))
	modelname.ManagedSettings = func() []string { return nil }
	write(filepath.Join(home, "settings.json"), `{"env":{"ANTHROPIC_BASE_URL":"http://gateway"}}`)
	untouched("a user set to a gateway", turn("", nil))
}

// Every agent turn goes through Exec, so this is where the rule holds for all
// of them.
func TestExecNeverStartsASupersededModel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake monomind is a shell script")
	}
	catalogAt(t, "")
	firstPartyClaude(t)
	dir := t.TempDir()
	seen := filepath.Join(dir, "seen")
	bin := filepath.Join(dir, "monomind")
	script := "#!/bin/sh\n" +
		`echo "$* haiku=$ANTHROPIC_DEFAULT_HAIKU_MODEL" > "` + seen + `"` + "\n" +
		`echo '{"v":1,"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","text":"ok"}'` + "\n" +
		`echo '{"v":1,"type":"done","exit_code":0}'` + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Exec(context.Background(), ExecOptions{Bin: bin, Runtime: "claude", Model: "claude-opus-4-8", Prompt: "hi"}, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--model claude-opus-5-5", "haiku=claude-haiku-5-5"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("monomind got %q, want %q", got, want)
		}
	}
}
