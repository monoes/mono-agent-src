package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"text/template/parse"
	"time"
)

// bracketIndexPattern matches the documented $node["Name"] / $json["key"]
// bracket-index syntax (see cmd/monoagentcli/ref.go's expression docs) so it
// can be rewritten into Go template's actual index-function syntax before
// parsing — text/template has no native support for `x["key"]`.
var bracketIndexPattern = regexp.MustCompile(`(\$\w+)\["([^"]*)"\]`)

// rewriteBracketIndex rewrites every `$name["key"]` occurrence in tmpl into
// `(index $name "key")`.
func rewriteBracketIndex(tmpl string) string {
	return bracketIndexPattern.ReplaceAllString(tmpl, `(index $1 "$2")`)
}

// preambleVars are the variables a template can use directly without
// referencing dot; each is declared as `$name := .name`.
var preambleVars = []string{
	"json", "node", "items", "workflow", "execution", "env",
	"now", "today", "trigger", "vars", "runIndex", "prevNode",
}

// preambleFor returns the declarations prepended to src: only the variables
// src mentions, because every declaration costs a map lookup on each
// execution. A template that mentions none still gets a trimming no-op, so
// leading whitespace is trimmed the same way either way.
func preambleFor(src string) string {
	var b strings.Builder
	for _, v := range preambleVars {
		if strings.Contains(src, "$"+v) {
			b.WriteString("{{- $" + v + " := ." + v + " -}}")
		}
	}
	if b.Len() == 0 {
		return "{{- /* */ -}}"
	}
	return b.String()
}

// nodeJSONProxy is a map[string]interface{} whose key "json" holds the
// node's first-item JSON map and whose keys "all" and "items" hold every
// item's JSON.  This lets templates use the syntax $node["Name"].json.someKey
// — the template looks up key "json" on the map and then key "someKey" on
// the inner map.
type nodeJSONProxy = map[string]interface{}

// ExpressionEngine evaluates Go text/template expressions against an
// ExpressionContext.  Parsed templates are cached in a sync.Map for
// performance.
type ExpressionEngine struct {
	cache sync.Map // key: template string → *parsedTemplate
	funcs template.FuncMap
}

// NewExpressionEngine returns a fully configured ExpressionEngine.
func NewExpressionEngine() *ExpressionEngine {
	e := &ExpressionEngine{}
	e.funcs = e.buildFuncMap()
	addStringMatchFuncs(e.funcs)
	return e
}

