// Package agent provides workflow nodes backed by locally-installed AI
// agent runtimes, delegated through monomind's Agent Exec Protocol. This is
// the local-agent replacement for the deprecated ai.* provider nodes
// (docs/plans/local-agent-monomind-delegation.md D5).
package agent

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/agentusage"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// templatePattern matches {{$json.FIELD}} placeholders in prompt templates —
// same syntax as the deprecated ai.* nodes so migrated prompts keep working.
var templatePattern = regexp.MustCompile(`\{\{\$json\.(\w+)\}\}`)

// AskNode sends each input item through one local AI agent turn.
//
// Config fields:
//
//	"runtime" (string, required): agent runtime id — claude, codex, kimicode, …
//	  (list with `monoagentcli agent scan --installed`), or "auto" to pick a
//	  validated one from the agent roster (pickAuto).
//	"prompt" (string, required): user prompt template. Supports {{$json.FIELD}} placeholders.
//	"model" (string): model override for the runtime.
//	"system_prompt" (string): optional system prompt.
//	"output_key" (string): key to store the agent's answer under (default "agent_response").
//	"timeout" (number): overall per-item timeout in seconds (default 300).
//	"output_schema" (object or JSON string): ask for JSON matching this JSON
//	  Schema; the parsed value is stored under output_key, re-asked once on a
//	  mismatch.
//	"merge_output" (bool): with output_schema and an object answer, also
//	  merge its fields into the item.
//	"concurrency" (number): items run in parallel, up to this many (default 1).
type AskNode struct{}

// Seams for tests.
var (
	ensureMonomind = monomind.Ensure
	execTurn       = monomind.Exec
)

// askConfig is a node execution's parsed config.
type askConfig struct {
	runtime, model, systemPrompt, promptTemplate, outputKey string
	timeout                                                 time.Duration
	schema                                                  map[string]interface{}
	merge, auto                                             bool
}

func (n *AskNode) Type() string { return "agent.ask" }

// PerItemConfigFields holds the prompts back from the engine's one-pass
// config resolution, which uses only the first item: each item's prompt is
// resolved against that item in ask.
func (n *AskNode) PerItemConfigFields() []string { return []string{"prompt", "system_prompt"} }

func (n *AskNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	runtime := configString(config, "runtime", "")
	if runtime == "" {
		return nil, fmt.Errorf("%w: agent.ask node requires \"runtime\" (see `monoagentcli agent scan --installed`, or use \"auto\")", workflow.ErrInvalidConfig)
	}
	promptTemplate := configString(config, "prompt", "")
	if promptTemplate == "" {
		return nil, fmt.Errorf("%w: agent.ask node requires \"prompt\"", workflow.ErrInvalidConfig)
	}
	timeoutSec := configInt(config, "timeout", 300)
	if timeoutSec <= 0 {
		timeoutSec = 300
	}
	schema, err := parseOutputSchema(config["output_schema"])
	if err != nil {
		return nil, fmt.Errorf("%w: agent.ask: %v", workflow.ErrInvalidConfig, err)
	}
	merge, _ := config["merge_output"].(bool)
	cfg := askConfig{
		runtime:        runtime,
		model:          configString(config, "model", ""),
		systemPrompt:   configString(config, "system_prompt", ""),
		promptTemplate: promptTemplate,
		outputKey:      configString(config, "output_key", "agent_response"),
		timeout:        time.Duration(timeoutSec) * time.Second,
		schema:         schema,
		merge:          merge,
	}
	concurrency := configInt(config, "concurrency", 1)
	if concurrency < 1 {
		concurrency = 1
	}

	// Handshake once per node execution — catches a missing or
	// protocol-incompatible monomind up front with an actionable error,
	// instead of every item silently returning an empty answer.
	// A failure because the agent is not set up is marked
	// (monomind.MarkNotSetup), so the run's stored error still says so.
	bin, _, err := ensureMonomind(ctx)
	if err != nil {
		return nil, fmt.Errorf("agent.ask: %w", monomind.MarkNotSetup(err))
	}

	db := vault.DBFromContext(ctx)
	if runtime == RuntimeAuto {
		choice, err := pickAuto(ctx, db, cfg.model)
		if err != nil {
			return nil, fmt.Errorf("agent.ask: %w", monomind.MarkNotSetup(err))
		}
		cfg.runtime, cfg.model, cfg.auto = choice.Runtime, choice.Model, true
	}

	t := &asker{bin: bin, cfg: cfg, input: input, db: db}
	items := make([]workflow.Item, len(input.Items))
	if concurrency == 1 || len(input.Items) <= 1 {
		for i, item := range input.Items {
			out, err := t.ask(ctx, item)
			if err != nil {
				return nil, err
			}
			items[i] = out
		}
	} else {
		if err := t.askAll(ctx, input.Items, items, concurrency); err != nil {
			return nil, err
		}
	}

	return []workflow.NodeOutput{
		{Handle: "main", Items: items},
	}, nil
}

