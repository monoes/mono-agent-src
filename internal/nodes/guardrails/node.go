// Package guardrails implements the ai.guardrails workflow node: check
// text against a set of policies (Pass / Fail outputs) or sanitize it
// (personal data, secret keys, URLs and custom patterns replaced with
// <TYPE> placeholders). It is the counterpart of n8n's Guardrails node and
// emits the same item shape ({guardrailsInput, checks}), so imported
// workflows' expressions keep working.
//
// Keywords, personal data, secret keys, URLs and custom regexes are
// deterministic and run locally. Jailbreak, NSFW, topical alignment and
// custom guardrails are judged by a local agent runtime through monomind
// (one turn per check per item, in the turn sandbox), which answers with
// {flagged, confidenceScore}; a check trips when flagged and the score is
// at or above its threshold.
package guardrails

import (
	"context"
	"fmt"
	"sync"

	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// NodeType is the registered type name.
const NodeType = "ai.guardrails"

// Output handles of the classify operation (n8n's outputs 0 and 1).
const (
	HandlePass = "pass"
	HandleFail = "fail"
)

// Node is the ai.guardrails executor (config: see GuardrailsNodeSchema).
type Node struct{}

// Type implements workflow.NodeExecutor.
func (n *Node) Type() string { return NodeType }

// PerItemConfigFields keeps the text template from being resolved against
// the first item only: Execute resolves it for each item.
func (n *Node) PerItemConfigFields() []string { return []string{"text"} }

// RegisterAll registers ai.guardrails.
func RegisterAll(r *workflow.NodeTypeRegistry) {
	r.Register(NodeType, func() workflow.NodeExecutor { return &Node{} })
}

// Execute implements workflow.NodeExecutor.
func (n *Node) Execute(ctx context.Context, input workflow.NodeInput, raw map[string]interface{}) ([]workflow.NodeOutput, error) {
	cfg, err := parseConfig(raw)
	if err != nil {
		return nil, err
	}
	var j *judge
	if len(cfg.llm) > 0 {
		if j, err = newJudge(ctx, cfg, input, vault.DBFromContext(ctx)); err != nil {
			return nil, err
		}
	}
	passed, failed := []workflow.Item{}, []workflow.Item{}
	engine := workflow.NewExpressionEngine()
	for i, item := range input.Items {
		text, err := engine.EvaluateString(cfg.text, workflow.ExpressionContext{
			JSON: item.JSON, Node: input.NodeOutputs, WorkflowID: input.WorkflowID,
			ExecutionID: input.ExecutionID, Location: input.Location,
		})
		if err != nil {
			return nil, fmt.Errorf("%s: item %d: text: %w", NodeType, i, err)
		}
		out, ok, err := cfg.process(ctx, j, text)
		if err != nil {
			return nil, fmt.Errorf("%w (item %d)", err, i)
		}
		if ok {
			passed = append(passed, out)
		} else {
			failed = append(failed, out)
		}
	}
	if cfg.operation == OpSanitize {
		return []workflow.NodeOutput{{Handle: "main", Items: passed}}, nil
	}
	return []workflow.NodeOutput{{Handle: HandlePass, Items: passed}, {Handle: HandleFail, Items: failed}}, nil
}

// result is one guardrail's outcome.
type result struct {
	name      string
	triggered bool
	llm       bool
	score     float64
	info      map[string]interface{}
	masks     map[string][]string
}

// toJSON is the check as n8n reports it (masks are internal).
func (r result) toJSON() map[string]interface{} {
	m := map[string]interface{}{"name": r.name, "triggered": r.triggered, "info": r.info}
	if r.llm {
		m["confidenceScore"] = r.score
		m["executionFailed"] = false
	}
	if m["info"] == nil {
		m["info"] = map[string]interface{}{}
	}
	return m
}

// process runs one item's text through the guardrails and returns its
// output item and whether it passed. Like n8n, the pattern checks run
// first; when one of them trips (classify) the keyword and model checks
// do not run, and the Fail item lists only the checks that tripped.
func (c *config) process(ctx context.Context, j *judge, text string) (workflow.Item, bool, error) {
	pre := c.preflight(text)
	if c.operation == OpSanitize {
		masks := map[string][]string{}
		for _, r := range pre {
			for k, v := range r.masks {
				masks[k] = append(masks[k], v...)
			}
		}
		return outItem(applyMasks(text, masks), pre), true, nil
	}
	if tripped := triggered(pre); len(tripped) > 0 {
		return outItem(text, tripped), false, nil
	}
	in, err := c.inputStage(ctx, j, text)
	if err != nil {
		return workflow.Item{}, false, err
	}
	if tripped := triggered(in); len(tripped) > 0 {
		return outItem(text, tripped), false, nil
	}
	return outItem(text, append(pre, in...)), true, nil
}

func triggered(rs []result) []result {
	var out []result
	for _, r := range rs {
		if r.triggered {
			out = append(out, r)
		}
	}
	return out
}

func outItem(text string, rs []result) workflow.Item {
	checks := make([]interface{}, len(rs))
	for i, r := range rs {
		checks[i] = r.toJSON()
	}
	return workflow.Item{JSON: map[string]interface{}{"guardrailsInput": text, "checks": checks}}
}

// preflight runs the pattern checks: personal data, custom regexes,
// secret keys and URLs.
func (c *config) preflight(text string) []result {
	var out []result
	if c.pii {
		out = append(out, findingsResult("personalData", detectPII(text, c.piiEntities)))
	}
	if len(c.regexes) > 0 {
		out = append(out, findingsResult("customRegex", detectRegexes(text, c.regexes)))
	}
	if c.secrets {
		found := detectSecrets(text, c.secretLevel)
		out = append(out, result{name: "secretKeys", triggered: len(found) > 0,
			info:  map[string]interface{}{"detectedSecrets": strs(found)},
			masks: map[string][]string{"SECRET": found}})
	}
	if c.urls {
		detected := detectURLs(text)
		var allowed, blocked, reasons []string
		for _, u := range detected {
			if why := c.urlRules.checkURL(u); why != "" {
				blocked = append(blocked, u)
				reasons = append(reasons, u+": "+why)
			} else {
				allowed = append(allowed, u)
			}
		}
		out = append(out, result{name: "urls", triggered: len(blocked) > 0,
			info: map[string]interface{}{"detected": strs(detected), "allowed": strs(allowed),
				"blocked": strs(blocked), "blockedReasons": strs(reasons)},
			masks: map[string][]string{"URL": blocked}})
	}
	return out
}

func findingsResult(name string, fs []finding) result {
	masks := map[string][]string{}
	analyzer := make([]interface{}, 0, len(fs))
	for _, f := range fs {
		masks[f.kind] = append(masks[f.kind], f.text)
		analyzer = append(analyzer, map[string]interface{}{"entityType": f.kind, "text": f.text})
	}
	return result{name: name, triggered: len(fs) > 0, masks: masks,
		info: map[string]interface{}{"analyzerResults": analyzer}}
}

// inputStage runs the keyword check and the model-judged checks (in
// parallel). A model check that cannot run fails the node, as in n8n.
func (c *config) inputStage(ctx context.Context, j *judge, text string) ([]result, error) {
	var out []result
	if len(c.keywords) > 0 {
		m := matchKeywords(text, c.keywords)
		out = append(out, result{name: "keywords", triggered: len(m) > 0,
			info: map[string]interface{}{"matchedKeywords": strs(m)}})
	}
	if len(c.llm) == 0 {
		return out, nil
	}
	results := make([]result, len(c.llm))
	errs := make([]error, len(c.llm))
	var wg sync.WaitGroup
	for i, chk := range c.llm {
		wg.Add(1)
		go func(i int, chk llmCheck) {
			defer wg.Done()
			v, err := j.run(ctx, chk, text)
			errs[i] = err
			results[i] = result{name: chk.name, llm: true, score: v.confidence,
				triggered: v.flagged && v.confidence >= chk.threshold, info: map[string]interface{}{}}
		}(i, chk)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return append(out, results...), nil
}

func strs(list []string) []interface{} {
	out := make([]interface{}, len(list))
	for i, s := range list {
		out[i] = s
	}
	return out
}
