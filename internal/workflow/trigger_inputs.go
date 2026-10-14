package workflow

import (
	"regexp"
	"sort"
	"strings"
)

// Trigger-input discovery: which fields of a manual run's `--input` payload a
// workflow's nodes actually read.
//
// A workflow whose node config says `{{ json $json.prompts }}` cannot do
// anything useful without a `prompts` supplied at run time — and used to say
// nothing about it, so `workflow run` with no --input (and the GUI's run
// button, which had no way to pass one) completed having done nothing. This
// answers "what does this workflow want?" for any caller: the CLI's
// `workflow inputs`, and through it the editor's run dialog.

var (
	// $json.field
	triggerDotRef = regexp.MustCompile(`\$json\.([A-Za-z_][A-Za-z0-9_]*)`)
	// $json["field"] / $json['field']
	triggerBracketRef = regexp.MustCompile(`\$json\[\s*["']([^"']+)["']\s*\]`)
	// A single {{ … }} expression, so a reference's surrounding call is known.
	triggerExpression = regexp.MustCompile(`\{\{([\s\S]*?)\}\}`)
	// $node["Trigger name"].json.field
	triggerNodeRef = regexp.MustCompile(`\$node\[\s*["']([^"']*)["']\s*\]\.json\.([A-Za-z_][A-Za-z0-9_]*)`)
	// `json $json.x` / `toJson $json.x` — the value is a collection, not a scalar.
	triggerJSONCall = regexp.MustCompile(`(?i)\bjson\s+\$(?:json[.\[]|node\[)`)
)

// TriggerField is one field a workflow reads from its trigger payload.
type TriggerField struct {
	Name string `json:"name"`
	// Structured is true when the field is read through the `json` template
	// function, which means the workflow wants a collection there.
	Structured bool `json:"structured"`
	// Examples are the example values the reading node's schema offers for
	// the config key that consumes this field — so a caller can show what to
	// type rather than an empty box.
	Examples []string `json:"examples,omitempty"`
	// Nodes names the nodes that read this field, for a caller that wants to
	// explain where the requirement comes from.
	Nodes []string `json:"nodes,omitempty"`
}

// SchemaLookup resolves a node type to its schema. Callers pass
// LoadDefaultSchema in production; tests pass a stub.
type SchemaLookup func(nodeType string) (*NodeSchema, error)

// TriggerInputs returns the trigger fields a workflow's nodes read, in
// first-seen order.
//
// $json is the trigger payload only in a node the trigger feeds directly (or
// a non-trigger node nothing feeds); further down it is the previous node's
// output, e.g. an HTTP response's "body", so those nodes count only through
// the $node["<trigger name>"].json.<field> form.
//
// Schemas come from the lookup, never from a schema stored alongside the
// workflow: a saved workflow carries the snapshot taken when it was saved, so
// a field that has since gained examples would offer none to exactly the
// workflow whose author needs them.
func TriggerInputs(nodes []WorkflowNode, conns []WorkflowConnection, lookup SchemaLookup) []TriggerField {
	if lookup == nil {
		lookup = LoadDefaultSchema
	}
	order := []string{}
	byName := map[string]*TriggerField{}

	triggers := map[string]bool{}
	for _, n := range nodes {
		if strings.HasPrefix(n.Type, "trigger.") {
			triggers[n.ID] = true
			triggers[n.Name] = true
		}
	}
	fed, fedByTrigger := map[string]bool{}, map[string]bool{}
	for _, c := range conns {
		fed[c.TargetNodeID] = true
		if triggers[c.SourceNodeID] {
			fedByTrigger[c.TargetNodeID] = true
		}
	}

	for _, node := range nodes {
		readsPayload := fedByTrigger[node.ID] ||
			(!fed[node.ID] && !strings.HasPrefix(node.Type, "trigger."))
		examplesFor := schemaExampleLookup(node.Type, lookup)
		// Sorted, not map order: the field order is part of the output
		// (it decides how the skeleton reads), so it must not change
		// between runs of the same workflow.
		configKeys := make([]string, 0, len(node.Config))
		for k := range node.Config {
			configKeys = append(configKeys, k)
		}
		sort.Strings(configKeys)
		for _, configKey := range configKeys {
			for _, text := range configStrings(node.Config[configKey]) {
				for _, expr := range triggerExpression.FindAllStringSubmatch(text, -1) {
					body := expr[1]
					structured := triggerJSONCall.MatchString(body)
					for _, name := range referencedFields(body, readsPayload, triggers) {
						field, seen := byName[name]
						if !seen {
							field = &TriggerField{Name: name}
							byName[name] = field
							order = append(order, name)
						}
						field.Structured = field.Structured || structured
						if len(field.Examples) == 0 {
							field.Examples = examplesFor(configKey)
						}
						if node.Name != "" && !contains(field.Nodes, node.Name) {
							field.Nodes = append(field.Nodes, node.Name)
						}
					}
				}
			}
		}
	}

	out := make([]TriggerField, 0, len(order))
	for _, name := range order {
		out = append(out, *byName[name])
	}
	return out
}

// referencedFields returns every trigger field named in one expression body,
// deduplicated and ordered so the result is stable. $json references count
// only when readsPayload; $node["<name>"].json references count when name is
// a trigger node.
func referencedFields(body string, readsPayload bool, triggers map[string]bool) []string {
	seen := map[string]bool{}
	names := []string{}
	add := func(name string) {
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	if readsPayload {
		for _, m := range triggerDotRef.FindAllStringSubmatch(body, -1) {
			add(m[1])
		}
		for _, m := range triggerBracketRef.FindAllStringSubmatch(body, -1) {
			add(m[1])
		}
	}
	for _, m := range triggerNodeRef.FindAllStringSubmatch(body, -1) {
		if triggers[m[1]] {
			add(m[2])
		}
	}
	return names
}

// schemaExampleLookup loads a node type's schema once and returns a lookup
// from config key to that field's examples.
func schemaExampleLookup(nodeType string, lookup SchemaLookup) func(string) []string {
	schema, err := lookup(nodeType)
	if err != nil || schema == nil {
		return func(string) []string { return nil }
	}
	return func(configKey string) []string {
		for _, f := range schema.Fields {
			if f.Key == configKey && len(f.Examples) > 0 {
				return append([]string(nil), f.Examples...)
			}
		}
		return nil
	}
}

// configStrings flattens one config value to the strings inside it, however
// deeply nested — a template can sit in an array element or a sub-object.
func configStrings(value interface{}) []string {
	switch v := value.(type) {
	case string:
		return []string{v}
	case []interface{}:
		var out []string
		for _, item := range v {
			out = append(out, configStrings(item)...)
		}
		return out
	case map[string]interface{}:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys) // stable output regardless of map order
		var out []string
		for _, k := range keys {
			out = append(out, configStrings(v[k])...)
		}
		return out
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// TriggerInputSkeleton renders fields as a JSON object a person can edit: the
// field's examples when it has them (so the shape and the wording are both
// visible), an empty collection or string otherwise.
func TriggerInputSkeleton(fields []TriggerField) map[string]interface{} {
	out := map[string]interface{}{}
	for _, f := range fields {
		switch {
		case f.Structured && len(f.Examples) > 0:
			vals := make([]interface{}, 0, len(f.Examples))
			for _, e := range f.Examples {
				vals = append(vals, e)
			}
			out[f.Name] = vals
		case f.Structured:
			out[f.Name] = []interface{}{}
		case len(f.Examples) > 0:
			out[f.Name] = f.Examples[0]
		default:
			out[f.Name] = ""
		}
	}
	return out
}