// asker runs one node execution's turns.
type asker struct {
	bin   string
	cfg   askConfig
	input workflow.NodeInput
	db    *sql.DB
}

// askAll runs items up to n at a time into out (same order). The first
// item to fail stops the rest and is the error returned.
func (t *asker) askAll(ctx context.Context, items, out []workflow.Item, n int) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg       sync.WaitGroup
		once     sync.Once
		firstErr error
		sem      = make(chan struct{}, n)
	)
	for i, item := range items {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(i int, item workflow.Item) {
			defer wg.Done()
			defer func() { <-sem }()
			res, err := t.ask(ctx, item)
			if err != nil {
				once.Do(func() { firstErr = err; cancel() })
				return
			}
			out[i] = res
		}(i, item)
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}

// ask runs one item: a turn, and with output_schema one re-ask on a mismatch.
func (t *asker) ask(ctx context.Context, item workflow.Item) (workflow.Item, error) {
	cfg := t.cfg
	prompt, err := t.resolve(cfg.promptTemplate, item)
	if err != nil {
		return workflow.Item{}, fmt.Errorf("agent.ask: prompt: %w", err)
	}
	system, err := t.resolve(cfg.systemPrompt, item)
	if err != nil {
		return workflow.Item{}, fmt.Errorf("agent.ask: system_prompt: %w", err)
	}
	if cfg.schema != nil {
		prompt += schemaInstruction(cfg.schema)
	}
	res, err := t.turn(ctx, prompt, system, "")
	if err != nil {
		return workflow.Item{}, err
	}

	outJSON := copyItemJSON(item)
	if cfg.schema == nil {
		outJSON[cfg.outputKey] = strings.TrimSpace(res.ResultText)
	} else {
		value, verr := parseAgainst(cfg.schema, res.ResultText)
		if verr != nil {
			res, value, err = t.reask(ctx, prompt, system, res.SessionID, verr)
			if err != nil {
				return workflow.Item{}, err
			}
		}
		outJSON[cfg.outputKey] = value
		if obj, ok := value.(map[string]interface{}); ok && cfg.merge {
			for k, v := range obj {
				outJSON[k] = v
			}
		}
	}
	outJSON["_agent_session_id"] = res.SessionID
	if res.SandboxStatus != "" {
		outJSON["_agent_sandbox"] = res.SandboxStatus
	}
	if cfg.auto {
		outJSON["_agent_runtime"] = cfg.runtime
		if cfg.model != "" {
			outJSON["_agent_model"] = cfg.model
		}
	}
	return workflow.Item{JSON: outJSON}, nil
}

// reask asks once more after a reply that did not match the schema: in the
// same session when there is one, else (or when resuming fails) in a fresh
// turn that carries the original prompt.
func (t *asker) reask(ctx context.Context, prompt, system, sessionID string, verr error) (*monomind.TurnResult, interface{}, error) {
	var res *monomind.TurnResult
	if sessionID != "" {
		r, err := t.turn(ctx, fmt.Sprintf("Your reply did not match the required JSON Schema: %v.%s", verr, schemaInstruction(t.cfg.schema)), system, sessionID)
		if err == nil {
			res = r
		} else if ctx.Err() != nil {
			return nil, nil, err
		}
	}
	if res == nil {
		r, err := t.turn(ctx, prompt+fmt.Sprintf("\n\nA previous reply was rejected: %v.", verr), system, "")
		if err != nil {
			return nil, nil, err
		}
		res = r
	}
	value, err := parseAgainst(t.cfg.schema, res.ResultText)
	if err != nil {
		return nil, nil, fmt.Errorf("agent.ask (%s): the reply did not match output_schema after one retry: %w", t.cfg.runtime, err)
	}
	return res, value, nil
}

// parseAgainst extracts the JSON in reply and validates it.
func parseAgainst(schema map[string]interface{}, reply string) (interface{}, error) {
	v, err := extractJSON(reply)
	if err != nil {
		return nil, err
	}
	if err := validateSchema(schema, v); err != nil {
		return nil, err
	}
	return v, nil
}

