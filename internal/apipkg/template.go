package apipkg

import (
	"encoding/json"
	"time"

	"fmt"
	"github.com/google/uuid"
	"strconv"
	"strings"
	"sync"
)

// Scope is what templates read: p (the op params), cred (the credential),
// item (the current input item's JSON), el (the element a $map is on) and
// res (the main request's decoded response, in "also" requests).
type Scope struct {
	P    map[string]any
	Cred map[string]any
	Item map[string]any
	El   any
	Res  any
	// Items is every input item's JSON, set for batch operations.
	Items []any
	// Pre is the decoded response of the op's (last) "before" request;
	// Pres holds every before step's response in order.
	Pre  any
	Pres []any
	// Bin describes the upload's binary property: file_name, mime_type,
	// file_extension (set when the op uploads a file).
	Bin map[string]any
	// Main is the main request's decoded response, unchanged while poll
	// repeats rebind res (Dropcontact: the id only the first reply has).
	Main any
	// Hdr is the main response's headers (first value each); lookups are
	// case-insensitive (hdr.Location, hdr.Resource-ID).
	Hdr headerMap
	// Idx is the element's position inside a $map / $entries.
	Idx int
	// Page holds the page params when pagination.param_in is "path"
	// ({{ page.pageNumber }}); Req describes the built request for
	// inject.hmac (method, path, query, body).
	Page map[string]any
	Req  map[string]any
	// Up is the enclosing element inside a nested $map (the outer el).
	Up any
	// Batch lists the chunk's items as {p, item, index} in a bulk request.
	Batch []any
	// Hook is the app webhook in a package's webhook block: url, path,
	// secret, id, workflow_id, node_id and extra (see webhook.go).
	Hook map[string]any
}

// headerMap holds response headers; lookupPath matches its keys
// case-insensitively.
type headerMap map[string]any

func (s Scope) root(name string) any {
	switch name {
	case "p":
		return s.P
	case "cred":
		return s.Cred
	case "item":
		return s.Item
	case "el":
		return s.El
	case "up":
		return s.Up
	case "res":
		return s.Res
	case "items":
		return s.Items
	case "pre":
		return s.Pre
	case "pres":
		return s.Pres
	case "bin":
		return s.Bin
	case "main":
		return s.Main
	case "hdr":
		return s.Hdr
	case "idx":
		return s.Idx
	case "page":
		return s.Page
	case "req":
		return s.Req
	case "batch":
		return s.Batch
	case "hook":
		return s.Hook
	case "now":
		return time.Now().UTC().Format(time.RFC3339)
	case "uuid":
		return uuid.NewString()
	}
	return nil
}

// tmpl is a parsed template string: literal text and {{ … }} expressions.
type tmpl struct {
	src    string
	parts  []tmplPart
	single bool // the whole string is exactly one expression
}

type tmplPart struct {
	lit  string
	expr *expr
}

// expr is "root.path | filter | filter:arg".
type expr struct {
	src     string
	root    string
	path    []string
	filters []filterCall
	// head is a parenthesised sub-expression the value starts from:
	// {{ (p.a | lower) | prefix:'x' }}.
	head *expr
}

type filterCall struct {
	name string
	arg  *filterArg
	// args holds every argument of a multi-argument filter (find,
	// find_row); arg is then args[0].
	args []*filterArg
}

// multiArgFilters take comma-separated arguments, passed to applyFilter
// as a []any.
var multiArgFilters = map[string][2]int{"find": {2, 2}, "find_row": {2, 2}, "row": {1, 2}, "replace": {2, 2}, "zip": {1, 3}, "set": {2, 2}}

type filterArg struct {
	lit  any   // a quoted string, number, bool or null
	path *expr // or a path (p.x, cred.y, item.z)
	sub  *expr // or a parenthesised expression: (p.sheetName | sheetgid)
}

// argValue evaluates a filter argument.
func (a *filterArg) argValue(sc Scope) (any, error) {
	switch {
	case a.sub != nil:
		return a.sub.eval(sc)
	case a.path != nil:
		return lookupPath(sc.root(a.path.root), a.path.path), nil
	}
	return a.lit, nil
}

