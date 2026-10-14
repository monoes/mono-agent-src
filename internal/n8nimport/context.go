package n8nimport

import (
	"fmt"
	"sort"
	"strings"
)

// SubNode is an n8n sub-node (a LangChain model, tool, memory, …) attached
// to the node being mapped through a non-"main" connection.
type SubNode struct {
	Name        string
	Type        string
	TypeVersion float64
	Kind        string // connection type, e.g. "ai_languageModel"
	Parameters  map[string]interface{}
	Credentials map[string]n8nCredentialRef
	// Children are the sub-nodes attached to this sub-node (a document
	// loader's text splitter, …).
	Children []SubNode
}

// MapContext is what a Mapper reads the n8n node from and records report
// entries on. Parameter paths use dots for nesting ("options.timeout").
type MapContext struct {
	NodeName    string
	Type        string
	TypeVersion float64
	Params      map[string]interface{}
	Credentials map[string]n8nCredentialRef
	SubNodes    []SubNode
	// WebhookID is the n8n node's webhookId (the path n8n served the
	// node's webhook under), empty for nodes without a webhook.
	WebhookID string
	// OnErrorOutput is true when the node routes failures to an extra
	// n8n output (onError = continueErrorOutput).
	OnErrorOutput bool

	tr            *exprTranslator
	used          map[string]bool
	issues        []Issue
	exprs         []ExprIssue
	creds         []credUse
	opUnsupported bool
}

type credUse struct {
	Platform string
	N8nType  string
	N8nName  string
	Hint     string
}

func newMapContext(n *n8nNode, wfName string) *MapContext {
	return &MapContext{
		NodeName:    n.Name,
		Type:        n.Type,
		TypeVersion: n.TypeVersion,
		Params:      n.Parameters,
		Credentials: n.Credentials,
		WebhookID:   n.WebhookID,
		tr:          &exprTranslator{workflowName: wfName},
		used:        map[string]bool{},
	}
}

