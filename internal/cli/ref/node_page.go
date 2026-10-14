package ref

import (
	"fmt"
	"sort"
	"strings"

	"github.com/monoes/mono-agent/internal/workflow"
)

// NodeRefField is one config field as `ref node` documents it, taken from
// the node type's schema.
type NodeRefField struct {
	Key         string      `json:"key"`
	Label       string      `json:"label,omitempty"`
	Type        string      `json:"type,omitempty"`
	Required    bool        `json:"required"`
	Default     interface{} `json:"default,omitempty"`
	Options     []string    `json:"options,omitempty"`
	Placeholder string      `json:"placeholder,omitempty"`
	Help        string      `json:"help,omitempty"`
	// ShownWhen is set for a field that depends on a key other than the
	// operation selector (operation-dependent fields are listed under their
	// operations instead).
	ShownWhen *workflow.FieldDependency `json:"shown_when,omitempty"`
}

// NodeRefOperation is one value of the operation selector and the fields
// the schema shows for it.
type NodeRefOperation struct {
	Name    string         `json:"name"`
	Default bool           `json:"default,omitempty"`
	Fields  []NodeRefField `json:"fields"`
}

// NodeRefPage is everything `ref node <type>` prints: the schema-derived
// reference (credential platform, fields, operations) with the hand-written
// prose from the node catalogue layered on top when the type has an entry.
type NodeRefPage struct {
	Type            string `json:"type"`
	Category        string `json:"category"`
	Registered      bool   `json:"registered"`
	HandWritten     bool   `json:"hand_written"`
	Deprecated      bool   `json:"deprecated,omitempty"`
	DeprecationHint string `json:"deprecation_hint,omitempty"`

	Short         string `json:"short,omitempty"`
	Description   string `json:"description,omitempty"`
	ConfigExample string `json:"config_example,omitempty"`
	Inputs        string `json:"inputs,omitempty"`
	Outputs       string `json:"outputs,omitempty"`
	Notes         string `json:"notes,omitempty"`

	CredentialPlatform string `json:"credential_platform,omitempty"`
	// OperationKey is the selector field the operations hang off (usually
	// "operation"); empty when the schema has none.
	OperationKey string             `json:"operation_key,omitempty"`
	Fields       []NodeRefField     `json:"fields"`
	Operations   []NodeRefOperation `json:"operations,omitempty"`
}

// NodeTypes is the set of node types the manual covers: every type the
// node registry builds (the same registry `node run` uses), plus the
// hand-documented triggers, which run through the trigger manager rather
// than the registry. Hand entries for types this build does not register
// (e.g. social nodes in a nosocial build) are left out.
func (d Deps) NodeTypes(reg *workflow.NodeTypeRegistry) []string {
	seen := map[string]bool{}
	var types []string
	for _, t := range reg.Types() {
		seen[t] = true
		types = append(types, t)
	}
	for _, doc := range d.NodeDocs() {
		if !seen[doc.Type] && strings.HasPrefix(doc.Type, "trigger.") {
			seen[doc.Type] = true
			types = append(types, doc.Type)
		}
	}
	sort.Strings(types)
	return types
}

// BuildNodeRefPage assembles the page for typ. Legacy aliases (e.g. "slack")
// resolve to their canonical type. It fails only for a type that is neither
// registered nor hand-documented.
func (d Deps) BuildNodeRefPage(reg *workflow.NodeTypeRegistry, typ string) (*NodeRefPage, error) {
	if canonical, ok := reg.Aliases()[typ]; ok {
		typ = canonical
	}
	doc := d.FindNodeDoc(typ)
	if doc != nil {
		typ = doc.Type // canonical casing
	}
	registered := reg.Has(typ)
	if doc == nil && !registered {
		return nil, fmt.Errorf("unknown node type %q — run 'monoagentcli ref nodes --all' to see all types", typ)
	}

	schema, err := workflow.LoadDefaultSchema(typ)
	if err != nil {
		return nil, err
	}
	page := &NodeRefPage{Type: typ, Category: d.nodeRefCategory(typ), Registered: registered}
	if hint, ok := workflow.IsDeprecatedNodeType(typ); ok {
		page.Deprecated, page.DeprecationHint = true, hint
	}
	if doc != nil {
		page.HandWritten = true
		page.Short, page.Description, page.ConfigExample = doc.Short, doc.Description, doc.Config
		page.Inputs, page.Outputs, page.Notes = doc.Inputs, doc.Outputs, doc.Notes
	} else {
		page.Short = workflow.SchemaTitle(typ)
	}
	if schema.CredentialPlatform != nil {
		page.CredentialPlatform = *schema.CredentialPlatform
	}
	page.OperationKey, page.Fields, page.Operations = splitByOperation(schema.Fields)
	return page, nil
}

// nodeRefCategory is the hand entry's category, else the type's prefix.
func (d Deps) nodeRefCategory(typ string) string {
	if doc := d.FindNodeDoc(typ); doc != nil && doc.Category != "" {
		return doc.Category
	}
	prefix, _, _ := strings.Cut(typ, ".")
	return prefix
}

// operationSelector picks the field the operations hang off: "operation"
// when it is a select, else the first select some other field depends on.
func operationSelector(fields []workflow.NodeSchemaField) *workflow.NodeSchemaField {
	dependedOn := map[string]bool{}
	for _, f := range fields {
		if f.DependsOn != nil {
			dependedOn[f.DependsOn.Key] = true
		}
	}
	var fallback *workflow.NodeSchemaField
	for i := range fields {
		f := &fields[i]
		if len(f.Options) == 0 {
			continue
		}
		if f.Key == "operation" {
			return f
		}
		if fallback == nil && dependedOn[f.Key] {
			fallback = f
		}
	}
	return fallback
}

// splitByOperation divides schema fields into the ones shown for every
// operation and, per operation, the ones whose depends_on names it.
func splitByOperation(fields []workflow.NodeSchemaField) (string, []NodeRefField, []NodeRefOperation) {
	sel := operationSelector(fields)
	common := []NodeRefField{}
	if sel == nil {
		for _, f := range fields {
			common = append(common, toRefField(f))
		}
		return "", common, nil
	}
	ops := make([]NodeRefOperation, len(sel.Options))
	index := map[string]int{}
	for i, o := range sel.Options {
		ops[i] = NodeRefOperation{Name: o, Default: fmt.Sprint(sel.Default) == o, Fields: []NodeRefField{}}
		index[o] = i
	}
	for _, f := range fields {
		if f.DependsOn == nil || f.DependsOn.Key != sel.Key {
			common = append(common, toRefField(f))
			continue
		}
		for _, v := range f.DependsOn.Values {
			if i, ok := index[v]; ok {
				rf := toRefField(f)
				rf.ShownWhen = nil // implied by the operation it is listed under
				ops[i].Fields = append(ops[i].Fields, rf)
			}
		}
	}
	return sel.Key, common, ops
}

func toRefField(f workflow.NodeSchemaField) NodeRefField {
	return NodeRefField{
		Key: f.Key, Label: f.Label, Type: f.Type, Required: f.Required,
		Default: f.Default, Options: f.Options, Placeholder: f.Placeholder,
		Help: f.Help, ShownWhen: f.DependsOn,
	}
}