// buildFuncMap returns the safe function set exposed to all templates.
func (e *ExpressionEngine) buildFuncMap() template.FuncMap {
	return template.FuncMap{
		noValueFunc: blankNoValue,
		// Encoding
		"json": func(v interface{}) (string, error) {
			b, err := json.Marshal(v)
			if err != nil {
				return "", err
			}
			return string(b), nil
		},
		"jsonParse": func(s string) (interface{}, error) {
			var out interface{}
			if err := json.Unmarshal([]byte(s), &out); err != nil {
				return nil, err
			}
			return out, nil
		},

		// Type conversions
		"toString": func(v interface{}) string {
			if v == nil {
				return ""
			}
			return fmt.Sprintf("%v", v)
		},
		"toInt": func(v interface{}) (int64, error) {
			switch t := v.(type) {
			case int:
				return int64(t), nil
			case int32:
				return int64(t), nil
			case int64:
				return t, nil
			case float32:
				return int64(t), nil
			case float64:
				return int64(t), nil
			case string:
				return strconv.ParseInt(strings.TrimSpace(t), 10, 64)
			case bool:
				if t {
					return 1, nil
				}
				return 0, nil
			default:
				return 0, fmt.Errorf("toInt: cannot convert %T", v)
			}
		},
		"toFloat": toFloat,
		"toBool": func(v interface{}) (bool, error) {
			switch t := v.(type) {
			case bool:
				return t, nil
			case int:
				return t != 0, nil
			case int64:
				return t != 0, nil
			case float64:
				return t != 0, nil
			case string:
				return strconv.ParseBool(strings.TrimSpace(t))
			default:
				return false, fmt.Errorf("toBool: cannot convert %T", v)
			}
		},

		// String operations
		"upper":     strings.ToUpper,
		"lower":     strings.ToLower,
		"trim":      strings.TrimSpace,
		"trimLeft":  func(cutset, s string) string { return strings.TrimLeft(s, cutset) },
		"trimRight": func(cutset, s string) string { return strings.TrimRight(s, cutset) },
		"split":     func(sep, s string) []string { return strings.Split(s, sep) },
		"join":      func(sep string, parts []string) string { return strings.Join(parts, sep) },
		"replace":   func(old, new, s string) string { return strings.ReplaceAll(s, old, new) },

		// Map helpers
		"hasKey": func(m map[string]interface{}, key string) bool {
			if m == nil {
				return false
			}
			_, ok := m[key]
			return ok
		},
		"default": func(def interface{}, v interface{}) interface{} {
			if v == nil {
				return def
			}
			// Also treat zero-value string as absent.
			if s, ok := v.(string); ok && s == "" {
				return def
			}
			return v
		},

		// Time
		"now": func() string {
			return time.Now().UTC().Format(time.RFC3339)
		},

		// Arithmetic (all operate on float64; operands convert as toFloat,
		// so integers from code nodes and $runIndex work too)
		"add": arith(func(a, b float64) (float64, error) { return a + b, nil }),
		"sub": arith(func(a, b float64) (float64, error) { return a - b, nil }),
		"mul": arith(func(a, b float64) (float64, error) { return a * b, nil }),
		"div": arith(func(a, b float64) (float64, error) {
			if b == 0 {
				return 0, fmt.Errorf("div: division by zero")
			}
			return a / b, nil
		}),

		// Collection helpers
		"len": func(v interface{}) (int, error) {
			switch t := v.(type) {
			case string:
				return len(t), nil
			case []interface{}:
				return len(t), nil
			case map[string]interface{}:
				return len(t), nil
			default:
				return 0, fmt.Errorf("len: unsupported type %T", v)
			}
		},
		// index provides explicit slice/map indexing (supplements the built-in).
		"index": func(collection interface{}, key interface{}) (interface{}, error) {
			switch c := collection.(type) {
			case []interface{}:
				idx, err := toIndexInt(key)
				if err != nil {
					return nil, err
				}
				if idx < 0 || idx >= len(c) {
					return nil, nil
				}
				return c[idx], nil
			case []map[string]interface{}:
				idx, err := toIndexInt(key)
				if err != nil {
					return nil, err
				}
				if idx < 0 || idx >= len(c) {
					return nil, nil
				}
				return c[idx], nil
			case map[string]interface{}:
				k, ok := key.(string)
				if !ok {
					return nil, fmt.Errorf("index: map key must be string, got %T", key)
				}
				return c[k], nil
			default:
				return nil, fmt.Errorf("index: unsupported collection type %T", collection)
			}
		},
	}
}

// toIndexInt converts an arbitrary key value to an integer slice index.
func toIndexInt(key interface{}) (int, error) {
	switch k := key.(type) {
	case int:
		return k, nil
	case int64:
		return int(k), nil
	case float64:
		return int(k), nil
	case string:
		n, err := strconv.Atoi(k)
		if err != nil {
			return 0, fmt.Errorf("index: invalid index %q", k)
		}
		return n, nil
	default:
		return 0, fmt.Errorf("index: unsupported key type %T", key)
	}
}