// lookupPath returns the raw value at a dotted path.
func lookupPath(m map[string]interface{}, path string) (interface{}, bool) {
	cur := interface{}(m)
	for _, part := range strings.Split(path, ".") {
		mm, ok := cur.(map[string]interface{})
		if !ok {
			return nil, false
		}
		if cur, ok = mm[part]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// Has reports whether the n8n node sets the parameter (n8n omits
// parameters left at their default).
func (c *MapContext) Has(path string) bool {
	_, ok := lookupPath(c.Params, path)
	return ok
}

// Raw returns the untranslated parameter value and marks it used.
func (c *MapContext) Raw(path string) interface{} {
	c.markUsed(path)
	v, _ := lookupPath(c.Params, path)
	return v
}

// Ignore marks parameters as handled without a mono counterpart (UI-only
// or irrelevant options), so the report does not list them.
func (c *MapContext) Ignore(paths ...string) {
	for _, p := range paths {
		c.markUsed(p)
	}
}

// markUsed marks path used. Reading inside a parameter that is not one of
// the collectionKeys consumes the whole parameter; inside a collection it
// consumes that collection entry ("options.response.x" uses
// "options.response").
func (c *MapContext) markUsed(path string) {
	c.used[path] = true
	parts := strings.Split(path, ".")
	if len(parts) < 2 {
		return
	}
	if !collectionKeys[parts[0]] {
		c.used[parts[0]] = true
		return
	}
	c.used[parts[0]+"."+parts[1]] = true
}

// translate translates a value, attributing failures to param.
func (c *MapContext) translate(param string, v interface{}) interface{} {
	before := len(c.tr.failures)
	out := c.tr.Value(v)
	for _, f := range c.tr.failures[before:] {
		c.exprs = append(c.exprs, ExprIssue{Param: param, Expression: f.Expression, Reason: f.Reason})
	}
	return out
}

// Translate translates any value (strings, maps, slices) for param.
func (c *MapContext) Translate(param string, v interface{}) interface{} { return c.translate(param, v) }

// Value returns the parameter with expressions translated.
func (c *MapContext) Value(path string) interface{} {
	return c.translate(path, c.Raw(path))
}

// Str returns the parameter as a mono string (expressions translated, plain
// text escaped), or def when it is absent.
func (c *MapContext) Str(path, def string) string {
	v := c.Raw(path)
	if v == nil {
		v = def
	}
	switch x := c.translate(path, v).(type) {
	case string:
		return x
	case nil:
		return ""
	default:
		return fmt.Sprint(x)
	}
}

// Literal returns an option-like parameter (resource, operation, mode …)
// as plain text. An expression there cannot be mapped statically: it is
// reported and def is used.
func (c *MapContext) Literal(path, def string) string {
	v := c.Raw(path)
	switch x := v.(type) {
	case nil:
		return def
	case string:
		if strings.HasPrefix(x, "=") {
			c.Warn(path, x, "is set by an expression; the importer used %q", def)
			return def
		}
		return x
	default:
		return fmt.Sprint(x)
	}
}

// Num returns a numeric parameter. An expression is translated and
// returned as a string template (mono resolves it to text at run time).
func (c *MapContext) Num(path string, def float64) interface{} {
	v := c.Raw(path)
	switch x := v.(type) {
	case nil:
		return def
	case float64:
		return x
	case string:
		if strings.HasPrefix(x, "=") {
			return c.translate(path, x)
		}
		var f float64
		if _, err := fmt.Sscanf(x, "%g", &f); err == nil {
			return f
		}
	}
	return def
}

// Float returns a numeric parameter as a number, def when absent or an
// expression (the expression is reported).
func (c *MapContext) Float(path string, def float64) float64 {
	switch x := c.Num(path, def).(type) {
	case float64:
		return x
	default:
		c.Warn(path, c.Raw(path), "is an expression; the importer used %v", def)
		return def
	}
}

// Bool returns a boolean parameter.
func (c *MapContext) Bool(path string, def bool) bool {
	switch x := c.Raw(path).(type) {
	case bool:
		return x
	case string:
		if strings.HasPrefix(x, "=") {
			c.Warn(path, x, "is an expression; the importer used %v", def)
		}
	}
	return def
}

// Note records an informational report line (does not lower the status).
func (c *MapContext) Note(format string, a ...interface{}) {
	c.issues = append(c.issues, Issue{Severity: SevInfo, Message: fmt.Sprintf(format, a...)})
}

// Warn records a parameter mono cannot honour; the node is partially mapped.
func (c *MapContext) Warn(param string, value interface{}, format string, a ...interface{}) {
	c.issues = append(c.issues, Issue{Severity: SevWarning, Param: param, Value: value, Message: fmt.Sprintf(format, a...)})
}

// Unsupported records that a parameter (with its value) has no mono
// equivalent.
func (c *MapContext) Unsupported(param string) {
	c.markUsed(param)
	v, _ := lookupPath(c.Params, param)
	c.Warn(param, v, "has no mono-agent equivalent")
}

// OperationUnsupported records that the node's resource/operation has no
// mono equivalent. When an API package (AppMapper) covers the node type,
// the converter uses it instead.
func (c *MapContext) OperationUnsupported(resource, operation string) Mapping {
	c.opUnsupported = true
	what := operation
	if resource != "" {
		what = resource + " / " + operation
	}
	c.issues = append(c.issues, Issue{Severity: SevError, Param: "operation", Value: what,
		Message: fmt.Sprintf("operation %q is not available in mono-agent yet", what)})
	return Mapping{Unmapped: true}
}

// Credential records that the node needs a connection on platform and
// returns the credential_id placeholder (the platform name, which mono
// resolves to the profile's connection for that platform). n8nTypes
// narrows which of the node's n8n credentials it stands for.
func (c *MapContext) Credential(platform string, n8nTypes ...string) string {
	use := credUse{Platform: platform}
	keys := make([]string, 0, len(c.Credentials))
	for k := range c.Credentials {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if len(n8nTypes) > 0 && !contains(n8nTypes, k) {
			continue
		}
		use.N8nType, use.N8nName = k, c.Credentials[k].Name
		break
	}
	c.creds = append(c.creds, use)
	return platform
}

// Secret records that the node needs a vault secret and returns the
// "@secret:<name>" reference mono resolves at run time. The name is
// derived from the n8n credential (or the node) plus purpose.
func (c *MapContext) Secret(purpose string, n8nTypes ...string) string {
	use := credUse{}
	for _, k := range sortedKeys(c.Credentials) {
		if len(n8nTypes) > 0 && !contains(n8nTypes, k) {
			continue
		}
		use.N8nType, use.N8nName = k, c.Credentials[k].Name
		break
	}
	base := use.N8nName
	if base == "" {
		base = c.NodeName
	}
	name := strings.Trim(strings.ToLower(nonIDChars.ReplaceAllString(base, "_")), "_-") + "_" + purpose
	use.Platform = "secret:" + name
	use.Hint = "monoagentcli secret add --kind secret --name " + name
	c.creds = append(c.creds, use)
	return "@secret:" + name
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// collectionKeys are parameters whose sub-keys are reported one by one
// when a mapper did not read them.
var collectionKeys = map[string]bool{
	"options": true, "additionalFields": true, "additionalOptions": true, "otherOptions": true,
	"filters": true, "updateFields": true, "additionalParameters": true,
	"otherProperties": true, "editFields": true,
}

// reportUnused lists set parameters no mapper read.
func (c *MapContext) reportUnused() {
	keys := make([]string, 0, len(c.Params))
	for k := range c.Params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := c.Params[k]
		if c.used[k] || isEmptyValue(v) {
			continue
		}
		if m, ok := v.(map[string]interface{}); ok && collectionKeys[k] {
			subs := make([]string, 0, len(m))
			for s := range m {
				subs = append(subs, s)
			}
			sort.Strings(subs)
			for _, s := range subs {
				if !c.used[k+"."+s] && !isEmptyValue(m[s]) {
					c.Warn(k+"."+s, m[s], "has no mono-agent equivalent")
				}
			}
			continue
		}
		c.Warn(k, v, "has no mono-agent equivalent")
	}
}

func isEmptyValue(v interface{}) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case map[string]interface{}:
		return len(x) == 0
	case []interface{}:
		return len(x) == 0
	}
	return false
}

// paramMap returns a parameter that is an object, or nil.
func (c *MapContext) paramMap(path string) map[string]interface{} {
	m, _ := c.Raw(path).(map[string]interface{})
	return m
}

// paramList returns a parameter that is an array of objects (n8n
// fixedCollection values), or nil.
func (c *MapContext) paramList(path string) []map[string]interface{} {
	raw, _ := c.Raw(path).([]interface{})
	out := make([]map[string]interface{}, 0, len(raw))
	for _, r := range raw {
		if m, ok := r.(map[string]interface{}); ok {
			out = append(out, m)
		}
	}
	return out
}

// version reports whether the node's typeVersion is at least v.
func (c *MapContext) version(v float64) bool { return c.TypeVersion >= v }
