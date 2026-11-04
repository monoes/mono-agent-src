package monomind

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// RuntimeModel is one selectable model for an agent runtime — an id to pass
// as --model plus a human-readable label for the picker.
type RuntimeModel struct {
	ID           string   `json:"id"`
	Label        string   `json:"label"`
	Description  string   `json:"description,omitempty"`
	EffortLevels []string `json:"effort_levels,omitempty"`
	// AliasOf is set when `agent models` lists this id as another name for
	// an earlier entry's model (protocol rev 28, capability
	// agent-models-alias-of): the canonical id.
	AliasOf string `json:"alias_of,omitempty"`
	// ResolvedID is the concrete model an alias stands for today, as `agent
	// models` reports it (claude "fable" → "claude-fable-5-1").
	ResolvedID string `json:"resolved_id,omitempty"`
}

// claudeModels is curated by hand: unlike antigravity and codex (see
// listAntigravityModels/listCodexModels), Claude Code has no CLI-level
// model-listing command — verified directly: `claude models` isn't a
// recognized subcommand, and since `claude [prompt]` treats any unrecognized
// first argument as a prompt to run, it silently launches a real chat turn
// asking the model to describe itself instead of failing, which is exactly
// the surprising/costly behavior this static list avoids triggering. The
// Agent SDK's supportedModels() is the real list (what Claude Code's /model
// picker shows); monomind exposes it as `agent models` (capability
// agent-models, see listAgentModels), and this copy of it as of 2026-10-19
// is the fallback for a monomind without that capability and the source of
// models the live list is too old to know (see withBuiltinClaude).
var claudeModels = []RuntimeModel{
	{ID: "claude-opus-5-5", Label: "Opus 5.5", EffortLevels: []string{"low", "medium", "high", "xhigh", "max"}},
	{ID: "claude-fable-5-1", Label: "Fable 5.1", EffortLevels: []string{"low", "medium", "high", "xhigh", "max"}},
	{ID: "claude-sonnet-5-5", Label: "Sonnet 5.5", EffortLevels: []string{"low", "medium", "high", "xhigh", "max"}},
	{ID: "claude-haiku-5-5", Label: "Haiku 5.5"},
	{ID: "claude-sonnet-5", Label: "Sonnet 5", EffortLevels: []string{"low", "medium", "high", "xhigh", "max"}},
	{ID: "claude-haiku-4-5-20251001", Label: "Haiku 4.5"},
	{ID: "claude-opus-5", Label: "Opus 5", EffortLevels: []string{"low", "medium", "high", "xhigh", "max"}},
	{ID: "claude-fable-5", Label: "Fable 5", EffortLevels: []string{"low", "medium", "high", "xhigh", "max"}},
	{ID: "claude-opus-4-8", Label: "Opus 4.8", EffortLevels: []string{"low", "medium", "high", "xhigh", "max"}},
	{ID: "claude-opus-4-7", Label: "Opus 4.7", EffortLevels: []string{"low", "medium", "high", "xhigh", "max"}},
	{ID: "claude-opus-4-6", Label: "Opus 4.6", EffortLevels: []string{"low", "medium", "high", "max"}},
	{ID: "claude-sonnet-4-6", Label: "Sonnet 4.6", EffortLevels: []string{"low", "medium", "high", "max"}},
}

// freeOpenRouterModels are zero-cost OpenRouter models that take tools and
// reasoning (checked against openrouter.ai/api/v1/models and `pi
// --list-models free` on 2026-09-29): each needs only a free OpenRouter key
// (OPENROUTER_API_KEY). Free models are rate-limited.
var freeOpenRouterModels = []struct{ id, label string }{
	{"qwen/qwen3.8-27b:free", "Qwen3.8 27B"},
	{"nvidia/nemotron-3-super-120b-a12b:free", "Nemotron 3 Super"},
	{"google/gemma-4-31b-it:free", "Gemma 4 31B"},
	{"poolside/laguna-s-2.1:free", "Laguna S 2.1"},
	{"cohere/north-mini-code:free", "North Mini Code"},
}

// openRouterFree lists freeOpenRouterModels for one runtime: prefix is how
// that runtime names an OpenRouter model ("openrouter/" for aider's litellm
// names and pi's provider/id, "" for cline, whose provider is its own
// setting), note says how to point the runtime at OpenRouter.
func openRouterFree(prefix, note string, efforts []string) []RuntimeModel {
	out := make([]RuntimeModel, 0, len(freeOpenRouterModels))
	for _, m := range freeOpenRouterModels {
		out = append(out, RuntimeModel{
			ID:           prefix + m.id,
			Label:        m.label + " (free, OpenRouter)",
			Description:  note,
			EffortLevels: efforts,
		})
	}
	return out
}