// parse returns a cached *parsedTemplate for the given source string.
// A preamble is prepended so $json/$node/... variables are available.
func (e *ExpressionEngine) parse(src string) (*parsedTemplate, error) {
	if v, ok := e.cache.Load(src); ok {
		return v.(*parsedTemplate), nil
	}
	body := rewriteBracketIndex(src)
	pre := preambleFor(body)
	full := pre + body
	t, err := template.New("").
		Option("missingkey=zero").
		Funcs(e.funcs).
		Parse(full)
	if err != nil {
		return nil, fmt.Errorf("expression: parse error in %q: %w", src, err)
	}
	for _, tt := range t.Templates() {
		if tt.Tree != nil {
			blankMissingValues(tt.Tree, tt.Tree.Root)
		}
	}
	p := &parsedTemplate{t: t, needs: needsOf(t, len(pre))}
	e.cache.Store(src, p)
	return p, nil
}

// noValueFunc is the function blankMissingValues appends to every printed
// pipeline. The leading underscore keeps it out of the documented set.
const noValueFunc = "_blankNoValue"

// blankNoValue turns a missing value into the empty string. text/template
// prints a nil interface — a key missing from a map, even with
// missingkey=zero, or a JSON null — as the literal "<no value>", which then
// passed required-input checks as if it were real data: a search node ran
// with the keyword "<no value>" and reported success.
func blankNoValue(v interface{}) interface{} {
	if v == nil {
		return ""
	}
	return v
}

// blankMissingValues appends noValueFunc to the pipeline of every action
// that prints, so a missing value renders as "". Conditions ({{ if }},
// {{ with }}, {{ range }}) and variable declarations are left alone: they
// still see the missing value as absent, so `{{ if $json.limit }}` keeps
// falling through to its else branch.
func blankMissingValues(tree *parse.Tree, n parse.Node) {
	switch x := n.(type) {
	case *parse.ListNode:
		if x == nil {
			return
		}
		for _, c := range x.Nodes {
			blankMissingValues(tree, c)
		}
	case *parse.ActionNode:
		if x.Pipe == nil || len(x.Pipe.Decl) > 0 {
			return
		}
		ident := parse.NewIdentifier(noValueFunc).SetTree(tree).SetPos(x.Pos)
		x.Pipe.Cmds = append(x.Pipe.Cmds, &parse.CommandNode{NodeType: parse.NodeCommand, Pos: x.Pos, Args: []parse.Node{ident}})
	case *parse.IfNode:
		blankMissingValues(tree, x.List)
		blankMissingValues(tree, x.ElseList)
	case *parse.RangeNode:
		blankMissingValues(tree, x.List)
		blankMissingValues(tree, x.ElseList)
	case *parse.WithNode:
		blankMissingValues(tree, x.List)
		blankMissingValues(tree, x.ElseList)
	}
}

// EvaluateString evaluates a template string against the given context.
// Strings that contain no {{ }} delimiters are returned as-is without parsing.
func (e *ExpressionEngine) EvaluateString(tmpl string, ctx ExpressionContext) (string, error) {
	if !strings.Contains(tmpl, "{{") {
		return tmpl, nil
	}

	p, err := e.parse(tmpl)
	if err != nil {
		return "", err
	}

	data := buildData(ctx, p.needs)

	var buf bytes.Buffer
	if err := p.t.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("expression: execute error: %w", err)
	}
	return buf.String(), nil
}

// EvaluateBool evaluates a template string and converts the result to bool.
func (e *ExpressionEngine) EvaluateBool(tmpl string, ctx ExpressionContext) (bool, error) {
	s, err := e.EvaluateString(tmpl, ctx)
	if err != nil {
		return false, err
	}
	s = strings.TrimSpace(s)
	switch strings.ToLower(s) {
	case "true", "1", "yes":
		return true, nil
	case "false", "0", "no", "":
		return false, nil
	}
	// Report the rendered result's type and length only — the rendered
	// string may be (or contain) a secret, and must not appear in errors.
	return false, fmt.Errorf("expression: cannot convert value (type %T, len %d) to bool", s, len(s))
}

// EvaluateValue evaluates a template and returns the raw interface{} result.
// The output is parsed as JSON when possible; otherwise the raw string is returned.
func (e *ExpressionEngine) EvaluateValue(tmpl string, ctx ExpressionContext) (interface{}, error) {
	s, err := e.EvaluateString(tmpl, ctx)
	if err != nil {
		return nil, err
	}
	s = strings.TrimSpace(s)
	var out interface{}
	if jsonErr := json.Unmarshal([]byte(s), &out); jsonErr == nil {
		return out, nil
	}
	return s, nil
}

