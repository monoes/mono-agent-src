package workflow

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed schemas/*.json
var embeddedSchemas embed.FS

// NodeSchemaField represents one configurable field in a node's schema.
type NodeSchemaField struct {
	Key         string      `json:"key"`
	Label       string      `json:"label"`
	Type        string      `json:"type"`
	Required    bool        `json:"required"`
	Default     interface{} `json:"default,omitempty"`
	Placeholder string      `json:"placeholder,omitempty"`
	// Examples are ready-made values the editor offers to insert. A field
	// whose shape is not obvious from its label — a list of image prompts,
	// say — is otherwise an empty box the user has to guess at.
	Examples  []string              `json:"examples,omitempty"`
	Help      string                `json:"help,omitempty"`
	Options   []string              `json:"options,omitempty"`
	Language  string                `json:"language,omitempty"`
	Rows      int                   `json:"rows,omitempty"`
	Min       *float64              `json:"min,omitempty"`
	Max       *float64              `json:"max,omitempty"`
	ItemType  string                `json:"item_type,omitempty"`
	Resource  *ResourcePickerConfig `json:"resource,omitempty"`
	DependsOn *FieldDependency      `json:"depends_on,omitempty"`
	// VisibleFor, when set, shows the field only for configs matching one
	// entry exactly (every key's value equal), e.g. an API package field
	// shown for {resource: message, operation: reply}.
	VisibleFor []map[string]string `json:"visible_for,omitempty"`
}

// ResourcePickerConfig configures a resource_picker field.
type ResourcePickerConfig struct {
	Type        string `json:"type"`
	CreateLabel string `json:"create_label,omitempty"`
	ParamField  string `json:"param_field,omitempty"`
}

// FieldDependency hides a field unless another field has one of the given values.
// Uses "key" (not "field") to reference the sibling field's key.
type FieldDependency struct {
	Key    string   `json:"key"`
	Values []string `json:"values"`
}

// NodeSchemaOutput is one top-level key of the items a node emits on its
// main output. Keys follow two conventions: "*" means the input item's
// fields are passed through, and "<name>" is a key named at run time —
// by the node's config field <name> when it has one, else by the data
// (an open key set).
type NodeSchemaOutput struct {
	Key         string `json:"key"`
	Type        string `json:"type,omitempty"`
	Description string `json:"description,omitempty"`
}

// NodeSchema is the schema embedded in each workflow node.
type NodeSchema struct {
	CredentialPlatform *string           `json:"credential_platform"`
	Fields             []NodeSchemaField `json:"fields"`
	// Outputs, when declared, lists the keys of the node's output items.
	Outputs []NodeSchemaOutput `json:"outputs,omitempty"`
	// SubInputs are the AI sub-node connections a root node takes (n8n's
	// ai_languageModel, ai_memory, ai_tool, …), drawn as ports on the
	// node's bottom edge in the editor.
	SubInputs []NodeSubInput `json:"sub_inputs,omitempty"`
	// SubOutput is the kind a sub-node attaches as (e.g. "ai_tool"): the
	// handle of its connection into a root node's matching sub-input.
	SubOutput string `json:"sub_output,omitempty"`
}

// NodeSubInput is one AI sub-node connection a node takes. Handle is the
// connection kind (also the connection's source and target handle), Max the
// most sub-nodes it takes (0 = no limit), Required whether the node needs one.
type NodeSubInput struct {
	Handle   string `json:"handle"`
	Label    string `json:"label"`
	Max      int    `json:"max,omitempty"`
	Required bool   `json:"required,omitempty"`
}

// legacyFormPlatforms are the platforms whose nodes without a schema file
// fall back to the shared action.<name>.json form, then browser.generic.json.
// This is the pre-package resolution, kept so built-in (and legacy
// local-<platform>) nodes keep exactly the forms they had.
var legacyFormPlatforms = map[string]bool{
	"instagram": true,
	"linkedin":  true,
	"x":         true,
	"tiktok":    true,
}

// LoadDefaultSchema returns the form schema for a node type:
//
//  1. its schema file (schemas/<type>.json);
//  2. for instagram/linkedin/x/tiktok nodes (built-in or legacy local-*),
//     the shared action-suffix file (e.g. linkedin.find_by_keyword →
//     schemas/action.find_by_keyword.json); for a legacy local-* action,
//     then browser.generic.json;
//  3. for any other automation action, the package's forms/<action>.json
//     (non-built-in packages), else a form generated from the action's
//     declared inputs and their ui hints.
//
// Returns an empty schema (no fields) when none of these applies.
func LoadDefaultSchema(nodeType string) (*NodeSchema, error) {
	data, ok := resolveSchemaJSON(nodeType)
	if !ok {
		empty := &NodeSchema{Fields: []NodeSchemaField{}}
		if p := sessionPlatform(nodeType); p != "" {
			empty.CredentialPlatform = &p
		}
		return empty, nil
	}
	var schema NodeSchema
	if err := json.Unmarshal(data, &schema); err != nil {
		return nil, fmt.Errorf("schema_loader: parse schemas/%s.json: %w", nodeType, err)
	}
	if schema.Fields == nil {
		schema.Fields = []NodeSchemaField{}
	}
	return &schema, nil
}

// resolveSchemaJSON is the schema JSON for nodeType (a schema file, else a
// generated form). A browser automation node's schema always names its
// session platform: shared and generated forms don't, so it is filled in
// here from the node type.
func resolveSchemaJSON(nodeType string) ([]byte, bool) {
	data, ok := schemaFile(nodeType)
	if pd, pok := providedSchema(nodeType, data); pok {
		data, ok = pd, true
	}
	if !ok {
		gen, ok := generateActionSchema(nodeType)
		if !ok {
			return nil, false
		}
		var err error
		if data, err = json.Marshal(gen); err != nil {
			return nil, false
		}
	}
	return withSessionPlatform(nodeType, data), true
}

// withSessionPlatform sets credential_platform on a browser automation
// node's schema when the schema leaves it unset. Other keys are kept as is.
func withSessionPlatform(nodeType string, data []byte) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(data, &m) != nil {
		return data
	}
	if cp, ok := m["credential_platform"]; ok && string(cp) != "null" && string(cp) != `""` {
		return data
	}
	p := sessionPlatform(nodeType)
	if p == "" {
		return data
	}
	m["credential_platform"], _ = json.Marshal(p)
	out, err := json.Marshal(m)
	if err != nil {
		return data
	}
	return out
}

// schemaFile returns the explicit schema file for nodeType (steps 1 and 2
// of LoadDefaultSchema).
func schemaFile(nodeType string) ([]byte, bool) {
	if data, err := embeddedSchemas.ReadFile("schemas/" + nodeType + ".json"); err == nil {
		return data, true
	}
	dot := strings.Index(nodeType, ".")
	if dot <= 0 || !legacyFormPlatforms[strings.TrimPrefix(nodeType[:dot], "local-")] {
		return nil, false
	}
	if data, err := embeddedSchemas.ReadFile("schemas/action." + nodeType[dot+1:] + ".json"); err == nil {
		return data, true
	}
	// A built-in action is described by its own inputs (the generated form);
	// the catch-all generic form is only for legacy local-* actions.
	if !strings.HasPrefix(nodeType, "local-") {
		return nil, false
	}
	if data, err := embeddedSchemas.ReadFile("schemas/browser.generic.json"); err == nil {
		return data, true
	}
	return nil, false
}

// ListEmbeddedSchemas returns all node type names that have an embedded schema.
func ListEmbeddedSchemas() []string {
	entries, err := embeddedSchemas.ReadDir("schemas")
	if err != nil {
		return nil
	}
	types := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".json") {
			types = append(types, strings.TrimSuffix(name, ".json"))
		}
	}
	return types
}