// curatedModels are the fallback lists for runtimes with no model-listing
// command (monomind's agent models reports supported:false for them), so
// their pickers are never empty. Effort levels are monomind's --effort
// names each runtime maps: cline --thinking has none|low|medium|high|xhigh,
// aider's reasoning_effort low|medium|high, pi's --thinking all six. dsh
// mirrors monomind's DSH_MODELS (dsh-runner-models.ts) without the two
// OpenRouter ids OpenRouter no longer lists (glm-5.2, minimax-m3), levels
// limited to monomind's names, plus the free OpenRouter set on dsh's pi-ai
// "openrouter/" route (Laguna S 2.1 comes from there).
var curatedModels = map[string][]RuntimeModel{
	"cline": openRouterFree("", "Needs cline's OpenRouter provider (cline auth openrouter, or CLINE_PROVIDER=openrouter with OPENROUTER_API_KEY)",
		[]string{"off", "low", "medium", "high", "xhigh"}),
	"aider": openRouterFree("openrouter/", "Needs OPENROUTER_API_KEY",
		[]string{"low", "medium", "high"}),
	"pi": openRouterFree("openrouter/", "Needs OPENROUTER_API_KEY (or pi's own openrouter login)",
		[]string{"off", "low", "medium", "high", "xhigh", "max"}),
	"dsh": append([]RuntimeModel{
		{ID: "deepseek-flash", Label: "DeepSeek V4.1 Flash", Description: "Needs DEEPSEEK_API_KEY", EffortLevels: []string{"off", "low", "high", "max"}},
		{ID: "deepseek-v4-pro", Label: "DeepSeek V4 Pro", Description: "Needs DEEPSEEK_API_KEY", EffortLevels: []string{"off", "low", "high", "max"}},
		{ID: "nvidia/deepseek-ai/deepseek-v4-flash-0731", Label: "DeepSeek V4 Flash (free, NVIDIA)", Description: "Needs a free NVIDIA_API_KEY", EffortLevels: []string{"off", "high", "max"}},
		{ID: "nvidia/deepseek-ai/deepseek-v4-pro-0813", Label: "DeepSeek V4 Pro (free, NVIDIA)", Description: "Needs a free NVIDIA_API_KEY", EffortLevels: []string{"off", "high", "max"}},
		{ID: "nvidia/moonshotai/kimi-k3", Label: "Kimi K3 (free, NVIDIA)", Description: "Needs a free NVIDIA_API_KEY", EffortLevels: []string{"off", "low", "medium", "high"}},
		{ID: "openrouter/nvidia/nemotron-3-ultra-550b-a55b:free", Label: "Nemotron 3 Ultra (free, OpenRouter)", Description: "Needs OPENROUTER_API_KEY", EffortLevels: []string{"off", "medium", "high"}},
		{ID: "openrouter/openrouter/free", Label: "OpenRouter free-models router", Description: "Needs OPENROUTER_API_KEY", EffortLevels: []string{"off", "low", "medium", "high"}},
	}, openRouterFree("openrouter/", "Needs OPENROUTER_API_KEY", []string{"off", "low", "medium", "high"})...),
}

// ListModels returns the models selectable for runtimeID's --model flag, for
// a picker. For claude that is the live list plus the built-in models it lacks
// (withBuiltinClaude); ListModelsStrict is the runtime's list as it is.
// antigravity and codex both have a real discovery command (see
// listAntigravityModels/listCodexModels); claude falls back to the curated
// list above. binary is the runtime's own resolved path (from a ScanEntry)
// — required for antigravity/codex, ignored otherwise. An unknown runtimeID
// returns a nil, nil slice so callers can fall back to a plain free-text
// model field.
func ListModels(ctx context.Context, runtimeID, binary string) ([]RuntimeModel, error) {
	models, err := ListModelsStrict(ctx, runtimeID, binary)
	if errors.Is(err, ErrBuiltinModels) {
		err = nil
	}
	if err == nil {
		models = withBuiltinClaude(runtimeID, models)
	}
	return models, err
}

// ErrBuiltinModels accompanies the models ListModelsStrict returns when the
// runtime's own listing failed and a built-in list stands in for it. The models
// are usable, but they are not what the runtime said (claude's curated ids are
// not the aliases it lists), so a caller that serves them to clients should
// look again soon rather than keep them.
var ErrBuiltinModels = errors.New("the runtime's model listing failed: these are the built-in models")

