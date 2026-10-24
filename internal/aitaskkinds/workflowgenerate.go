package aitaskkinds

import "github.com/monoes/mono-agent/internal/aitask"

func obj(required []any, props map[string]any) map[string]any {
	return map[string]any{"type": "object", "required": required, "additionalProperties": false, "properties": props}
}

func workflowGenerateKind() aitask.Kind {
	return aitask.Kind{
		Name:          WorkflowGenerate,
		DefaultAccess: aitask.AccessWorkspace,
		Schema: obj([]any{"summary", "workflow", "validation"}, map[string]any{
			"summary": map[string]any{"type": "string", "minLength": float64(1), "maxLength": float64(maxText)},
			"workflow": map[string]any{
				"type": "object", "required": []any{"name", "nodes"}, "additionalProperties": false,
				"properties": map[string]any{
					"name":        map[string]any{"type": "string", "minLength": float64(1), "maxLength": float64(maxName)},
					"description": str(maxText),
					"is_active":   map[string]any{"type": "boolean", "enum": []any{false}},
					"nodes":       map[string]any{"type": "array", "minItems": float64(1), "maxItems": float64(maxNodes), "items": map[string]any{"type": "object"}},
					"connections": map[string]any{"type": "array", "maxItems": float64(maxConns), "items": map[string]any{"type": "object"}},
				},
			},
			"validation": obj([]any{"valid"}, map[string]any{
				"valid":    map[string]any{"type": "boolean"},
				"errors":   strList(maxListLen, maxListStr),
				"warnings": strList(maxListLen, maxListStr),
			}),
		}),
		PromptTemplate: `Propose ONE workflow for the user's brief (the goal above). You design it; you do not save, import, activate or run it, and you have no file to write: the whole workflow goes into the result. ` + dataRule + `

1. Find before you build: ` + "`monoagentcli --profile P workflow search <words>`" + `, ` + "`workflow templates list`" + ` and ` + "`workflow templates show <id>`" + `, ` + "`workflow get <id>`" + `. A template or an existing workflow that does most of it is a better start than a blank page.
2. Look up the node types: ` + "`monoagentcli --profile P node list`, `node schema <type>`" + ` for each one you use, and ` + "`ref workflow`, `ref expressions`" + ` for the format. Use only node types that exist, with exactly the config keys the schema names and every required field filled.
3. Write the workflow as JSON: name, description, is_active false, nodes (id, type, name, position, config) and connections (source, source_handle, target, target_handle). Exactly one trigger node; trigger.manual unless the brief asks for a schedule or a webhook.
4. A step that sends, posts, publishes, writes to an outside service or runs code is the user's to approve: say which nodes do, in the summary, in plain words. Never put a real password, token or key in it: use the placeholder REPLACE_ME or a saved connection (credential_id).
5. Hand the result back with ` + "`aitask complete`" + `: {summary, workflow, validation}. summary: what the workflow does, which steps send something, and what the user still has to supply. validation is your own note; the real verdict is recomputed with the validator ` + "`workflow validate`" + ` uses, and the result is refused with its reasons when the workflow does not pass: fix them and complete again.`,
	}
}
