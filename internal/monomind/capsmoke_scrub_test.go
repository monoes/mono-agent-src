package monomind

import (
	"regexp"
	"strings"
)

// Helpers of the opt-in cap smoke harness (capsmoke_realrunner_test.go, build tag realrunner). They live in a file without the
// tag so that their own tests (capsmoke_scrub_unit_test.go) run in every build: they spend nothing and start nothing.

// smokeProviderKeyVars are the variables that would let the runtime bill a provider key, a token or a cloud role instead of
// its own login. The harness removes every one that is set from its own environment before the runner starts (the child
// inherits the environment), and prints the NAMES it removed, never a value.
var smokeProviderKeyVars = []string{
	"OPENROUTER_API_KEY", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "OPENAI_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY",
	"MISTRAL_API_KEY", "GROQ_API_KEY", "XAI_API_KEY", "AZURE_OPENAI_API_KEY",
	"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_BEARER_TOKEN_BEDROCK",
	"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX",
}

// scrubProviderEnv unsets every name of smokeProviderKeyVars that lookup says is set (even set to empty) and returns the names
// it removed and their old values, so that the caller can restore them. The values are for restoring only; callers never print them.
func scrubProviderEnv(lookup func(string) (string, bool), unset func(string) error) (removed []string, old map[string]string) {
	old = map[string]string{}
	for _, k := range smokeProviderKeyVars {
		if v, ok := lookup(k); ok {
			if unset(k) == nil {
				removed = append(removed, k)
				old[k] = v
			}
		}
	}
	return removed, old
}

var secretShapes = []*regexp.Regexp{
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=-]{8,}`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{8,}`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}`), // a JWT
	regexp.MustCompile(`\bAKIA[0-9A-Z]{12,}`),
	regexp.MustCompile(`\b[0-9a-fA-F]{32,}\b`),                                    // a long hex run
	regexp.MustCompile(`[A-Za-z0-9+/_-]{40,}={0,2}`),                              // a long base64-like run
	regexp.MustCompile(`(?i)(api[_-]?key|token|secret|password)\s*[=:]\s*\S{6,}`), // key=value
}

// redactSecrets replaces anything in s that has the shape of a key or token with [REDACTED]. The harness passes everything the
// runner said (error strings, stop reasons) through it before logging.
func redactSecrets(s string) string {
	for _, re := range secretShapes {
		s = re.ReplaceAllString(s, "[REDACTED]")
	}
	return strings.TrimSpace(s)
}
