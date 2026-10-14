package apipkg

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/monoes/mono-agent/internal/workflow"
)

// opRef names one op a schema field is shown for.
type opRef struct {
	Resource  string `json:"resource"`
	Operation string `json:"operation"`
}

// SchemaFor returns the node schema JSON of a package node type ("app.x"),
// or of a native node a package extends (the native schema file with the
// package's fields appended). false when no package answers for nodeType.
func SchemaFor(nodeType string) ([]byte, bool) {
	if p, ok := ForNodeType(nodeType); ok {
		return p.nodeSchema()
	}
	if extensionFor(nodeType) != nil {
		// The registered provider merges the package into the native schema.
		return workflow.ReadEmbeddedSchema(nodeType)
	}
	return nil, false
}

// schemaProvider is registered with workflow.RegisterSchemaProvider.
func schemaProvider(nodeType string, fileSchema []byte) ([]byte, bool) {
	if p, ok := ForNodeType(nodeType); ok {
		return p.nodeSchema()
	}
	if p := extensionFor(nodeType); p != nil {
		base := fileSchema
		if base == nil {
			base = []byte(`{"credential_platform":null,"fields":[]}`)
		}
		return p.extendSchema(base)
	}
	return nil, false
}

// nodeSchema builds a package node's schema: connection, resource,
// operation (one select per resource) and every op's fields.
func (p *Package) nodeSchema() ([]byte, bool) {
	var fields []map[string]any
	fields = append(fields, map[string]any{
		"key": "credential_id", "label": p.Title + " Connection", "type": "credential_picker",
		"required": p.needsCredential(),
	})
	fields = append(fields, p.selectorFields(false)...)
	fields = append(fields, p.opFields()...)
	doc := map[string]any{
		"title":               p.Title,
		"description":         p.Description,
		"category":            p.categoryOrApp(),
		"apipkg":              p.ID,
		"credential_platform": nil,
		"fields":              fields,
		"outputs":             p.schemaOutputs(false),
	}
	if p.DocsURL != "" {
		doc["docs_url"] = p.DocsURL
	}
	if pl := p.PrimaryPlatform(); pl != "" {
		doc["credential_platform"] = pl
	}
	return marshalSchema(doc)
}

// marshalSchema encodes a schema document without escaping <, > and &,
// so "<API response fields>" reads as written in `node schema`.
func marshalSchema(doc map[string]any) ([]byte, bool) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return nil, false
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), true
}

func (p *Package) categoryOrApp() string {
	if p.Category != "" {
		return p.Category
	}
	return "app"
}

// selectorFields are the resource select and the operation selects. For an
// extension the resource select starts with "" (the native operations).
func (p *Package) selectorFields(extension bool) []map[string]any {
	var out []map[string]any
	if len(p.Resources) == 0 {
		return []map[string]any{{
			"key": "operation", "label": "Operation", "type": "select", "required": true,
			"options": p.opNames(""), "default": p.DefaultOperation(""),
		}}
	}
	var vals []string
	if extension {
		vals = append(vals, "")
	}
	for _, r := range p.Resources {
		vals = append(vals, r.Value)
	}
	res := map[string]any{"key": "resource", "label": "Resource", "type": "select", "required": !extension, "options": vals}
	if extension {
		res["default"] = ""
		res["help"] = "Leave empty for the built-in operations; pick a resource for the " + p.Title + " API package operations."
	} else {
		res["default"] = p.DefaultResource()
	}
	out = append(out, res)
	for _, r := range p.Resources {
		out = append(out, map[string]any{
			"key": "operation", "label": r.Label + " Operation", "type": "select", "required": true,
			"options": p.opNames(r.Value), "default": p.DefaultOperation(r.Value),
			"depends_on": map[string]any{"key": "resource", "values": []string{r.Value}},
		})
	}
	return out
}

func (p *Package) opNames(resource string) []string {
	var out []string
	for _, o := range p.Operations {
		if o.Resource == resource {
			out = append(out, o.Operation)
		}
	}
	return out
}

// opFields merges every op's fields: one entry per (key, type, label, options),
// shown for the ops listed in visible_for. depends_on carries the operation
// names (the editor's single-key rule); visible_for is exact.
// opFields lists every op's fields. For an extension with resources the
// fields depend on the resource, not the operation name: native configs
// carry no resource, so a package op named like a native one ("reply")
// never imposes its required fields on them. visible_for stays exact.
func (p *Package) opFields(extension ...bool) []map[string]any {
	ext := len(extension) > 0 && extension[0] && len(p.Resources) > 0
	type group struct {
		field    workflow.NodeSchemaField
		required bool
		ops      []opRef
		opNames  []string
		resNames []string
	}
	var order []string
	groups := map[string]*group{}
	for _, o := range p.Operations {
		for _, f := range o.Fields {
			id := f.Key + "\x00" + f.Type + "\x00" + f.Label + "\x00" + strings.Join(f.Options, ",")
			if f.DependsOn != nil {
				id += "\x00" + f.DependsOn.Key + "=" + strings.Join(f.DependsOn.Values, ",")
			}
			g, ok := groups[id]
			if !ok {
				g = &group{field: f, required: f.Required}
				groups[id] = g
				order = append(order, id)
			}
			g.required = g.required && f.Required
			g.ops = append(g.ops, opRef{o.Resource, o.Operation})
			if !contains(g.opNames, o.Operation) {
				g.opNames = append(g.opNames, o.Operation)
			}
			if !contains(g.resNames, o.Resource) {
				g.resNames = append(g.resNames, o.Resource)
			}
		}
	}
	out := make([]map[string]any, 0, len(order))
	for _, id := range order {
		g := groups[id]
		f := schemaField(g.field)
		f.Required = g.required
		f.DependsOn = &workflow.FieldDependency{Key: "operation", Values: g.opNames}
		if ext {
			f.DependsOn = &workflow.FieldDependency{Key: "resource", Values: g.resNames}
			// A field some ops of the resource don't use can't be required
			// once it shows for the whole resource.
			f.Required = g.required && len(g.ops) == p.opsIn(g.resNames)
		}
		m := toMap(f)
		m["visible_for"] = visibleFor(g.ops, g.field.DependsOn)
		out = append(out, m)
	}
	return out
}

