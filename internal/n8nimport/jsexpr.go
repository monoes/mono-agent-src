package n8nimport

import (
	"fmt"
	"regexp"
	"strings"
)

// unavailableInCode matches n8n names a mono core.code node cannot
// provide: Node's Buffer, other nodes' data, binary data, workflow/execution metadata and
// variables.
var unavailableInCode = regexp.MustCompile(`\bBuffer\b|\$\(|\$node\b|\$items\b|\$binary\b|\$execution\b|\$workflow\b|\$env\b|\$vars\b|\$prevNode\b|\$parameter\b|\$jmespath\b|\$evaluateExpression\b|\bthis\.|\$getWorkflowStaticData\b|\brequire\s*\(`)

// unavailableInCodeNode is unavailableInCode for a Code node's own
// JavaScript, where $('Node') and $node read other nodes' output.
var unavailableInCodeNode = regexp.MustCompile(`\bBuffer\b|\$items\b|\$binary\b|\$execution\b|\$workflow\b|\$env\b|\$vars\b|\$prevNode\b|\$parameter\b|\$jmespath\b|\$evaluateExpression\b|\bthis\.|\$getWorkflowStaticData\b|\brequire\s*\(`)

// expressionFails reports whether an n8n value is an expression the
// template translator cannot express.
func expressionFails(v interface{}) bool {
	s, ok := v.(string)
	if !ok || !strings.HasPrefix(s, "=") {
		return false
	}
	tr := &exprTranslator{}
	_, ok = tr.String(s)
	return !ok
}

// jsForValue returns JavaScript that computes an n8n parameter value: a
// literal for plain values, and for "=…{{ js }}…" the code blocks joined
// with the literal text (a lone block keeps its type, as in n8n). ok is
// false when the expression uses something core.code cannot provide.
func jsForValue(v interface{}) (string, bool) {
	s, isStr := v.(string)
	if !isStr || !strings.HasPrefix(s, "=") {
		return jsValue(v), true
	}
	if unavailableInCode.MatchString(s) {
		return "", false
	}
	parts, err := splitTemplate(s[1:])
	if err != nil {
		return "", false
	}
	if len(parts) == 1 && parts[0].isCode {
		return "(" + strings.TrimSpace(parts[0].code) + ")", true
	}
	var segs []string
	for _, p := range parts {
		if p.isCode {
			segs = append(segs, "__n8nStr("+strings.TrimSpace(p.code)+")")
		} else {
			segs = append(segs, jsQuote(p.text))
		}
	}
	if len(segs) == 0 {
		return `""`, true
	}
	return strings.Join(segs, " + "), true
}

// jsValueHelpers are shared by generated per-item programs.
const jsValueHelpers = `function __n8nStr(v) { if (v === undefined || v === null) { return ""; } return typeof v === "object" ? JSON.stringify(v) : String(v); }
function __n8nConv(v, t) {
  if (t === "number") { return Number(v); }
  if (t === "bool") { return v === true || v === "true" || v === 1 || v === "1"; }
  if (t === "json" && typeof v === "string") { try { return JSON.parse(v); } catch (e) { return v; } }
  if (t === "string") { return __n8nStr(v); }
  return v;
}
function __n8nSet(o, path, v) {
  var keys = String(path).split(".");
  var cur = o;
  for (var i = 0; i < keys.length - 1; i++) {
    if (cur[keys[i]] === null || typeof cur[keys[i]] !== "object") { cur[keys[i]] = {}; }
    cur = cur[keys[i]];
  }
  cur[keys[keys.length - 1]] = v;
}
`

// setViaCode maps a Set node to core.code when one of its values is an
// expression the template language cannot express but JavaScript can:
// the n8n expressions then run as they are, once per item.
func setViaCode(c *MapContext, assigns []assignment, include string, fields []string) (Mapping, bool) {
	need := false
	for _, a := range assigns {
		if expressionFails(a.value) {
			need = true
		}
	}
	if !need {
		return Mapping{}, false
	}
	var body strings.Builder
	body.WriteString(jsValueHelpers)
	body.WriteString("var o = " + jsBase(include, fields, "$json") + ";\n")
	for _, a := range assigns {
		js, ok := jsForValue(a.value)
		if !ok {
			return Mapping{}, false
		}
		fmt.Fprintf(&body, "__n8nSet(o, %s, __n8nConv(%s, %s));\n", jsQuote(a.name), js, jsQuote(a.typ))
	}
	body.WriteString("return o;\n")
	c.Note("mapped to core.code so its JavaScript expressions run unchanged")
	return Mapping{Type: "core.code", Config: codeConfig(wrapPerItem(body.String()))}, true
}