var tmplCache sync.Map // string → *tmpl

// hasTemplate reports whether s contains template syntax.
func hasTemplate(s string) bool { return strings.Contains(s, "{{") || strings.Contains(s, `\}}`) }

// litUnescape turns the escapes \{{ and \}} of literal text into {{ and }}.
var litUnescape = strings.NewReplacer(`\{{`, "{{", `\}}`, "}}")

// parseTemplate parses s, caching the result.
func parseTemplate(s string) (*tmpl, error) {
	if t, ok := tmplCache.Load(s); ok {
		return t.(*tmpl), nil
	}
	t := &tmpl{src: s}
	rest := s
	lit := "" // literal text not yet added, escapes kept
	for {
		i := strings.Index(rest, "{{")
		if i > 0 && rest[i-1] == '\\' {
			// \{{ is a literal {{ (Zendesk placeholders in a body).
			lit += rest[:i+2]
			rest = rest[i+2:]
			continue
		}
		if i < 0 {
			if lit+rest != "" {
				t.parts = append(t.parts, tmplPart{lit: litUnescape.Replace(lit + rest)})
			}
			break
		}
		rest = lit + rest
		i += len(lit)
		lit = ""
		j := strings.Index(rest[i:], "}}")
		if j < 0 {
			return nil, fmt.Errorf("template %q: unclosed {{", s)
		}
		if i > 0 {
			t.parts = append(t.parts, tmplPart{lit: litUnescape.Replace(rest[:i])})
		}
		e, err := parseExpr(strings.TrimSpace(rest[i+2 : i+j]))
		if err != nil {
			return nil, fmt.Errorf("template %q: %w", s, err)
		}
		t.parts = append(t.parts, tmplPart{expr: e})
		rest = rest[i+j+2:]
	}
	t.single = len(t.parts) == 1 && t.parts[0].expr != nil
	tmplCache.Store(s, t)
	return t, nil
}

// parseExpr parses the inside of {{ }}.
func parseExpr(src string) (*expr, error) {
	segs := splitPipes(src)
	var e *expr
	if h := strings.TrimSpace(segs[0]); len(h) > 1 && h[0] == '(' && h[len(h)-1] == ')' {
		inner, err := parseExpr(strings.TrimSpace(h[1 : len(h)-1]))
		if err != nil {
			return nil, err
		}
		e = &expr{head: inner}
	} else {
		var err error
		if e, err = parsePath(h); err != nil {
			return nil, err
		}
	}
	e.src = src
	for _, f := range segs[1:] {
		f = strings.TrimSpace(f)
		name, argSrc, hasArg := strings.Cut(f, ":")
		name = strings.TrimSpace(name)
		if !knownFilters[name] {
			return nil, fmt.Errorf("unknown filter %q (see FORMAT.md §5 for the list)", name)
		}
		fc := filterCall{name: name}
		if n, multi := multiArgFilters[name]; multi {
			parts := splitArgs(argSrc)
			if !hasArg || len(parts) < n[0] || len(parts) > n[1] {
				return nil, fmt.Errorf("filter %s needs %d to %d comma-separated arguments", name, n[0], n[1])
			}
			for _, ps := range parts {
				a, err := parseArg(strings.TrimSpace(ps))
				if err != nil {
					return nil, fmt.Errorf("filter %s: %w", name, err)
				}
				fc.args = append(fc.args, a)
			}
			fc.arg = fc.args[0]
		} else if hasArg {
			a, err := parseArg(strings.TrimSpace(argSrc))
			if err != nil {
				return nil, fmt.Errorf("filter %s: %w", name, err)
			}
			fc.arg = a
		} else if name == "default" || name == "then" {
			return nil, fmt.Errorf("filter %s needs an argument, e.g. %s:\"x\"", name, name)
		}
		e.filters = append(e.filters, fc)
	}
	return e, nil
}