// visibleFor lists the configs a field shows for: its ops, each narrowed
// by the field's own depends_on (one entry per value), e.g. a field read
// only while a legacy toggle is on.
func visibleFor(ops []opRef, own *workflow.FieldDependency) any {
	if own == nil || own.Key == "" {
		return ops
	}
	var out []map[string]string
	for _, o := range ops {
		for _, v := range own.Values {
			out = append(out, map[string]string{"resource": o.Resource, "operation": o.Operation, own.Key: v})
		}
	}
	return out
}

// schemaField maps package field types to the editor's.
func schemaField(f workflow.NodeSchemaField) workflow.NodeSchemaField {
	switch f.Type {
	case "collection", "json":
		if f.Type == "collection" && len(f.Options) > 0 {
			keys := "Keys: " + strings.Join(f.Options, ", ")
			if f.Help != "" {
				keys = f.Help + " " + keys
			}
			f.Help = keys
			f.Options = nil
		}
		f.Type, f.Language = "code", "json"
	case "multiselect":
		f.Type = "array"
		if f.ItemType == "" {
			f.ItemType = "text"
		}
	case "datetime":
		f.Type = "text"
		if f.Placeholder == "" {
			f.Placeholder = "2026-01-31T09:00:00Z"
		}
	}
	return f
}

func toMap(f workflow.NodeSchemaField) map[string]any {
	b, _ := json.Marshal(f)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// extendSchema appends the package's selectors and fields to a native
// node's schema. Native fields stay first. Without resources, the package's
// operations are appended to the native "operation" select's options.
func (p *Package) extendSchema(base []byte) ([]byte, bool) {
	var doc map[string]any
	if err := json.Unmarshal(base, &doc); err != nil {
		return nil, false
	}
	fields, _ := doc["fields"].([]any)
	if len(p.Resources) == 0 {
		merged := false
		for _, f := range fields {
			m, ok := f.(map[string]any)
			if !ok || m["key"] != "operation" {
				continue
			}
			opts, _ := m["options"].([]any)
			for _, name := range p.opNames("") {
				if !containsAny(opts, name) {
					opts = append(opts, name)
				}
			}
			m["options"] = opts
			merged = true
		}
		if !merged {
			for _, f := range p.selectorFields(true) {
				fields = append(fields, f)
			}
		}
	} else {
		hideNativeFields(fields)
		for _, f := range p.selectorFields(true) {
			fields = append(fields, f)
		}
	}
	for _, f := range p.opFields(true) {
		fields = append(fields, f)
	}
	doc["fields"] = fields
	p.extendOutputs(doc)
	exts, _ := doc["extended_by"].([]any)
	doc["extended_by"] = append(exts, p.ID)
	return marshalSchema(doc)
}

// hideNativeFields limits the native fields to configs with no resource:
// a resource brings its own operation select and fields, and native ones
// beside them read as a second form (and would impose native required
// fields on package ops). The account picker stays shared. A field with no
// condition gets depends_on {resource: [""]}; one with depends_on on
// another key keeps it (the validator needs both to hold) and gets
// visible_for entries {resource: "", key: value} (the editor reads
// visible_for first); an existing visible_for gains resource "".
// Native configs carry no resource, so they validate as before.
func hideNativeFields(fields []any) {
	for _, f := range fields {
		m, ok := f.(map[string]any)
		if !ok || m["key"] == "resource" || m["type"] == "credential_picker" ||
			m["key"] == "credential_id" || m["key"] == "credential" {
			continue
		}
		if vf, ok := m["visible_for"].([]any); ok && len(vf) > 0 {
			for _, e := range vf {
				if em, ok := e.(map[string]any); ok {
					if _, has := em["resource"]; !has {
						em["resource"] = ""
					}
				}
			}
			continue
		}
		dep, _ := m["depends_on"].(map[string]any)
		if dep == nil {
			m["depends_on"] = map[string]any{"key": "resource", "values": []string{""}}
			continue
		}
		key, _ := dep["key"].(string)
		vals, _ := dep["values"].([]any)
		if key == "" || key == "resource" {
			continue
		}
		var vf []map[string]string
		for _, v := range vals {
			vf = append(vf, map[string]string{"resource": "", key: fmt.Sprint(v)})
		}
		m["visible_for"] = vf
	}
}

func containsAny(xs []any, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// sortedKeys returns m's keys sorted.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// opsIn counts the package's ops in the given resources.
func (p *Package) opsIn(resources []string) int {
	n := 0
	for _, o := range p.Operations {
		if contains(resources, o.Resource) {
			n++
		}
	}
	return n
}

// defaultRequiredSelects starts every required select whose default is
// empty (or not one of its options) on its first option. n8n leaves many
// of these at "", which the editor shows as an unanswered "— choose —"
// and the API rejects; a package that wants another start value sets it.
func (p *Package) defaultRequiredSelects() {
	for i := range p.Operations {
		for j := range p.Operations[i].Fields {
			f := &p.Operations[i].Fields[j]
			if f.Type != "select" || !f.Required || len(f.Options) == 0 {
				continue
			}
			if f.Default != nil {
				if s := fmt.Sprint(f.Default); contains(f.Options, s) || hasTemplate(s) {
					continue
				}
			}
			f.Default = f.Options[0]
		}
	}
}
