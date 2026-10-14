package workflow

import (
	"fmt"
	"sort"
	"strings"
)

// Issue codes reported by ValidateWorkflow.
const (
	IssueInvalidWorkflow      = "invalid_workflow"
	IssueUnknownNodeType      = "unknown_node_type"
	IssueMissingRequired      = "missing_required"
	IssueInvalidOption        = "invalid_option"
	IssueUnknownField         = "unknown_field"
	IssueDanglingNodeRef      = "dangling_node_ref"
	IssueBadExpression        = "bad_expression"
	IssueUnknownUpstreamField = "unknown_upstream_field"
	IssueNeedsSetup           = "needs_setup"
)

// ValidationIssue is one finding of ValidateWorkflow.
type ValidationIssue struct {
	NodeID   string `json:"node_id,omitempty"`
	Field    string `json:"field,omitempty"`
	Code     string `json:"code"`
	Severity string `json:"severity"` // "error" or "warning"
	Message  string `json:"message"`
	Hint     string `json:"hint,omitempty"`
}

// ValidationReport is the result of ValidateWorkflow. Errors and Warnings
// are the issues' messages as plain strings (the shape `workflow validate`
// always returned); Issues carries them structured.
type ValidationReport struct {
	Valid    bool              `json:"valid"`
	Errors   []string          `json:"errors"`
	Warnings []string          `json:"warnings"`
	Issues   []ValidationIssue `json:"issues"`
}

// ValidateOptions configures ValidateWorkflow.
type ValidateOptions struct {
	// KnownNodeType reports whether a node type has an executor (the node
	// registry's Has). Nil skips the unknown-node-type check for types
	// without a schema file.
	KnownNodeType func(nodeType string) bool
}

// genericConfigKeys are config keys the engine (or every node) reads
// whatever the node type: execution.go's on_error, retry_policy,
// node_timeout and credential_id (plus the credential/username it injects), the profile
// a node runs under, and editor metadata.
var genericConfigKeys = map[string]bool{
	"on_error": true, "retry_policy": true, "credential_id": true,
	"credential": true, "username": true, "profile_id": true,
	"continue_on_fail": true, "timeout": true, "timeout_seconds": true,
	"notes": true, "disabled": true, "description": true, "label": true,
	"always_output_data": true, "execute_once": true, "executeOnce": true,
	"max_tries": true, "wait_between_tries": true,
	"execute_once_per_item": true, "node_timeout": true,
}

func isGenericConfigKey(k string) bool {
	return genericConfigKeys[k] || strings.HasPrefix(k, "retry") || strings.HasPrefix(k, "_")
}

// ValidateWorkflow runs the activation rules (ValidateForActivation) and,
// on top, schema-aware checks: node types, required fields, select
// options, unknown config keys, node references and expression syntax in
// config values, and $json fields an upstream node does not declare. It
// never changes what save or activation accept.
func ValidateWorkflow(w *Workflow, opts ValidateOptions) ValidationReport {
	r := ValidationReport{Errors: []string{}, Warnings: []string{}, Issues: []ValidationIssue{}}
	if err := ValidateForActivation(w); err != nil {
		r.Errors = append(r.Errors, err.Error())
		r.Issues = append(r.Issues, ValidationIssue{Code: IssueInvalidWorkflow, Severity: "error", Message: err.Error()})
	}
	for _, is := range append(schemaIssues(w, opts), settingsIssues(w)...) {
		r.Issues = append(r.Issues, is)
		msg := is.Message
		if is.NodeID != "" {
			msg = fmt.Sprintf("node %q: %s", is.NodeID, msg)
		}
		if is.Severity == "error" {
			r.Errors = append(r.Errors, msg)
		} else {
			r.Warnings = append(r.Warnings, msg)
		}
	}
	r.Valid = len(r.Errors) == 0
	return r
}

// checkedSchema is the schema whose fields validate a node's config: only
// an exact schemas/<type>.json. Browser automation nodes (shared or
// generated forms over one dynamic config surface) are skipped.
func checkedSchema(nodeType string) (*NodeSchema, bool) {
	if dot := strings.Index(nodeType, "."); dot > 0 {
		p := nodeType[:dot]
		if officialAutomations[p] || legacyFormPlatforms[p] || strings.HasPrefix(p, "local-") || nodeType == "browser.generic" {
			return nil, false
		}
	}
	if _, ok := embeddedSchemaFile(nodeType); !ok {
		return nil, false
	}
	s, err := LoadDefaultSchema(nodeType)
	if err != nil || s == nil {
		return nil, false
	}
	return s, true
}

func embeddedSchemaFile(nodeType string) ([]byte, bool) {
	data, err := embeddedSchemas.ReadFile("schemas/" + nodeType + ".json")
	return data, err == nil
}