// splitPipes splits on | outside quotes.
func splitPipes(s string) []string {
	var out []string
	var quote rune
	start, depth := 0, 0
	for i, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '"' || r == '\'':
			quote = r
		case r == '(':
			depth++
		case r == ')':
			depth--
		case r == '|' && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func parsePath(s string) (*expr, error) {
	if s == "" {
		return nil, fmt.Errorf("empty expression")
	}
	segs := strings.Split(s, ".")
	switch segs[0] {
	case "p", "cred", "item", "el", "up", "res", "items", "pre", "pres", "bin", "main", "hdr", "uuid", "now", "idx", "page", "req", "batch", "hook":
	default:
		return nil, fmt.Errorf("expression %q must start with p., cred., item., items, el., up., res., main., hdr., pre., pres., bin., page., req., batch, hook. or be uuid / now / idx", s)
	}
	for _, sg := range segs[1:] {
		if sg == "" {
			return nil, fmt.Errorf("expression %q has an empty path segment", s)
		}
	}
	return &expr{src: s, root: segs[0], path: segs[1:]}, nil
}

func parseArg(s string) (*filterArg, error) {
	if s == "" {
		return nil, fmt.Errorf("empty argument")
	}
	if s[0] == '(' && s[len(s)-1] == ')' {
		e, err := parseExpr(strings.TrimSpace(s[1 : len(s)-1]))
		if err != nil {
			return nil, err
		}
		return &filterArg{sub: e}, nil
	}
	if (s[0] == '"' || s[0] == '\'') && len(s) >= 2 && s[len(s)-1] == s[0] {
		return &filterArg{lit: s[1 : len(s)-1]}, nil
	}
	switch s {
	case "true":
		return &filterArg{lit: true}, nil
	case "false":
		return &filterArg{lit: false}, nil
	case "null":
		return &filterArg{lit: nil}, nil
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return &filterArg{lit: f}, nil
	}
	p, err := parsePath(s)
	if err != nil {
		return nil, fmt.Errorf("argument %q is not a quoted string, number, bool or path", s)
	}
	return &filterArg{path: p}, nil
}

// eval evaluates the expression against sc.
func (e *expr) eval(sc Scope) (any, error) {
	v, _, err := e.evalLit(sc)
	return v, err
}

// evalLit also reports whether the value is a literal the package author
// wrote (a then/default argument), which path rendering does not escape.
func (e *expr) evalLit(sc Scope) (any, bool, error) {
	var v any
	if e.head != nil {
		hv, err := e.head.eval(sc)
		if err != nil {
			return nil, false, err
		}
		v = hv
	} else {
		v = lookupPath(sc.root(e.root), e.path)
	}
	literal := false
	for _, f := range e.filters {
		var arg any
		argLit := false
		if len(f.args) > 0 {
			vals := make([]any, len(f.args))
			for i, a := range f.args {
				v, err := a.argValue(sc)
				if err != nil {
					return nil, false, err
				}
				vals[i] = v
			}
			arg = vals
		} else if f.arg != nil && f.arg.sub != nil {
			var err error
			if arg, err = f.arg.sub.eval(sc); err != nil {
				return nil, false, err
			}
		} else if f.arg != nil {
			if f.arg.path != nil {
				arg = lookupPath(sc.root(f.arg.path.root), f.arg.path.path)
			} else {
				arg, argLit = f.arg.lit, true
			}
		}
		before := v
		var err error
		if v, err = applyFilter(f.name, v, arg); err != nil {
			return nil, false, fmt.Errorf("{{ %s }}: %w", e.src, err)
		}
		switch f.name {
		case "then":
			literal = argLit && !unset(before)
		case "default":
			if isEmpty(before) {
				literal = argLit
			}
		}
	}
	return v, literal, nil
}

// lookupPath walks maps by key and arrays by index (negative from the end).
func lookupPath(v any, path []string) any {
	for i := 0; i < len(path); i++ {
		seg := path[i]
		switch c := v.(type) {
		case headerMap:
			var hit any
			for k, x := range c {
				if strings.EqualFold(k, seg) {
					hit = x
					break
				}
			}
			v = hit
		case map[string]any:
			if x, ok := c[seg]; ok {
				v = x
				continue
			}
			// A key containing dots ("@odata.nextLink"): join the
			// following segments, longest existing key first.
			found := false
			for j := len(path); j > i+1; j-- {
				if x, ok := c[strings.Join(path[i:j], ".")]; ok {
					v, i, found = x, j-1, true
					break
				}
			}
			if !found {
				return nil
			}
		case []any:
			n, err := strconv.Atoi(seg)
			if err != nil {
				return nil
			}
			if n < 0 {
				n += len(c)
			}
			if n < 0 || n >= len(c) {
				return nil
			}
			v = c[n]
		default:
			return nil
		}
	}
	return v
}

// render evaluates the template. A single-expression template keeps the
// typed value; anything else is a string. escape, when set, is applied to
// each expression's text (path segments).
func (t *tmpl) render(sc Scope, escape func(string) string) (any, error) {
	if t.single && escape == nil {
		return t.parts[0].expr.eval(sc)
	}
	var b strings.Builder
	for _, p := range t.parts {
		if p.expr == nil {
			b.WriteString(p.lit)
			continue
		}
		v, literal, err := p.expr.evalLit(sc)
		if err != nil {
			return nil, err
		}
		s := stringify(v)
		if _, pre := v.(pathValue); escape != nil && !literal && !pre {
			s = escape(s)
		}
		b.WriteString(s)
	}
	return b.String(), nil
}

// renderString renders a template string (literal strings pass unchanged).
func renderString(s string, sc Scope) (any, error) {
	if !hasTemplate(s) {
		return s, nil
	}
	t, err := parseTemplate(s)
	if err != nil {
		return nil, err
	}
	return t.render(sc, nil)
}

// renderText renders s and stringifies the result.
func renderText(s string, sc Scope) (string, error) {
	v, err := renderString(s, sc)
	if err != nil {
		return "", err
	}
	return stringify(v), nil
}

// renderValue renders a JSON template value: objects drop empty keys, honour
// "!key" and "$merge"; arrays drop empty elements.
func renderValue(v any, sc Scope) (any, error) {
	switch t := v.(type) {
	case string:
		return renderString(t, sc)
	case map[string]any:
		if src, ok := t["$map"]; ok {
			return uniqueBuilt(t, sc)(renderMap(src, t["$to"], sc))
		}
		if parts, ok := t["$concat"]; ok {
			return uniqueBuilt(t, sc)(renderConcat(parts, sc))
		}
		if parts, ok := t["$list"]; ok {
			// Positional lists keep every element, empty ones included
			// (Odoo execute_kw args, Notion {"past_week": {}}).
			arr, ok := parts.([]any)
			if !ok {
				return nil, fmt.Errorf("$list must be a list")
			}
			out := make([]any, len(arr))
			for i, el := range arr {
				r, err := renderValue(el, sc)
				if err != nil {
					return nil, fmt.Errorf("$list element %d: %w", i, err)
				}
				if m, ok := r.(map[string]any); ok && m == nil {
					r = map[string]any{}
				}
				out[i] = r
			}
			return out, nil
		}
		if parts, ok := t["$join"]; ok {
			return renderJoin(parts, t, sc)
		}
		if inner, ok := t["$json"]; ok {
			r, err := renderValue(inner, sc)
			if err != nil {
				return nil, fmt.Errorf("$json: %w", err)
			}
			if isEmpty(r) {
				return nil, nil
			}
			b, err := json.Marshal(r)
			if err != nil {
				return nil, fmt.Errorf("$json: %w", err)
			}
			return string(b), nil
		}
		if src, ok := t["$entries"]; ok {
			return renderEntries(src, t["$key"], t["$value"], sc)
		}
		return renderObject(t, sc)
	case []any:
		out := make([]any, 0, len(t))
		for _, el := range t {
			r, err := renderValue(el, sc)
			if err != nil {
				return nil, err
			}
			if !isEmpty(r) {
				out = append(out, r)
			}
		}
		return out, nil
	default:
		return v, nil
	}
}

// renderObject renders an object template. $merge spreads object values
// into the parent first; explicit keys then override.
func renderObject(m map[string]any, sc Scope) (map[string]any, error) {
	// $when gates the whole object: empty/false → nil (omitted); otherwise
	// the object is kept even when every other key is empty, so marker
	// objects like Google Docs' "endOfSegmentLocation": {} survive.
	if wv, ok := m["$when"]; ok {
		w, err := renderValue(wv, sc)
		if err != nil {
			return nil, fmt.Errorf("$when: %w", err)
		}
		if !truthy(w) {
			return nil, nil
		}
	}
	out := map[string]any{}
	if mv, ok := m["$merge"]; ok {
		srcs := []any{mv}
		if arr, ok := mv.([]any); ok {
			srcs = arr
		}
		for _, src := range srcs {
			r, err := renderValue(src, sc)
			if err != nil {
				return nil, fmt.Errorf("$merge: %w", err)
			}
			if isEmpty(r) {
				continue
			}
			obj, ok := r.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("$merge: %v is %T, not an object", src, r)
			}
			forced := forcedKeys(src)
			for k, val := range obj {
				if !isEmpty(val) || forced[k] {
					out[k] = val
				}
			}
		}
	}
	for k, val := range m {
		if k == "$merge" || k == "$when" {
			continue
		}
		r, err := renderValue(val, sc)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", strings.TrimPrefix(k, "!"), err)
		}
		if strings.HasPrefix(k, "!") {
			if r == nil && isListBuilder(val) {
				r = []any{} // a forced list builder sends [] (ClickUp assignees.rem)
			}
			out[k[1:]] = r
			continue
		}
		if gated, ok := val.(map[string]any); ok {
			if _, has := gated["$when"]; has {
				if m, ok := r.(map[string]any); ok && m != nil {
					out[k] = m // a $when object that passed is kept even when empty
				}
				continue
			}
		}
		if isEmpty(r) {
			continue // an empty explicit key leaves a merged value in place
		}
		out[k] = r
	}
	return out, nil
}