// ListModelsStrict is ListModels, except that it returns ErrBuiltinModels along
// with the list when a built-in list stands in for a listing that failed. A
// runtime with no listing command, or a monomind without agent models, is not
// a failure: its built-in list is the answer.
func ListModelsStrict(ctx context.Context, runtimeID, binary string) ([]RuntimeModel, error) {
	if set, err := Capabilities(ctx); err == nil && set.Has(CapAgentModels) {
		models, supported, err := listAgentModels(ctx, runtimeID)
		switch {
		case err == nil && (!supported || len(models) == 0):
			// No listing command (cline, aider, dsh, pi have none in
			// monomind's agent models): the curated list, if any.
			return builtinModels(ctx, runtimeID, binary)
		case err == nil:
			return models, nil
		}
		// A failed listing (runtime not logged in, timed out) falls back to
		// the built-in sources rather than leaving the picker empty.
		fallback, berr := builtinModels(ctx, runtimeID, binary)
		if berr == nil && len(fallback) > 0 && hasStaticList(runtimeID) {
			return fallback, ErrBuiltinModels
		}
		return fallback, berr
	}
	return builtinModels(ctx, runtimeID, binary)
}

// withBuiltinClaude adds the built-in claude models that the live list lacks.
// `agent models` asks the Claude Code that monomind selected, which can be a
// release or two behind (monomind skips an installed claude it does not
// trust and runs the SDK's bundled copy), so a model released since then —
// Haiku 5.5 on Claude Code 2.1.289 — would be missing from the picker. A
// built-in model counts as listed when its id is a live id or the concrete
// model a live alias resolves to. Other runtimes' lists are returned as is.
func withBuiltinClaude(runtimeID string, live []RuntimeModel) []RuntimeModel {
	if runtimeID != "claude" {
		return live
	}
	listed := make(map[string]bool, 2*len(live))
	for _, m := range live {
		listed[m.ID] = true
		if m.ResolvedID != "" {
			listed[m.ResolvedID] = true
		}
	}
	out := slices.Clone(live)
	for _, m := range claudeModels {
		if !listed[m.ID] {
			out = append(out, m)
		}
	}
	return out
}

// hasStaticList reports whether runtimeID's built-in models are a curated list
// written into this package, as opposed to the runtime's own listing command
// (antigravity, codex), which is as live as monomind's.
func hasStaticList(runtimeID string) bool {
	if runtimeID == "claude" {
		return true
	}
	_, ok := curatedModels[runtimeID]
	return ok
}

// builtinModels is ListModels without monomind's agent models: the
// runtimes' own listing commands, and the curated claude list.
func builtinModels(ctx context.Context, runtimeID, binary string) ([]RuntimeModel, error) {
	switch runtimeID {
	case "antigravity":
		if binary == "" {
			return nil, fmt.Errorf("listing antigravity models: no resolved binary path (is it installed?)")
		}
		return listAntigravityModels(ctx, binary)
	case "codex":
		if binary == "" {
			return nil, fmt.Errorf("listing codex models: no resolved binary path (is it installed?)")
		}
		return listCodexModels(ctx, binary)
	case "claude":
		return claudeModels, nil
	default:
		if m, ok := curatedModels[runtimeID]; ok {
			return m, nil
		}
		return nil, nil
	}
}

// listAntigravityModels runs `agy models`, which prints one
// "<id>\t<label>" pair per line (verified directly against a real
// installation — no --json/--output-format flag exists for this
// subcommand, so plain-text parsing is the only option).
func listAntigravityModels(ctx context.Context, binary string) ([]RuntimeModel, error) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, binary, "models")
	cmd.Env = ScrubDestinations(os.Environ()) // the agent's own login: not for a caller to redirect
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("agy models: %w", err)
	}

	var models []RuntimeModel
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		id, label, ok := strings.Cut(line, "\t")
		if !ok {
			// Not a "<id>\t<label>" data row (e.g. the leading "Fetching
			// available models..." status line) — skip rather than error,
			// so a harmless banner line doesn't sink the whole result.
			continue
		}
		id, label = strings.TrimSpace(id), strings.TrimSpace(label)
		if id == "" {
			continue
		}
		if label == "" {
			label = id
		}
		models = append(models, RuntimeModel{ID: id, Label: label})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("agy models: reading output: %w", err)
	}
	return models, nil
}

// codexModelCatalog mirrors the fields of `codex debug models`' JSON output
// that matter here: slug, display name, visibility, and supported reasoning levels.
type codexModelCatalog struct {
	Models []struct {
		Slug                     string `json:"slug"`
		DisplayName              string `json:"display_name"`
		Visibility               string `json:"visibility"`
		SupportedReasoningLevels []struct {
			Effort string `json:"effort"`
		} `json:"supported_reasoning_levels"`
	} `json:"models"`
}

