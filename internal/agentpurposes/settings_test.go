package agentpurposes

import (
	"strings"
	"testing"
)

func TestParsePair(t *testing.T) {
	for in, want := range map[string]Pair{
		"claude/opus": {"claude", "opus"}, "claude": {"claude", ""}, "claude/": {"claude", ""},
		"claude/default": {"claude", ""}, " codex / gpt-5 ": {"codex", "gpt-5"}, "openrouter/vendor/model": {"openrouter", "vendor/model"},
	} {
		got, err := ParsePair(in)
		if err != nil || got != want {
			t.Errorf("ParsePair(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "/opus", " ", "cl aude/opus", "claude/op\nus", "claude/../x"} {
		if _, err := ParsePair(in); err == nil {
			t.Errorf("ParsePair(%q) accepted", in)
		}
	}
	if k := (Pair{"claude", ""}).Key(); k != "claude/default" {
		t.Errorf("Key of the default model = %q", k)
	}
	if k := (Pair{"claude", "opus"}).Key(); k != "claude/opus" {
		t.Errorf("Key = %q", k)
	}
}

func TestValidateSettings(t *testing.T) {
	ok := Settings{Pairs: []Pair{{"claude", "opus"}, {"codex", "gpt-5"}}, Rule: RuleOrder, Effort: "high", BudgetSharePct: 40}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid settings refused: %v", err)
	}
	if err := (Settings{}).Validate(); err != nil {
		t.Fatalf("empty settings refused: %v", err)
	}
	for name, s := range map[string]Settings{
		"unknown rule":      {Rule: "random"},
		"negative share":    {BudgetSharePct: -1},
		"share over 100":    {BudgetSharePct: 101},
		"bad effort":        {Effort: "very high!"},
		"duplicate pair":    {Pairs: []Pair{{"claude", "opus"}, {"claude", "opus"}}},
		"empty runtime":     {Pairs: []Pair{{"", "opus"}}},
		"too many pairs":    {Pairs: manyPairs(MaxPairs + 1)},
		"duplicate default": {Pairs: []Pair{{"claude", ""}, {"claude", "default"}}},
	} {
		if err := s.Validate(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if (Settings{}).EffectiveRule() != RuleAny || (Settings{Rule: RuleCheapest}).EffectiveRule() != RuleCheapest {
		t.Error("EffectiveRule")
	}
}

func manyPairs(n int) []Pair {
	var out []Pair
	for i := 0; i < n; i++ {
		out = append(out, Pair{"r", strings.Repeat("m", 1) + string(rune('a'+i%26)) + string(rune('a'+i/26))})
	}
	return out
}

func TestSettingsContains(t *testing.T) {
	s := Settings{Pairs: []Pair{{"claude", "opus"}, {"codex", ""}}}
	if !s.Contains("claude", "opus") || !s.Contains("codex", "") || !s.Contains("codex", "default") || s.Contains("claude", "haiku") || s.Contains("gemini", "") {
		t.Error("Contains")
	}
	if s.Index("codex", "") != 1 || s.Index("gemini", "") != -1 {
		t.Error("Index")
	}
	if (Settings{}).Restricts() || !s.Restricts() {
		t.Error("Restricts: a purpose with nothing chosen uses every validated pair")
	}
}