// turn runs one agent turn and records its usage.
func (t *asker) turn(ctx context.Context, prompt, system, resume string) (*monomind.TurnResult, error) {
	cfg := t.cfg
	model := cfg.model
	// ResultText holds the reply even when the result event has no text:
	// monomind.ApplyEventToResult assembles it from the assistant events.
	res, err := execTurn(ctx, monomind.ExecOptions{
		Bin:          t.bin,
		Runtime:      cfg.runtime,
		Model:        cfg.model,
		Prompt:       prompt,
		SystemPrompt: system,
		Resume:       resume,
		Timeout:      cfg.timeout,
		// The item travels in the prompt; one shared empty folder
		// for every agent.ask turn, whichever workflow runs it.
		Sandbox:          monomind.TurnSandboxMode,
		WorkspacePurpose: monomind.WorkspaceAgentAsk,
	}, func(ev monomind.Event) {
		if ev.Type == monomind.EventStart && ev.Model != "" {
			model = ev.Model
		}
	})
	if err != nil {
		return nil, fmt.Errorf("agent.ask (%s): %w", cfg.runtime, monomind.MarkNotSetup(err))
	}
	t.record(ctx, model, res)
	if res.Err != nil {
		monomind.ReclassifyMissingRuntime(ctx, cfg.runtime, res)
		return nil, fmt.Errorf("agent.ask (%s) turn failed: %w", cfg.runtime, monomind.MarkNotSetup(res.Err))
	}
	return res, nil
}

// record stores a turn's usage; a failure to store it never fails the turn.
func (t *asker) record(ctx context.Context, model string, res *monomind.TurnResult) {
	if t.db == nil || res == nil {
		return
	}
	wfID, execID := t.input.WorkflowID, t.input.ExecutionID
	if wfID == "" && execID == "" {
		wfID, execID = vault.ExecIDsFromContext(ctx)
	}
	_ = agentusage.RecordTurn(context.WithoutCancel(ctx), t.db, agentusage.Turn{
		ProfileID:   vault.ProfileIDFromContext(ctx),
		Source:      agentusage.SourceAgentAsk,
		WorkflowID:  wfID,
		ExecutionID: execID,
		NodeID:      t.input.NodeID,
		Runtime:     t.cfg.runtime,
		Model:       model,
	}, res)
}

// RegisterAll registers the agent node types into the given registry.
func RegisterAll(r *workflow.NodeTypeRegistry) {
	r.Register("agent.ask", func() workflow.NodeExecutor { return &AskNode{} })
}

// expandTemplate replaces {{$json.KEY}} placeholders in template with values from item.JSON.
func expandTemplate(template string, item workflow.Item) string {
	return templatePattern.ReplaceAllStringFunc(template, func(match string) string {
		parts := templatePattern.FindStringSubmatch(match)
		if len(parts) < 2 {
			return match
		}
		key := parts[1]
		if item.JSON == nil {
			return match
		}
		val, ok := item.JSON[key]
		if !ok {
			return match
		}
		return fmt.Sprintf("%v", val)
	})
}

// configString extracts a non-empty string value from config.
func configString(config map[string]interface{}, key, defaultVal string) string {
	if v, ok := config[key]; ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return defaultVal
}

// configInt extracts an int value from config (JSON numbers arrive as float64).
func configInt(config map[string]interface{}, key string, defaultVal int) int {
	if v, ok := config[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		}
	}
	return defaultVal
}

// copyItemJSON creates a shallow copy of an item's JSON map.
func copyItemJSON(item workflow.Item) map[string]interface{} {
	m := make(map[string]interface{}, len(item.JSON)+2)
	for k, v := range item.JSON {
		m[k] = v
	}
	return m
}

// resolve evaluates a prompt template ({{ }} expressions, as in any other
// config field) against one item. The legacy {{$json.FIELD}} form of the
// deprecated ai.* nodes is expanded first, as before.
func (t *asker) resolve(tmpl string, item workflow.Item) (string, error) {
	tmpl = expandTemplate(tmpl, item)
	if !strings.Contains(tmpl, "{{") {
		return tmpl, nil
	}
	return workflow.NewExpressionEngine().EvaluateString(tmpl, workflow.ExpressionContext{
		JSON:        item.JSON,
		Node:        t.input.NodeOutputs,
		WorkflowID:  t.input.WorkflowID,
		ExecutionID: t.input.ExecutionID,
		Location:    t.input.Location,
	})
}
