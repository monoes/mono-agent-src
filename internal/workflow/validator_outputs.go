package workflow

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// jsonFieldPatterns match the top-level key a $json reference reads.
var jsonFieldPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\$json\.([A-Za-z_][A-Za-z0-9_]*)`),
	regexp.MustCompile(`\$json\[\s*"([^"]+)"\s*\]`),
	regexp.MustCompile(`index\s+\$json\s+"([^"]+)"`),
}

// outputGraph answers which keys the items reaching a node can carry,
// from the upstream nodes' declared schema outputs.
type outputGraph struct {
	byID  map[string]WorkflowNode
	preds map[string][]WorkflowConnection
	memo  map[string]map[string]bool
	busy  map[string]bool
}

func newOutputGraph(nodes []WorkflowNode, conns []WorkflowConnection) *outputGraph {
	g := &outputGraph{
		byID:  map[string]WorkflowNode{},
		preds: map[string][]WorkflowConnection{},
		memo:  map[string]map[string]bool{},
		busy:  map[string]bool{},
	}
	for _, n := range nodes {
		g.byID[n.ID] = n
	}
	for _, c := range conns {
		g.preds[c.TargetNodeID] = append(g.preds[c.TargetNodeID], c)
	}
	return g
}

// schemaOutputs is a node's schema outputs (nil when it declares none).
func schemaOutputs(nodeType string) []NodeSchemaOutput {
	if _, ok := embeddedSchemaFile(nodeType); !ok {
		return nil
	}
	s, err := LoadDefaultSchema(nodeType)
	if err != nil || s == nil {
		return nil
	}
	return s.Outputs
}

// outputKeys is the set of keys n's main-output items can carry; nil when
// that is not known (no declared outputs, an open key set, or a
// passthrough whose input is not known).
func (g *outputGraph) outputKeys(id string) map[string]bool {
	if keys, ok := g.memo[id]; ok {
		return keys
	}
	if g.busy[id] {
		return nil
	}
	g.busy[id] = true
	defer delete(g.busy, id)

	n, ok := g.byID[id]
	var keys map[string]bool
	if ok {
		keys = g.resolveOutputs(n)
	}
	g.memo[id] = keys
	return keys
}

func (g *outputGraph) resolveOutputs(n WorkflowNode) map[string]bool {
	outs := schemaOutputs(n.Type)
	if len(outs) == 0 {
		return nil
	}
	var schema *NodeSchema
	keys := map[string]bool{}
	for _, o := range outs {
		switch {
		case o.Key == "*":
			in := g.inputKeys(n.ID)
			if in == nil {
				return nil
			}
			for k := range in {
				keys[k] = true
			}
		case strings.HasPrefix(o.Key, "<") && strings.HasSuffix(o.Key, ">"):
			// Named by config fields ("<a|b>": a's value, else b's,
			// else their defaults); any other name is a data-named,
			// open key set.
			if schema == nil {
				schema, _ = LoadDefaultSchema(n.Type)
			}
			name, ok := configNamedKey(n, schema, o.Key[1:len(o.Key)-1])
			if !ok {
				return nil
			}
			keys[name] = true
		default:
			keys[o.Key] = true
		}
	}
	return keys
}

// configNamedKey resolves an output key named by the config fields in
// alts ("a|b"): the first one set in the config, else the first default.
// ok is false for a name that is no config field, or a value that is an
// expression or empty.
func configNamedKey(n WorkflowNode, schema *NodeSchema, alts string) (string, bool) {
	var fields []*NodeSchemaField
	for _, name := range strings.Split(alts, "|") {
		var field *NodeSchemaField
		if schema != nil {
			for i := range schema.Fields {
				if schema.Fields[i].Key == name {
					field = &schema.Fields[i]
				}
			}
		}
		if field == nil {
			return "", false
		}
		fields = append(fields, field)
	}
	pick := func(v interface{}) (string, bool) {
		s, isStr := v.(string)
		// A list of names ("a, b") is not one key: treat the set as open.
		if !isStr || strings.TrimSpace(s) == "" || strings.Contains(s, "{{") || strings.ContainsAny(s, ",\n") {
			return "", false
		}
		return s, true
	}
	for _, f := range fields {
		if v, set := n.Config[f.Key]; set && !isEmptyValue(v) {
			return pick(v)
		}
	}
	for _, f := range fields {
		if f.Default != nil {
			return pick(f.Default)
		}
	}
	return "", false
}

// inputKeys is the union of the output keys of every node wired into id
// on a non-error handle; nil when any of them is unknown or there is none.
func (g *outputGraph) inputKeys(id string) map[string]bool {
	preds := g.preds[id]
	if len(preds) == 0 {
		return nil
	}
	keys := map[string]bool{}
	for _, c := range preds {
		if c.SourceHandle == "error" {
			return nil
		}
		up := g.outputKeys(c.SourceNodeID)
		if up == nil {
			return nil
		}
		for k := range up {
			keys[k] = true
		}
	}
	return keys
}

// upstreamFieldIssues warns about $json.<key> references in s that no
// node feeding n declares among its outputs.
func (g *outputGraph) upstreamFieldIssues(n WorkflowNode, field, s string) []ValidationIssue {
	if !strings.Contains(s, "$json") {
		return nil
	}
	var refs []string
	for _, p := range jsonFieldPatterns {
		for _, m := range p.FindAllStringSubmatch(s, -1) {
			refs = append(refs, m[1])
		}
	}
	if len(refs) == 0 {
		return nil
	}
	in := g.inputKeys(n.ID)
	if in == nil {
		return nil
	}
	var out []ValidationIssue
	seen := map[string]bool{}
	for _, k := range refs {
		if in[k] || k == "error" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, ValidationIssue{
			NodeID: n.ID, Field: field, Code: IssueUnknownUpstreamField, Severity: "warning",
			Message: fmt.Sprintf("$json.%s is not among the fields the upstream node(s) output", k),
			Hint:    "upstream outputs: " + strings.Join(sortedKeys(in), ", "),
		})
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
