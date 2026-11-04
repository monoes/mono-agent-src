package monomind

import (
	"strings"
	"sync"
)

// The monomind runner holds the provider login. A caller that controls the variables which say where
// the runner's provider traffic goes (a base URL, a proxy, a CA bundle) can send that traffic, and the
// provider key it carries, to a host it listens on. So a process that is not the operator's own
// terminal (an agent, a coder turn, a chat or workflow run) gets none of them; the operator keeps
// them, since a corporate gateway or proxy is set that way. Provider KEY variables are not
// destinations and still reach the runner, which needs them.

// operatorCheck says whether this process is the operator's own. nil (a process that registered none,
// such as the desktop app) falls back to the agent-context variables in its own environment.
var (
	operatorCheckMu sync.RWMutex
	operatorCheck   func() bool
)

// SetOperatorCheck installs the answer to "is this process the operator's own terminal?" that decides
// whether destination variables reach the runner; nil removes it. internal/monomind cannot import
// internal/operator (the other way round is already an import), so cmd/monoagentcli registers it.
func SetOperatorCheck(f func() bool) {
	operatorCheckMu.Lock()
	operatorCheck = f
	operatorCheckMu.Unlock()
}

func isOperator() bool {
	operatorCheckMu.RLock()
	f := operatorCheck
	operatorCheckMu.RUnlock()
	if f == nil {
		return AgentContextMarker() == ""
	}
	return f()
}

// destinationEnvVars is the table half of the rule: names that are destinations whatever the pattern
// says. A test holds that each of them is dropped.
var destinationEnvVars = []string{
	"ANTHROPIC_BASE_URL", "OPENAI_BASE_URL", "OPENAI_API_BASE", "OLLAMA_HOST",
	"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "FTP_PROXY",
	"SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS", "REQUESTS_CA_BUNDLE", "CURL_CA_BUNDLE",
	"AWS_ENDPOINT_URL", "GOOGLE_VERTEX_BASE_URL", "AZURE_OPENAI_ENDPOINT",
}

// providerWords are the name tokens that make a *_URL, *_ENDPOINT* or *_BASE* variable a provider
// destination. A new provider's variable matches the pattern half when its name carries one.
var providerWords = map[string]bool{
	"ANTHROPIC": true, "CLAUDE": true, "OPENAI": true, "GOOGLE": true, "GEMINI": true, "VERTEX": true,
	"AZURE": true, "AWS": true, "BEDROCK": true, "MISTRAL": true, "GROQ": true, "OLLAMA": true,
	"OPENROUTER": true, "COHERE": true, "XAI": true, "DEEPSEEK": true, "PERPLEXITY": true,
	"TOGETHER": true, "FIREWORKS": true, "HUGGINGFACE": true, "HF": true, "LITELLM": true, "GROK": true,
}

// IsDestinationEnvVar says whether the variable name decides where provider traffic goes.
func IsDestinationEnvVar(name string) bool {
	n := strings.ToUpper(name)
	for _, d := range destinationEnvVars {
		if n == d {
			return true
		}
	}
	switch {
	case strings.HasSuffix(n, "_BASE_URL"), strings.HasSuffix(n, "_API_BASE"), strings.HasSuffix(n, "_API_HOST"),
		strings.HasPrefix(n, "OLLAMA_"),
		n == "PROXY", strings.HasSuffix(n, "_PROXY"),
		strings.HasSuffix(n, "_CA_BUNDLE"), strings.HasPrefix(n, "SSL_CERT_"), n == "NODE_EXTRA_CA_CERTS":
		return true
	}
	provider := false
	for _, tok := range strings.Split(n, "_") {
		if providerWords[tok] {
			provider = true
			break
		}
	}
	return provider && (strings.HasSuffix(n, "_URL") || strings.Contains(n, "_ENDPOINT") || strings.Contains(n, "_BASE"))
}

// ScrubDestinations returns env without the destination variables unless this process is the
// operator's own, in which case it is env unchanged. The operator check runs only when a destination
// variable is present (it can read the database).
func ScrubDestinations(env []string) []string {
	var hit bool
	for _, kv := range env {
		if name, _, _ := strings.Cut(kv, "="); IsDestinationEnvVar(name) {
			hit = true
			break
		}
	}
	if !hit || isOperator() {
		return env
	}
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if name, _, _ := strings.Cut(kv, "="); !IsDestinationEnvVar(name) {
			out = append(out, kv)
		}
	}
	return out
}