// forcedKeys names the "!key" entries of a literal $merge source, so a forced
// empty value survives the spread like it does as a direct key.
func forcedKeys(src any) map[string]bool {
	m, ok := src.(map[string]any)
	if !ok {
		return nil
	}
	var forced map[string]bool
	for k := range m {
		if strings.HasPrefix(k, "!") {
			if forced == nil {
				forced = map[string]bool{}
			}
			forced[k[1:]] = true
		}
	}
	return forced
}

// isEmpty: nil, "", and empty arrays/objects are empty. 0 and false are not.
func isEmpty(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case map[string]any:
		return len(t) == 0
	case []any:
		return len(t) == 0
	case []string:
		return len(t) == 0
	}
	return false
}

// stringify formats a value for a URL, header or interpolated string.
func stringify(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case pathValue:
		return string(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(t), 'f', -1, 32)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case bool:
		return strconv.FormatBool(t)
	case json.Number:
		return t.String()
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

// renderMap renders "$to" once per element of the list "$map" resolves to,
// with el bound to the element. Empty results are dropped; an empty or
// missing list renders nil (so the key is omitted).
func renderMap(src, to any, sc Scope) (any, error) {
	if to == nil {
		return nil, fmt.Errorf("$map needs a $to template")
	}
	list, err := renderValue(src, sc)
	if err != nil {
		return nil, fmt.Errorf("$map: %w", err)
	}
	if isEmpty(list) {
		return nil, nil
	}
	arr, ok := list.([]any)
	if !ok {
		return nil, fmt.Errorf("$map: %v is %T, not a list", src, list)
	}
	out := make([]any, 0, len(arr))
	for i, el := range arr {
		esc := sc
		esc.El, esc.Idx, esc.Up = el, i, sc.El
		r, err := renderValue(to, esc)
		if err != nil {
			return nil, fmt.Errorf("$map element %d: %w", i, err)
		}
		if !isEmpty(r) {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// renderConcat renders each part and joins them into one list: list parts
// are spread, other non-empty values appended, empty parts skipped.
func renderConcat(parts any, sc Scope) (any, error) {
	arr, ok := parts.([]any)
	if !ok {
		return nil, fmt.Errorf("$concat must be a list of templates")
	}
	var out []any
	for i, part := range arr {
		r, err := renderValue(part, sc)
		if err != nil {
			return nil, fmt.Errorf("$concat part %d: %w", i, err)
		}
		switch t := r.(type) {
		case nil:
		case []any:
			out = append(out, t...)
		default:
			if !isEmpty(t) {
				out = append(out, t)
			}
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// splitArgs splits filter arguments on commas outside quotes.
func splitArgs(s string) []string {
	var out []string
	var q byte
	start, depth := 0, 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case q != 0:
			if c == q {
				q = 0
			}
		case c == '"' || c == '\'':
			q = c
		case c == '(':
			depth++
		case c == ')':
			depth--
		case c == ',' && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// renderEntries builds an object from a list: for each element (bound to
// el) "$key" renders the key and "$value" the value; empty keys or values
// are skipped. n8n filter rows → {"date[after]": 1704067200}.
func renderEntries(src, key, val any, sc Scope) (any, error) {
	ks, ok := key.(string)
	if !ok || ks == "" || val == nil {
		return nil, fmt.Errorf("$entries needs $key (a template) and $value")
	}
	list, err := renderValue(src, sc)
	if err != nil {
		return nil, fmt.Errorf("$entries: %w", err)
	}
	if isEmpty(list) {
		return nil, nil
	}
	arr, ok := list.([]any)
	if !ok {
		return nil, fmt.Errorf("$entries: %v is %T, not a list", src, list)
	}
	out := map[string]any{}
	for i, el := range arr {
		esc := sc
		esc.El, esc.Idx = el, i
		k, err := renderText(ks, esc)
		if err != nil {
			return nil, fmt.Errorf("$entries element %d key: %w", i, err)
		}
		v, err := renderValue(val, esc)
		if err != nil {
			return nil, fmt.Errorf("$entries element %d value: %w", i, err)
		}
		if k != "" && !isEmpty(v) {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// uniqueBuilt applies a list builder's "$unique" key to its result: true
// drops repeated elements, a path (text) compares objects by that value
// (SeaTable merges the row's assets with the uploaded ones).
func uniqueBuilt(m map[string]any, sc Scope) func(any, error) (any, error) {
	return func(v any, err error) (any, error) {
		u, ok := m["$unique"]
		if err != nil || !ok || v == nil {
			return v, err
		}
		if b, isBool := u.(bool); isBool {
			if !b {
				return v, nil
			}
			return uniqueList(v, nil)
		}
		path, err := renderText(stringify(u), sc)
		if err != nil {
			return nil, fmt.Errorf("$unique: %w", err)
		}
		return uniqueList(v, path)
	}
}

// isListBuilder reports whether a template value builds a list.
func isListBuilder(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	_, isMap := m["$map"]
	_, isConcat := m["$concat"]
	return isMap || isConcat
}

// renderJoin renders "$join" (a list of templates, or one template or
// builder yielding a list), drops empty parts, joins the rest with "$sep"
// and wraps them in "$prefix"/"$suffix". Nothing left → nil (omitted).
func renderJoin(parts any, m map[string]any, sc Scope) (any, error) {
	var vals []any
	if arr, ok := parts.([]any); ok {
		for i, p := range arr {
			r, err := renderValue(p, sc)
			if err != nil {
				return nil, fmt.Errorf("$join part %d: %w", i, err)
			}
			vals = append(vals, r)
		}
	} else {
		r, err := renderValue(parts, sc)
		if err != nil {
			return nil, fmt.Errorf("$join: %w", err)
		}
		if l, ok := r.([]any); ok {
			vals = l
		} else if r != nil {
			vals = []any{r}
		}
	}
	var keep []string
	for _, v := range vals {
		if s := stringify(v); !isEmpty(v) && s != "" {
			keep = append(keep, s)
		}
	}
	if len(keep) == 0 {
		return nil, nil
	}
	str := func(k string) string { s, _ := m[k].(string); return s }
	return str("$prefix") + strings.Join(keep, str("$sep")) + str("$suffix"), nil
}
