package monomind

import (
	"slices"
	"strings"
	"testing"
)

func TestTheSmokeScrubberRemovesEveryProviderKeyAndReportsNamesOnly(t *testing.T) {
	env := map[string]string{"HOME": "/h", "PATH": "/bin", "ZZZ_OTHER": "keep"}
	for _, k := range smokeProviderKeyVars {
		env[k] = "value-of-" + k // a value that must never show up in the removed list
	}
	env["ANTHROPIC_API_KEY"] = "" // set but empty is removed too
	removed, old := scrubProviderEnv(
		func(k string) (string, bool) { v, ok := env[k]; return v, ok },
		func(k string) error { delete(env, k); return nil })
	for _, k := range []string{"OPENROUTER_API_KEY", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "OPENAI_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY",
		"MISTRAL_API_KEY", "GROQ_API_KEY", "XAI_API_KEY", "AZURE_OPENAI_API_KEY", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
		"AWS_BEARER_TOKEN_BEDROCK", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX"} {
		if !slices.Contains(removed, k) {
			t.Errorf("%s was not removed", k)
		}
		if _, still := env[k]; still {
			t.Errorf("%s is still in the environment", k)
		}
	}
	if len(env) != 3 || env["HOME"] != "/h" || env["ZZZ_OTHER"] != "keep" {
		t.Errorf("the scrubber touched something else: %v", env)
	}
	if strings.Contains(strings.Join(removed, " "), "value-of") {
		t.Error("a value is in the removed list")
	}
	if old["OPENAI_API_KEY"] != "value-of-OPENAI_API_KEY" {
		t.Error("the old value is not kept for restoring")
	}
	// nothing set: nothing removed
	none, _ := scrubProviderEnv(func(string) (string, bool) { return "", false }, func(string) error { t.Fatal("unset called"); return nil })
	if len(none) != 0 {
		t.Errorf("removed %v from an empty environment", none)
	}
}

func TestTheSmokeOutputFilterRedactsKeyAndTokenShapes(t *testing.T) {
	for name, in := range map[string]string{
		"an sk key":         "auth failed for sk-ant-api03-AbCdEfGhIjKlMnOp",
		"a bearer header":   "Authorization: Bearer abcdef1234567890xyz",
		"a jwt":             "token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dBjftJeZ4CVPmB92K27uhbUJU1p1r_wW1gFWFOEjXk",
		"an aws key id":     "id AKIAIOSFODNN7EXAMPLE used",
		"a long hex run":    "digest 0123456789abcdef0123456789abcdef0123",
		"a long base64 run": "blob QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVowMTIzNDU2Nzg5YWJjZGVm end",
		"a key=value pair":  "OPENAI_API_KEY=hunter2hunter2",
		"a token: value":    "token: s3cr3tvalue99",
	} {
		out := redactSecrets(in)
		if !strings.Contains(out, "[REDACTED]") {
			t.Errorf("%s: %q was not redacted: %q", name, in, out)
		}
	}
	plain := "Claude Code returned an error result: Not logged in · Please run /login"
	if got := redactSecrets(plain); got != plain {
		t.Errorf("plain text was changed: %q", got)
	}
}
