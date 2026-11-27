package monomind

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// fakeAgentModelsMonomind stands in for a monomind with (or without) the
// agent-models capability.
func fakeAgentModelsMonomind(t *testing.T, withCap bool) {
	t.Helper()
	caps := `"agent-exec","agent-scan","org-json-v1"`
	if withCap {
		caps += `,"agent-models"`
	}
	bin := filepath.Join(t.TempDir(), "monomind")
	script := "#!/bin/sh\n" +
		`if [ "$1" = "--version" ]; then echo '{"v":1,"version":"9.0.0","min_caller":"1.0.0","capabilities":[` + caps + `]}'; exit 0; fi` + "\n" +
		`if [ "$1 $2 $3 $4" = "agent models --runtime claude" ]; then cat <<'JSON'` + "\n" +
		`{"v":1,"runtime":"claude","supported":true,"models":[` +
		`{"id":"default","resolved_id":"claude-opus-5-5","label":"Default (recommended)","description":"Opus 5.5 · Best for everyday, complex tasks","default":true,"effort_levels":["low","medium","high","xhigh","max"]},` +
		`{"id":"opus","resolved_id":"claude-opus-5-5","label":"Opus","description":"Opus 5.5 · Most capable","effort_levels":["low","medium","high","xhigh","max"]},` +
		`{"id":"claude-sonnet-4-6","label":"Sonnet 4.6","description":"Efficient for routine tasks"}]}` + "\nJSON\nexit 0\nfi\n" +
		`if [ "$1 $2 $3 $4" = "agent models --runtime dsh" ]; then echo '{"v":1,"runtime":"dsh","supported":true,"models":[{"id":"kimi","label":"Kimi","effort_levels":["off","minimal","low","ultra"]},{"id":"odd","label":"Odd","effort_levels":["minimal"]}]}'; exit 0; fi` + "\n" +
		`if [ "$1 $2 $3 $4" = "agent models --runtime ali" ]; then echo '{"v":1,"runtime":"ali","supported":true,"models":[{"id":"default","label":"Default","aliases":["opus"]},{"id":"opus","label":"Opus","alias_of":"default"}]}'; exit 0; fi` + "\n" +
		`if [ "$1 $2 $3 $4" = "agent models --runtime zed" ]; then echo '{"v":1,"runtime":"zed","supported":false,"models":[]}'; exit 0; fi` + "\n" +
		`if [ "$1 $2 $3 $4" = "agent models --runtime pi" ]; then echo '{"v":1,"runtime":"pi","supported":false,"models":[]}'; exit 0; fi` + "\n" +
		`if [ "$1 $2 $3 $4" = "agent models --runtime codex" ]; then echo '{"v":1,"runtime":"codex","supported":true,"models":[],"error":{"code":"list-failed","message":"timed out"}}'; exit 1; fi` + "\n" +
		"exit 2\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvOverride, bin)
	ResetCapabilityCache()
	t.Cleanup(ResetCapabilityCache)
}

func TestListModelsUsesAgentModels(t *testing.T) {
	fakeAgentModelsMonomind(t, true)
	ctx := context.Background()

	got, err := ListModels(ctx, "claude", "")
	if err != nil {
		t.Fatal(err)
	}
	want := []RuntimeModel{
		{ID: "default", ResolvedID: "claude-opus-5-5", Label: "Default (Opus 5.5)", Description: "Opus 5.5 · Best for everyday, complex tasks", EffortLevels: []string{"low", "medium", "high", "xhigh", "max"}},
		{ID: "opus", ResolvedID: "claude-opus-5-5", Label: "Opus 5.5", Description: "Opus 5.5 · Most capable", EffortLevels: []string{"low", "medium", "high", "xhigh", "max"}},
	}
	if len(got) < len(want) || !reflect.DeepEqual(got[:len(want)], want) {
		t.Errorf("claude = %+v", got)
	}

	if got, err := ListModels(ctx, "zed", ""); err != nil || got != nil {
		t.Errorf("unsupported runtime = %v, %v; want nil, nil (free-text model)", got, err)
	}

	// A runtime with no listing command gets its curated list.
	if got, err := ListModels(ctx, "pi", ""); err != nil || !reflect.DeepEqual(got, curatedModels["pi"]) {
		t.Errorf("unsupported pi = %v, %v; want the curated list", got, err)
	}

	// A failed listing falls back to the built-in source (codex needs its
	// binary for that).
	if _, err := ListModels(ctx, "codex", ""); err == nil {
		t.Error("codex fallback without a binary should report the missing binary")
	}
}

// The live list comes from whichever Claude Code monomind selected, which can
// be older than a model that has been released since: the built-in models it
// lacks are added after it, and the ones it has (by id, or as the concrete
// model of an alias) are not listed twice.
func TestListModelsAddsBuiltinClaudeModelsTheLiveListLacks(t *testing.T) {
	fakeAgentModelsMonomind(t, true)
	got, err := ListModels(context.Background(), "claude", "")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, m := range got {
		seen[m.ID]++
	}
	for _, id := range []string{"claude-haiku-5-5", "claude-sonnet-5-5", "claude-fable-5-1"} {
		if seen[id] != 1 {
			t.Errorf("%s listed %d times, want once: %+v", id, seen[id], got)
		}
	}
	if seen["claude-opus-5-5"] != 0 {
		t.Errorf("claude-opus-5-5 is what the live aliases resolve to, yet it is listed again: %+v", got)
	}
	if seen["claude-sonnet-4-6"] != 0 {
		t.Errorf("the superseded claude-sonnet-4-6 is still listed: %+v", got)
	}
}

func TestListModelsFallsBackWithoutTheCapability(t *testing.T) {
	fakeAgentModelsMonomind(t, false)
	got, err := ListModels(context.Background(), "claude", "")
	if err != nil || !reflect.DeepEqual(got, claudeModels) {
		t.Errorf("old monomind: %v, %v; want the built-in claude list", got, err)
	}
}

// An effort name outside monomind's --effort set (dsh's "minimal", codex's
// "ultra") would fail the turn with a usage error, so it is never offered.
func TestListModelsDropsEffortNamesAgentExecRejects(t *testing.T) {
	fakeAgentModelsMonomind(t, true)
	got, err := ListModels(context.Background(), "dsh", "")
	if err != nil {
		t.Fatal(err)
	}
	want := []RuntimeModel{
		{ID: "kimi", Label: "Kimi", EffortLevels: []string{"off", "low"}},
		{ID: "odd", Label: "Odd"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("dsh = %+v, want %+v", got, want)
	}
}

// monomind 2.21 (rev 28, agent-models-alias-of) keeps a duplicate alias in
// the list but marks it; the mark must survive for the validate loop.
func TestListModelsKeepsAliasOf(t *testing.T) {
	fakeAgentModelsMonomind(t, true)
	got, err := ListModels(context.Background(), "ali", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].AliasOf != "" || got[1].ID != "opus" || got[1].AliasOf != "default" {
		t.Fatalf("models = %+v", got)
	}
}
