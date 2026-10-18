package apipkg

import (
	"encoding/json"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
)

// A request merges some collection params whole ("$merge": "{{
// p.updateFields }}"), so a key the collection doesn't declare goes out as
// is: a raw "article" under a Zammad ticket's updateFields, a "bcc_emails"
// under Freshservice's additionalFields. The send gate can't judge such a
// key by the op's sends annotation, so SendOf takes a config with one as a
// send: an agent's or an unapproved workflow's run waits for approval, and
// everyone else's runs as before (the request is what it always was).
// Raw JSON goes through the params an op declares for it (jsonParameters,
// additionalFieldsJson), which its annotation judges.

// mergeUse finds a param a template uses whole (p.updateFields or
// p.article.articleDetails, followed by a filter or the template's end),
// and the keys of a pick that lets only those through ("{{ p.editFields |
// pick:'title,body' }}").
var mergeUse = regexp.MustCompile(`p\.(\w+)(\.\w+)?\s*(?:\|\s*pick:'([^']*)'|\||\}\})`)

// merged is how o's requests use a param path: whole, or only the keys a
// pick names.
type merged struct {
	whole  bool
	picked map[string]bool
}

// sends reports whether a key k of the path goes out.
func (m *merged) sends(k string) bool { return m != nil && (m.whole || m.picked[k]) }

var mergeCache sync.Map // *Operation → map[string]*merged

// merges is how o's requests use its param paths ("updateFields",
// "article.articleDetails"); a path used whole anywhere is whole.
func (o *Operation) merges() map[string]*merged {
	if v, ok := mergeCache.Load(o); ok {
		return v.(map[string]*merged)
	}
	b, _ := json.Marshal([]any{o.Path, o.Query, o.Body, o.Headers, o.Before, o.Also, o.Bulk})
	out := map[string]*merged{}
	for _, m := range mergeUse.FindAllStringSubmatch(string(b), -1) {
		path := m[1] + m[2]
		u := out[path]
		if u == nil {
			u = &merged{picked: map[string]bool{}}
			out[path] = u
		}
		if !strings.Contains(m[0], "pick:") {
			u.whole = true
			continue
		}
		for _, k := range strings.Split(m[3], ",") {
			u.picked[strings.TrimSpace(k)] = true
		}
	}
	mergeCache.Store(o, out)
	return out
}

// undeclaredKey is a key config sends that its op doesn't declare: its
// name on the card's label and the param path that holds it; expr when that
// path is a collection or option given as an expression.
type undeclaredKey struct {
	name, path string
	expr       bool
}

// undeclared lists what config sends that o doesn't declare: a key of a
// collection its requests use whole (or pick) that isn't one of the
// collection's options, a key of an option object that isn't one of its
// subfields, or such a collection or option given as an expression (its
// keys are the run's). A key a pick leaves out never goes out.
func (o *Operation) undeclared(config map[string]any) []undeclaredKey {
	uses := o.merges()
	var out []undeclaredKey
	for _, f := range o.Fields {
		if f.Type != "collection" && f.Type != "fixedCollection" {
			continue
		}
		use := uses[f.Key]
		v := config[f.Key]
		if s, ok := v.(string); ok && hasTemplate(s) {
			if use != nil && (use.whole || o.picksUndeclared(use, f.Options)) || o.hasSubfields(f.Key) {
				out = append(out, undeclaredKey{f.Key + " (an expression)", f.Key, true})
			}
			continue
		}
		c, ok := v.(map[string]any)
		if !ok {
			continue
		}
		for k, ov := range c {
			if use.sends(k) && len(f.Options) > 0 && !slices.Contains(f.Options, k) {
				out = append(out, undeclaredKey{k, f.Key + "." + k, false})
				continue
			}
			sub, ok := o.Subfields[f.Key+"."+k]
			optUse := uses[f.Key+"."+k]
			if !ok || !use.sends(k) && optUse == nil {
				continue
			}
			if s, ok := ov.(string); ok && hasTemplate(s) {
				out = append(out, undeclaredKey{k + " (an expression)", f.Key + "." + k, true})
				continue
			}
			for _, obj := range objectsOf(ov) {
				for sk := range obj {
					if !slices.Contains(sub, sk) && (use.sends(k) || optUse.sends(sk)) {
						out = append(out, undeclaredKey{k + "." + sk, f.Key + "." + k, false})
					}
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return slices.Compact(out)
}

// picksUndeclared reports a pick that names a key options don't declare.
func (o *Operation) picksUndeclared(use *merged, options []string) bool {
	for k := range use.picked {
		if !slices.Contains(options, k) {
			return true
		}
	}
	return false
}

// hasSubfields reports an option of collection coll with declared subfields.
func (o *Operation) hasSubfields(coll string) bool {
	for k := range o.Subfields {
		if strings.HasPrefix(k, coll+".") {
			return true
		}
	}
	return false
}

// undeclaredSend is the send SendOf reports for keys o doesn't declare: the
// action of o's own sends annotation, or a post (nothing says what the keys
// do, and a post is addressed to no person), to a recipient the card names
// by those keys.
func (o *Operation) undeclaredSend(keys []undeclaredKey) *Sends {
	names := make([]string, len(keys))
	for i, k := range keys {
		names[i] = k.name
	}
	action := "post"
	if o.Sends != nil && !o.Sends.None {
		action = o.Sends.Action
	}
	return &Sends{Action: action, ToUnset: "whoever its undeclared fields (" + strings.Join(names, ", ") + ") reach"}
}

// undeclaredPaths is the param paths that hold keys, for the card to show,
// and those of them given as an expression.
func undeclaredPaths(keys []undeclaredKey) (paths, exprs []string) {
	for _, k := range keys {
		if !slices.Contains(paths, k.path) {
			paths = append(paths, k.path)
		}
		if k.expr {
			exprs = append(exprs, k.path)
		}
	}
	return paths, exprs
}

// opLabel names o of pkg the way the card says it: "trello card/update".
func (o *Operation) opLabel(pkg *Package) string {
	if o.Resource == "" {
		return pkg.ID + " " + o.Operation
	}
	return pkg.ID + " " + o.Resource + "/" + o.Operation
}

// objectsOf is v's objects: v itself, or the objects in a list.
func objectsOf(v any) []map[string]any {
	switch x := v.(type) {
	case map[string]any:
		return []map[string]any{x}
	case []any:
		var out []map[string]any
		for _, e := range x {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

// Undeclared lists what running nodeType with config would send that its
// op doesn't declare (see undeclared): for an app node, or a native node
// a package extends; nil for any other node type.
func Undeclared(nodeType string, config map[string]any) []string {
	if pkg, ok := ForNodeType(nodeType); ok {
		op, err := pkg.resolveOp(config)
		if err != nil {
			return nil
		}
		return keyNames(op.undeclared(config))
	}
	if pkg := extensionFor(nodeType); pkg != nil {
		if op, ok := pkg.Op(strings.TrimSpace(str(config["resource"])), strings.TrimSpace(str(config["operation"]))); ok {
			return keyNames(op.undeclared(config))
		}
	}
	return nil
}

func keyNames(keys []undeclaredKey) []string {
	var out []string
	for _, k := range keys {
		out = append(out, k.name)
	}
	return out
}