func schemaIssues(w *Workflow, opts ValidateOptions) []ValidationIssue {
	var out []ValidationIssue
	nodes := make([]WorkflowNode, len(w.Nodes))
	refNames := map[string]bool{}
	for i, n := range w.Nodes {
		if n.Config == nil && n.ConfigRaw != "" {
			_ = n.ParseConfig()
		}
		nodes[i] = n
		refNames[n.ID] = true
		if n.Name != "" {
			refNames[n.Name] = true
		}
	}
	g := newOutputGraph(nodes, w.Connections)

	for _, n := range nodes {
		if n.Type == "" {
			continue
		}
		if _, deprecated := IsDeprecatedNodeType(n.Type); deprecated {
			continue // ValidateForSave reports it
		}
		_, hasFile := embeddedSchemaFile(n.Type)
		if !hasFile && opts.KnownNodeType != nil && !opts.KnownNodeType(n.Type) {
			out = append(out, ValidationIssue{
				NodeID: n.ID, Code: IssueUnknownNodeType, Severity: "error",
				Message: fmt.Sprintf("unknown node type %q", n.Type),
				Hint:    "run `monoagentcli node list`; an automation it needs may not be installed",
			})
			continue
		}
		schema, ok := checkedSchema(n.Type)
		if ok {
			out = append(out, fieldIssues(n, schema)...)
		}
		var fieldTypes map[string]string
		if ok {
			fieldTypes = map[string]string{}
			for _, f := range schema.Fields {
				fieldTypes[f.Key] = f.Type
			}
		}
		keys := make([]string, 0, len(n.Config))
		for k := range n.Config {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			walkStrings(n.Config[k], k, func(path, s string) {
				// Code without "{{" runs as written: its "}}" closes nested
				// JavaScript blocks, not a template.
				if fieldTypes[k] != "code" || strings.Contains(s, "{{") {
					out = append(out, expressionIssues(n.ID, path, s, refNames)...)
				}
				out = append(out, g.upstreamFieldIssues(n, path, s)...)
			})
		}
	}
	return out
}

// isEmptyValue reports whether a config value counts as not set.
func isEmptyValue(v interface{}) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(t) == ""
	case []interface{}:
		return len(t) == 0
	case map[string]interface{}:
		return len(t) == 0
	}
	return false
}

func isExpression(v interface{}) bool {
	s, ok := v.(string)
	return ok && strings.Contains(s, "{{")
}

func fieldIssues(n WorkflowNode, s *NodeSchema) []ValidationIssue {
	var out []ValidationIssue
	known := map[string]bool{}
	for _, f := range s.Fields {
		known[f.Key] = true
		if f.Resource != nil && f.Resource.ParamField != "" {
			known[f.Resource.ParamField] = true
		}
	}
	reported := map[string]bool{}
	for i := range s.Fields {
		f := &s.Fields[i]
		v, present := n.Config[f.Key]
		if f.Required && (!present || isEmptyValue(v)) && f.Default == nil && !reported[f.Key] {
			if applies, ok := fieldApplies(n, s, f); applies && ok {
				reported[f.Key] = true
				is := ValidationIssue{
					NodeID: n.ID, Field: f.Key, Code: IssueMissingRequired, Severity: "error",
					Message: fmt.Sprintf("required field %q is missing", f.Key),
					Hint:    fmt.Sprintf("set config.%s (see `monoagentcli node schema %s`)", f.Key, n.Type),
				}
				// An account is bound per machine: a shared workflow or a
				// template leaves it for the user to pick at install/setup.
				if isCredentialField(f) {
					is.Code = IssueNeedsSetup
					is.Severity = "warning"
					is.Message = fmt.Sprintf("needs setup: no account chosen for %q", f.Key)
					is.Hint = fmt.Sprintf("pick a connected account for config.%s before running (`monoagentcli connect for-node %s`)", f.Key, n.Type)
				}
				out = append(out, is)
			}
		}
	}
	out = append(out, optionIssues(n, s)...)
	keys := make([]string, 0, len(n.Config))
	for k := range n.Config {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if known[k] || isGenericConfigKey(k) {
			continue
		}
		hint := ""
		if best := closestKey(k, s.Fields); best != "" {
			hint = fmt.Sprintf("did you mean %q?", best)
		} else if len(s.Fields) > 0 {
			names := make([]string, 0, len(s.Fields))
			for _, f := range s.Fields {
				names = append(names, f.Key)
			}
			hint = "known fields: " + strings.Join(names, ", ")
		}
		out = append(out, ValidationIssue{
			NodeID: n.ID, Field: k, Code: IssueUnknownField, Severity: "warning",
			Message: fmt.Sprintf("config key %q is not a field of %s", k, n.Type),
			Hint:    hint,
		})
	}
	return out
}

// isCredentialField reports whether f picks the account a node runs as.
func isCredentialField(f *NodeSchemaField) bool {
	return f.Type == "credential_picker" || f.Key == "credential_id" || f.Key == "credential"
}

// optionIssues checks select values. A schema may declare one field per key
// for each value of a controlling field (n8n-style resource/operation
// lists), so a value is checked only against the fields that apply
// (depends_on and visible_for hold), and it is valid when any of them offers it; one issue per key.
func optionIssues(n WorkflowNode, s *NodeSchema) []ValidationIssue {
	type check struct {
		options []string
		ok      bool
	}
	byKey := map[string]*check{}
	var order []string
	for i := range s.Fields {
		f := &s.Fields[i]
		if f.Type != "select" || len(f.Options) == 0 {
			continue
		}
		v, present := n.Config[f.Key]
		if !present || isEmptyValue(v) || isExpression(v) {
			continue
		}
		if applies, known := fieldApplies(n, s, f); !applies || !known {
			continue
		}
		c := byKey[f.Key]
		if c == nil {
			c = &check{}
			byKey[f.Key] = c
			order = append(order, f.Key)
		}
		sv := fmt.Sprint(v)
		for _, o := range f.Options {
			if strings.EqualFold(sv, o) {
				c.ok = true
			}
		}
		c.options = append(c.options, f.Options...)
	}
	var out []ValidationIssue
	for _, k := range order {
		c := byKey[k]
		if c.ok {
			continue
		}
		out = append(out, ValidationIssue{
			NodeID: n.ID, Field: k, Code: IssueInvalidOption, Severity: "error",
			Message: fmt.Sprintf("field %q: %q is not one of its options", k, fmt.Sprint(n.Config[k])),
			Hint:    "use one of: " + strings.Join(dedupeStrings(c.options), ", "),
		})
	}
	return out
}
