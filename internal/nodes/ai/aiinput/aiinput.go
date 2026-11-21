// Package aiinput builds the untrusted per-item text a decision node (ai.choose,
// ai.decide) sends as its state: a {{$json.field}} template, a projection of
// item fields, or the whole item, capped in length. Item content reaches the
// provider verbatim and is never evaluated as a template.
package aiinput

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/monoes/mono-agent/internal/workflow"
)

// templatePattern matches {{$json.FIELD}} placeholders — same syntax as
// agent.ask's prompt templates.
var templatePattern = regexp.MustCompile(`\{\{\$json\.(\w+)\}\}`)

// ItemInput builds the untrusted_input value for one item: tmpl rendered
// when set, else the item projected to fields when set, else the whole item
// as JSON; always capped at maxChars runes.
func ItemInput(tmpl string, fields []string, maxChars int, item workflow.Item, in workflow.NodeInput) string {
	var s string
	switch {
	case tmpl != "":
		s = Render(tmpl, item, in)
	case len(fields) > 0:
		proj := make(map[string]any, len(fields))
		for _, f := range fields {
			if v, ok := item.JSON[f]; ok {
				proj[f] = v
			}
		}
		s = Marshal(proj)
	default:
		s = Marshal(item.JSON)
	}
	return capChars(s, maxChars)
}

// Marshal is v as JSON, or fmt's rendering when it cannot be encoded.
func Marshal(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func capChars(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// Render renders the configured input template for one item. Item content
// must reach the provider verbatim and never be evaluated as a template (an
// email body holding "{{ .node.X.json.token }}" would otherwise leak that
// value), so {{$json.field}} placeholders are first masked with random
// per-call tokens, the configured template alone goes through the expression
// engine, and only then are the tokens replaced — in a single pass that never
// rescans what it inserts — by the literal field values.
func Render(tmpl string, item workflow.Item, in workflow.NodeInput) string {
	nonce := make([]byte, 8)
	_, _ = rand.Read(nonce)
	prefix := "⟦choose-" + hex.EncodeToString(nonce) + "-"
	var pairs []string
	masked := templatePattern.ReplaceAllStringFunc(tmpl, func(match string) string {
		val, ok := placeholderValue(match, item)
		if !ok {
			return match
		}
		token := fmt.Sprintf("%s%d⟧", prefix, len(pairs)/2)
		pairs = append(pairs, token, val)
		return token
	})
	if strings.Contains(masked, "{{") {
		engine := workflow.NewExpressionEngine()
		if v, err := engine.EvaluateString(masked, workflow.ExpressionContext{
			JSON: item.JSON, Node: in.NodeOutputs, WorkflowID: in.WorkflowID, ExecutionID: in.ExecutionID,
		}); err == nil {
			masked = v
		}
	}
	if len(pairs) == 0 {
		return masked
	}
	return strings.NewReplacer(pairs...).Replace(masked)
}

// placeholderValue renders the item value a {{$json.KEY}} match stands for
// (objects and arrays as JSON); ok is false when the key is absent.
func placeholderValue(match string, item workflow.Item) (string, bool) {
	parts := templatePattern.FindStringSubmatch(match)
	if len(parts) < 2 || item.JSON == nil {
		return "", false
	}
	val, ok := item.JSON[parts[1]]
	if !ok {
		return "", false
	}
	switch v := val.(type) {
	case string:
		return v, true
	case map[string]interface{}, []interface{}:
		return Marshal(val), true
	}
	return fmt.Sprintf("%v", val), true
}