// ResolveConfig walks a config map and evaluates all string values as
// templates.  Non-string values pass through unchanged.  Nested maps and
// slices are recursively resolved.  An unprepared ctx is prepared once for
// the whole walk, so the per-node template data is built at most once.
func (e *ExpressionEngine) ResolveConfig(config map[string]interface{}, ctx ExpressionContext) (map[string]interface{}, error) {
	if ctx.base == nil {
		ctx = ctx.Prepared()
	}
	out := make(map[string]interface{}, len(config))
	for k, v := range config {
		resolved, err := e.resolveValue(v, ctx)
		if err != nil {
			return nil, fmt.Errorf("expression: resolving key %q: %w", k, err)
		}
		out[k] = resolved
	}
	return out, nil
}

// resolveValue recursively resolves a single value.
func (e *ExpressionEngine) resolveValue(v interface{}, ctx ExpressionContext) (interface{}, error) {
	switch t := v.(type) {
	case string:
		// Only a string that actually contains template syntax was subject to
		// evaluation; EvaluateString returns any other string unchanged. A
		// static literal (e.g. a hardcoded JSON-formatted default) that never
		// had "{{" in it must pass through as-is and must NOT be reinterpreted
		// as JSON below just because it happens to look like an array/object —
		// that would silently coerce its Go type out from under node code that
		// expects a plain string.
		hadTemplate := strings.Contains(t, "{{")
		s, err := e.EvaluateString(t, ctx)
		if err != nil {
			return nil, err
		}
		// If the template evaluation produced a string that looks like a JSON
		// array or object, try to parse it back to a native Go value so that
		// downstream nodes receive the correct type (e.g. []interface{}
		// instead of a raw string).
		if hadTemplate {
			trimmed := strings.TrimSpace(s)
			if len(trimmed) > 0 && (trimmed[0] == '[' || trimmed[0] == '{') {
				var parsed interface{}
				if json.Unmarshal([]byte(trimmed), &parsed) == nil {
					return parsed, nil
				}
			}
		}
		return s, nil
	case map[string]interface{}:
		return e.ResolveConfig(t, ctx)
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, elem := range t {
			resolved, err := e.resolveValue(elem, ctx)
			if err != nil {
				return nil, fmt.Errorf("expression: resolving slice index %d: %w", i, err)
			}
			out[i] = resolved
		}
		return out, nil
	default:
		return v, nil
	}
}

// ResolveItem walks an Item's JSON map and evaluates all string values as
// templates, returning a new Item with resolved values.
func (e *ExpressionEngine) ResolveItem(item Item, ctx ExpressionContext) (Item, error) {
	resolved, err := e.ResolveConfig(item.JSON, ctx)
	if err != nil {
		return Item{}, err
	}
	return Item{
		JSON:   resolved,
		Binary: item.Binary,
	}, nil
}

func toFloat(v interface{}) (float64, error) {
	switch t := v.(type) {
	case int:
		return float64(t), nil
	case int32:
		return float64(t), nil
	case int64:
		return float64(t), nil
	case float32:
		return float64(t), nil
	case float64:
		return t, nil
	case string:
		return strconv.ParseFloat(strings.TrimSpace(t), 64)
	case bool:
		if t {
			return 1, nil
		}
		return 0, nil
	default:
		return 0, fmt.Errorf("toFloat: cannot convert %T", v)
	}
}

// arith lifts a float64 operation to operands of any number type.
func arith(op func(a, b float64) (float64, error)) func(a, b interface{}) (float64, error) {
	return func(a, b interface{}) (float64, error) {
		x, err := toFloat(a)
		if err != nil {
			return 0, err
		}
		y, err := toFloat(b)
		if err != nil {
			return 0, err
		}
		return op(x, y)
	}
}
