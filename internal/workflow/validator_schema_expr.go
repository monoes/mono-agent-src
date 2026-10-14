package workflow

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// closestKey is the schema field key nearest to k by edit distance, when
// near enough to be a plausible typo.
func closestKey(k string, fields []NodeSchemaField) string {
	best, bestD := "", -1
	for _, f := range fields {
		d := levenshtein(strings.ToLower(k), strings.ToLower(f.Key))
		if bestD < 0 || d < bestD {
			best, bestD = f.Key, d
		}
	}
	limit := len(k) / 3
	if limit < 2 {
		limit = 2
	}
	if bestD < 0 || bestD > limit {
		return ""
	}
	return best
}

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// walkStrings calls fn for every string inside v, with its config path.
func walkStrings(v interface{}, path string, fn func(path, s string)) {
	switch t := v.(type) {
	case string:
		fn(path, t)
	case map[string]interface{}:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			walkStrings(t[k], path+"."+k, fn)
		}
	case []interface{}:
		for i, e := range t {
			walkStrings(e, fmt.Sprintf("%s[%d]", path, i), fn)
		}
	}
}

var (
	nodeRefPatterns = []*regexp.Regexp{
		regexp.MustCompile(`\$node\[\s*"([^"]*)"\s*\]`),
		regexp.MustCompile(`index\s+\$node\s+"([^"]*)"`),
		regexp.MustCompile(`\$node\.([A-Za-z_][A-Za-z0-9_]*)`),
	}
	// $('Name') is n8n's syntax, which these templates don't support.
	dollarCallPattern   = regexp.MustCompile(`\$\(\s*['"]([^'"]*)['"]\s*\)`)
	validatorExprEngine = NewExpressionEngine()
)

// looksLikeBrokenExpression reports a string with a closing "}}" but no
// opening "{{" that still reads like an expression ("{ $json.x }}"): the
// engine passes such a string through literally.
func looksLikeBrokenExpression(s string) bool {
	return !strings.Contains(s, "{{") && strings.Contains(s, "}}") &&
		(strings.Contains(s, "$json") || strings.Contains(s, "$node"))
}

func expressionIssues(nodeID, field, s string, names map[string]bool) []ValidationIssue {
	var out []ValidationIssue
	switch {
	case looksLikeBrokenExpression(s):
		out = append(out, ValidationIssue{
			NodeID: nodeID, Field: field, Code: IssueBadExpression, Severity: "error",
			Message: "unbalanced {{ }} in expression: \"}}\" without \"{{\"",
			Hint:    "every }} needs a matching {{",
		})
	case strings.Contains(s, "{{"):
		// The engine's own parser: a value it cannot parse fails the node.
		if _, err := validatorExprEngine.parse(s); err != nil {
			msg := err.Error()
			if i := strings.LastIndex(msg, ": "); i >= 0 {
				msg = msg[i+2:]
			}
			hint := "expressions are Go templates, e.g. {{ $json.field }} or {{ $node[\"Name\"].json.field }}"
			switch {
			case dollarCallPattern.MatchString(s):
				hint = "$('Name') is not supported; use $node[\"Name\"].json.field"
			case strings.Contains(msg, "unclosed action"):
				msg = "unbalanced {{ }} (" + msg + ")"
				hint = "every {{ needs a matching }}"
			}
			out = append(out, ValidationIssue{
				NodeID: nodeID, Field: field, Code: IssueBadExpression, Severity: "error",
				Message: "invalid expression: " + msg, Hint: hint,
			})
		}
	default:
		return nil
	}
	seen := map[string]bool{}
	pats := append(append([]*regexp.Regexp{}, nodeRefPatterns...), dollarCallPattern)
	for _, p := range pats {
		for _, m := range p.FindAllStringSubmatch(s, -1) {
			ref := m[1]
			if names[ref] || seen[ref] {
				continue
			}
			seen[ref] = true
			out = append(out, ValidationIssue{
				NodeID: nodeID, Field: field, Code: IssueDanglingNodeRef, Severity: "error",
				Message: fmt.Sprintf("expression references node %q, which is not in this workflow", ref),
				Hint:    "reference a node by its name (or id) exactly as it appears in the workflow",
			})
		}
	}
	return out
}

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
