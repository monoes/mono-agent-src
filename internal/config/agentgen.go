// internal/config/agentgen.go — LLM-backed config generation delegated to a
// locally-installed AI agent runtime via monomind (Agent Exec Protocol),
// replacing the former remote API generator (apiv1.monoes.me). Offline-first:
// when no monomind/runtime is installed, generation fails fast to cache-only
// — it never blocks on subprocess spin-up (plan D1 ⓥ2, §9.15).
package config

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/internal/monomind"
)

// RuntimeEnvVar pins the agent runtime used for generation/selector tasks.
const RuntimeEnvVar = "MONOAGENT_AI_RUNTIME"

// runtimePriority orders installed runtimes when no explicit choice is made.
var runtimePriority = []string{
	"claude", "codex", "kimicode", "qwen", "grok", "crush", "copilot",
	"pi", "antigravity", "opencode", "qwen-rpc", "pi-rpc", "kilo",
}

// maxHTMLChars caps the HTML sent in the prompt (DOM relevant to selectors
// is front-loaded; the prompt travels via --prompt-file, but the cap still
// bounds prompt size and cost).
const maxHTMLChars = 60_000

// agentGenBudgetUSD caps spend for one config-generation turn
// (monomind --budget-usd): a single HTML→JSON extraction task must never
// run up unbounded cost on the user's agent runtime.
const agentGenBudgetUSD = 2.0

// AgentGenerator generates field-extraction configs with a local agent.
type AgentGenerator struct {
	logger         zerolog.Logger
	ensureMonomind func(context.Context) (string, *monomind.VersionInfo, error)

	mu              sync.Mutex
	resolvedRuntime string // resolved once, cached
	resolvedBin     string // monomind binary path, resolved+handshaken alongside resolvedRuntime
}

// NewAgentGenerator creates a generator using the given logger.
func NewAgentGenerator(logger zerolog.Logger) *AgentGenerator {
	return &AgentGenerator{logger: logger, ensureMonomind: monomind.Ensure}
}

// resolve determines the monomind binary and agent runtime: handshake first
// (catches a missing or protocol-incompatible monomind with an actionable
// error, same as the CLI's Ensure preamble), then explicit env pin, else the
// first installed runtime in priority order. Cached after the first call.
func (g *AgentGenerator) resolve(ctx context.Context) (bin, runtime string, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.resolvedRuntime != "" && g.resolvedBin != "" {
		return g.resolvedBin, g.resolvedRuntime, nil
	}
	handshakeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resolvedBin, _, err := g.ensureMonomind(handshakeCtx)
	if err != nil {
		return "", "", fmt.Errorf("cache-only mode: %w", err)
	}
	if env := strings.TrimSpace(os.Getenv(RuntimeEnvVar)); env != "" {
		g.resolvedBin = resolvedBin
		g.resolvedRuntime = env
		return g.resolvedBin, g.resolvedRuntime, nil
	}
	scanCtx, cancel2 := context.WithTimeout(ctx, 30*time.Second)
	defer cancel2()
	scan, err := monomind.Scan(scanCtx)
	if err != nil {
		return "", "", fmt.Errorf("cache-only mode: agent scan failed: %w", err)
	}
	for _, want := range runtimePriority {
		// kilo runs full-access only (or not at all); a scoped
		// generation turn would be refused with `unsupported`.
		if e := scan.Find(want); e != nil && e.Installed && runsScoped(e) {
			g.resolvedBin = resolvedBin
			g.resolvedRuntime = want
			return g.resolvedBin, g.resolvedRuntime, nil
		}
	}
	return "", "", fmt.Errorf("cache-only mode: %w", monomind.ErrNoRuntime)
}

// runsScoped reports whether a plain (scoped-access) turn can run on the
// runtime. An older monomind that lists no access modes is assumed to.
func runsScoped(e *monomind.ScanEntry) bool {
	if _, no := e.UnsupportedReason(); no {
		return false
	}
	return len(e.AccessModes) == 0 || slices.Contains(e.AccessModes, "scoped")
}

const agentSystemPrompt = `You generate field-extraction config JSON for Mono Agent browser automation.
Given a page's HTML, a purpose, and a field schema, respond with ONLY a JSON object —
no prose, no markdown fences — shaped as:
{"config_name": "<name>", "fields": {"<field>": {"xpath": "<xpath-or-css>", "type": "text|array", "data": [<selector parts> or null]}}}
XPath may contain {FIELD} placeholders. Keep selectors minimal and robust.`

// GenerateConfig asks the local agent to produce a config map for the given
// HTML. Returns the raw parsed JSON map (same contract the remote
// APIClient.GenerateConfig had, so parseAPIConfig applies unchanged).
func (g *AgentGenerator) GenerateConfig(
	ctx context.Context,
	baseName, htmlContent, purpose string,
	schema map[string]interface{},
) (map[string]interface{}, error) {
	bin, runtime, err := g.resolve(ctx)
	if err != nil {
		return nil, err
	}
	if htmlContent == "" {
		return nil, fmt.Errorf("no HTML content for config generation of %q", baseName)
	}
	if len(schema) == 0 {
		schema = map[string]interface{}{}
	}
	schemaJSON, _ := json.Marshal(schema)
	if len(htmlContent) > maxHTMLChars {
		htmlContent = htmlContent[:maxHTMLChars]
	}
	prompt := fmt.Sprintf(
		"Generate an extraction config.\nconfig_name base: %s\npurpose: %s\nfield schema: %s\n\nHTML:\n%s",
		baseName, purpose, string(schemaJSON), htmlContent,
	)

	execCtx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()

	// Sandbox: the agent runs in a fresh empty temp dir (no access to the
	// user's working directory) with no tools and a hard spend cap.
	sandbox, err := os.MkdirTemp("", "monoagent-agentgen-*")
	if err != nil {
		return nil, fmt.Errorf("create agent sandbox dir: %w", err)
	}
	defer os.RemoveAll(sandbox)

	var answer strings.Builder
	res, err := monomind.Exec(execCtx, monomind.ExecOptions{
		Bin:          bin,
		Runtime:      runtime,
		Prompt:       prompt,
		SystemPrompt: agentSystemPrompt,
		Cwd:          sandbox,
		Sandbox:      monomind.TurnSandboxMode,
		BudgetUSD:    agentGenBudgetUSD,
		Timeout:      170 * time.Second,
	}, func(ev monomind.Event) {
		if ev.Type == monomind.EventAssistant && ev.Text != "" {
			answer.WriteString(ev.Text)
		}
	})
	if err != nil {
		return nil, fmt.Errorf("agent exec: %w", err)
	}
	if res.Err != nil {
		return nil, fmt.Errorf("agent %s: %s", runtime, res.Err.Error())
	}
	text := strings.TrimSpace(res.ResultText)
	if text == "" {
		text = strings.TrimSpace(answer.String())
	}
	if text == "" {
		return nil, fmt.Errorf("agent %s returned an empty answer", runtime)
	}
	raw := stripJSONFences(text)
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, fmt.Errorf("agent %s answer is not a JSON object: %w", runtime, err)
	}
	return m, nil
}

// stripJSONFences removes a single wrapping ```json … ``` fence if present.
func stripJSONFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	}
	return strings.TrimSpace(s)
}