// listCodexModels runs `codex debug models` ("Render the raw model catalog
// as JSON" — verified directly against a real installation) and keeps only
// entries marked visibility:"list", the same filter Codex's own UI uses to
// hide internal/reserved slugs (e.g. "gpt-reserve", "codex-auto-review")
// from a picker.
func listCodexModels(ctx context.Context, binary string) ([]RuntimeModel, error) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, binary, "debug", "models")
	cmd.Env = ScrubDestinations(os.Environ()) // the agent's own login: not for a caller to redirect
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("codex debug models: %w", err)
	}

	var catalog codexModelCatalog
	if err := json.Unmarshal(out, &catalog); err != nil {
		return nil, fmt.Errorf("codex debug models: unparseable output: %w", err)
	}

	models := make([]RuntimeModel, 0, len(catalog.Models))
	for _, m := range catalog.Models {
		if m.Visibility != "list" || m.Slug == "" {
			continue
		}
		label := m.DisplayName
		if label == "" {
			label = m.Slug
		}
		var efforts []string
		for _, l := range m.SupportedReasoningLevels {
			efforts = append(efforts, l.Effort)
		}
		models = append(models, RuntimeModel{ID: m.Slug, Label: label, EffortLevels: execEfforts(efforts)})
	}
	return models, nil
}

// agentExecEfforts are the names `agent exec --effort` accepts; it maps
// each to the runtime's own levels and rejects any other with a usage error.
var agentExecEfforts = map[string]bool{"off": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true}

// execEfforts keeps the levels agent exec accepts: a runtime's own name
// outside them (codex's "ultra", dsh's "minimal") would fail the turn.
// nil when none is left.
func execEfforts(levels []string) []string {
	var out []string
	for _, l := range levels {
		if agentExecEfforts[l] {
			out = append(out, l)
		}
	}
	return out
}

// CapAgentModels is `monomind agent models` (monomind#369).
const CapAgentModels = "agent-models"

// agentModelsResult is `monomind agent models --json` (protocol §12).
type agentModelsResult struct {
	Supported bool `json:"supported"`
	Models    []struct {
		ID           string   `json:"id"`
		ResolvedID   string   `json:"resolved_id"`
		Label        string   `json:"label"`
		Description  string   `json:"description"`
		Default      bool     `json:"default"`
		EffortLevels []string `json:"effort_levels"`
		AliasOf      string   `json:"alias_of"`
	} `json:"models"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// agentModelsTimeout bounds `agent models`; monomind's own limit is 30s.
var agentModelsTimeout = 45 * time.Second

// listAgentModels runs `monomind agent models --runtime <id> --json`, the
// runtime's own model list (for claude, what Claude Code's /model picker
// shows for this account). supported is false for a runtime with no
// listing command.
func listAgentModels(ctx context.Context, runtimeID string) (models []RuntimeModel, supported bool, err error) {
	bin, err := Find()
	if err != nil {
		return nil, false, err
	}
	cctx, cancel := context.WithTimeout(ctx, agentModelsTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, "agent", "models", "--runtime", runtimeID, "--json")
	cmd.Env = PinEnv(FilteredEnviron(), bin)
	out, runErr := cmd.Output()
	var res agentModelsResult
	if err := json.Unmarshal(lastJSONDocument(out), &res); err != nil {
		if runErr != nil {
			return nil, false, fmt.Errorf("monomind agent models: %w", runErr)
		}
		return nil, false, fmt.Errorf("monomind agent models: unparseable output: %w", err)
	}
	if res.Error != nil {
		return nil, false, fmt.Errorf("monomind agent models: %s: %s", res.Error.Code, res.Error.Message)
	}
	if runErr != nil {
		return nil, false, fmt.Errorf("monomind agent models: %w", runErr)
	}
	for _, m := range res.Models {
		if m.ID == "" {
			continue
		}
		models = append(models, RuntimeModel{
			ID:           m.ID,
			Label:        modelLabel(m.Label, m.Description, m.ID, m.Default),
			Description:  m.Description,
			EffortLevels: execEfforts(m.EffortLevels),
			AliasOf:      m.AliasOf,
			ResolvedID:   m.ResolvedID,
		})
	}
	return models, res.Supported, nil
}

// modelLabel names a listed model for a picker. Claude's aliases carry a
// generic label ("Opus", "Default (recommended)") and put the concrete
// model first in the description ("Opus 5.5 · Best for …"), so that name
// is used: "Opus 5.5", "Default (Opus 5.5)".
func modelLabel(label, description, id string, isDefault bool) string {
	concrete, _, found := strings.Cut(description, " · ")
	concrete = strings.TrimSpace(concrete)
	switch {
	case label == "":
		label = id
	case !found || concrete == "":
	case isDefault:
		label = "Default (" + concrete + ")"
	case strings.HasPrefix(concrete, label):
		label = concrete
	}
	return label
}

// lastJSONDocument returns out from its first '{' on, skipping any banner
// text a runtime printed before the JSON.
func lastJSONDocument(out []byte) []byte {
	if i := strings.IndexByte(string(out), '{'); i > 0 {
		return out[i:]
	}
	return out
}
