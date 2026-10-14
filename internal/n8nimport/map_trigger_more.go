package n8nimport

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Core n8n triggers beyond schedules and webhooks: the n8n Form, the MCP
// server trigger, the evaluation dataset trigger, and the trigger types
// that import as placeholders (triggerPlaceholders).

func init() {
	Register("n8n-nodes-base.formTrigger", mapFormTrigger)
	Register("n8n-nodes-base.n8nTrigger", mapWorkflowTrigger)
	Register("n8n-nodes-base.evaluationTrigger", mapEvaluationTrigger)
	Register("@n8n/n8n-nodes-langchain.mcpTrigger", mapMCPTrigger)
	for t, reason := range triggerPlaceholders {
		reason := reason
		Register(t, func(c *MapContext) Mapping {
			for k := range c.Params {
				c.Ignore(k)
			}
			c.issues = append(c.issues, Issue{Severity: SevError,
				Message: fmt.Sprintf("n8n trigger %s cannot be imported: %s; start the workflow with a schedule or webhook trigger instead", c.Type, reason)})
			return Mapping{Unmapped: true}
		})
	}
}

// triggerPlaceholders are the n8n trigger types with no runnable mono
// construct, with the reason the report gives.
var triggerPlaceholders = map[string]string{
	"n8n-nodes-base.sseTrigger":            "mono-agent has no Server-Sent Events trigger that keeps a stream open",
	"n8n-nodes-base.e2eTestPollingTrigger": "it is n8n's internal end-to-end test node",
	"@n8n/n8n-nodes-langchain.microsoftAgent365Trigger": "Microsoft Agent 365 activities (Bot Framework requests with token " +
		"validation, answered inside the conversation) have no mono-agent equivalent",
}

// TriggerPlaceholder returns why an n8n trigger type imports as a
// placeholder, or "" when it imports to a runnable trigger.
func TriggerPlaceholder(n8nType string) string { return triggerPlaceholders[n8nType] }

// formField is one field of an n8n Form Trigger, as the shaping step reads it.
type formField struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Multi    bool   `json:"multi,omitempty"`
	Element  string `json:"element,omitempty"`
	Required bool   `json:"-"`
}

// formFields reads the form's fields (the field list, or the JSON
// definition of defineForm "json").
func (c *MapContext) formFields() []formField {
	var raw []map[string]interface{}
	if c.Literal("defineForm", "fields") == "json" {
		text, _ := c.Raw("jsonOutput").(string)
		if strings.HasPrefix(text, "=") {
			c.Warn("jsonOutput", text, "a form defined by an expression cannot be read; the fields arrive as posted")
			return nil
		}
		if err := json.Unmarshal([]byte(text), &raw); err != nil {
			c.Warn("jsonOutput", text, "is not a JSON list of fields; the fields arrive as posted")
			return nil
		}
	} else {
		raw = c.paramList("formFields.values")
	}
	fields := make([]formField, 0, len(raw))
	for _, f := range raw {
		label, _ := f["fieldLabel"].(string)
		name, _ := f["fieldName"].(string)
		id := label
		if (c.version(2.4) && name != "") || id == "" {
			id = name
		}
		typ, _ := f["fieldType"].(string)
		if typ == "" {
			typ = "text"
		}
		multi, _ := f["multiselect"].(bool)
		req, _ := f["requiredField"].(bool)
		el, _ := f["elementName"].(string)
		fields = append(fields, formField{Name: id, Type: typ, Multi: multi, Element: el, Required: req})
	}
	return fields
}

// mapFormTrigger: the form's submissions arrive at a trigger.webhook on
// the form's path; a code step names the answers the way n8n did
// (field-<n> → the field's label or name) and adds submittedAt and
// formMode. mono-agent does not serve the form page itself.
func mapFormTrigger(c *MapContext) Mapping {
	path := strings.Trim(c.Literal("path", ""), "/")
	if path == "" {
		path = strings.Trim(c.Literal("options.path", ""), "/")
	}
	if path == "" {
		path = webhookPath(c)
	}
	cfg := map[string]interface{}{"path": path, "method": "POST"}
	switch mode := c.Literal("responseMode", "onReceived"); mode {
	case "onReceived":
	case "lastNode":
		cfg["response_mode"], cfg["response_data"] = "lastNode", "noData"
	case "responseNode":
		cfg["response_mode"] = "responseNode"
	default:
		c.Warn("responseMode", mode, "is not known; the submission is answered at once")
	}
	switch auth := c.Literal("authentication", "none"); auth {
	case "none":
	default:
		c.Warn("authentication", auth, "mono-agent webhooks check a header token, not %s: set auth_header/auth_token, or the form accepts any caller", auth)
	}
	fields := c.formFields()
	var files, required []string
	for _, f := range fields {
		if f.Type == "file" {
			files = append(files, f.Name)
		}
		if f.Required {
			required = append(required, f.Name)
		}
	}
	c.Ignore("formTitle", "formDescription", "formFields", "defineForm", "jsonOutput", "options", "formNotice",
		"respondWithOptions", "requireExecuteAccess")
	c.Warn("form", nil, "mono-agent does not serve the form page: post the answers to https://<your mono-agent host>/webhook/%s "+
		"(form, multipart or JSON) as field-0, field-1, … in field order, or under each field's name", path)
	if len(required) > 0 {
		c.Note("required fields (%s) are not checked on submission", strings.Join(required, ", "))
	}
	if len(files) > 0 {
		c.Note("uploaded files (%s) arrive as the item's binary data", strings.Join(files, ", "))
	}
	spec, _ := json.Marshal(fields)
	body := fmt.Sprintf(`var fields = %s;
return items.map(function (it) {
  var body = it.json.body !== null && typeof it.json.body === "object" ? it.json.body : {};
  var out = {};
  if (!fields.length) { out = Object.assign({}, body); }
  fields.forEach(function (f, i) {
    var v = body["field-" + i];
    if (v === undefined) { v = body[f.name]; }
    if (v === undefined || v === null) { if (f.type !== "html") { out[f.name] = null; } return; }
    if (f.type === "html") { if (f.element) { out[f.element] = v; } return; }
    if (f.type === "number") { v = Number(v); }
    if (f.type === "text" || f.type === "email") { v = String(v).trim(); }
    if ((f.multi || f.type === "checkbox" || f.type === "radio") && typeof v === "string") {
      try { v = JSON.parse(v); } catch (e) {}
      if (f.type === "radio" && Array.isArray(v)) { v = v[0]; }
    }
    out[f.name] = v;
  });
  out.submittedAt = new Date().toISOString();
  out.formMode = "production";
  return out;
});
`, spec)
	return Mapping{Type: "trigger.webhook", Config: cfg, EntrySuffix: " (receive)",
		Chain: []ChainStep{{Type: "core.code", Config: codeConfig(wrapCode(body))}}}
}

// mapMCPTrigger: n8n served the trigger's tool sub-nodes as an MCP server.
// mono-agent's own MCP server (monoagentcli mcp) runs saved workflows
// through its workflow_run tool, so the workflow starts manually and each
// tool becomes a workflow of its own.
func mapMCPTrigger(c *MapContext) Mapping {
	c.Ignore("path", "authentication", "options")
	var tools []string
	for _, s := range c.SubNodes {
		tools = append(tools, s.Name)
	}
	msg := "n8n served this trigger's tools as an MCP server; mono-agent's MCP server (monoagentcli mcp) runs saved workflows through its workflow_run tool"
	if len(tools) > 0 {
		msg += fmt.Sprintf(": rebuild each tool (%s) as a workflow and call it from your MCP client", strings.Join(tools, ", "))
	}
	c.Warn("tools", nil, "%s", msg)
	return Mapping{Type: "trigger.manual", Config: map[string]interface{}{}}
}

// mapEvaluationTrigger: n8n fed the rows of a test dataset (a Google
// Sheet or an n8n data table) through the workflow. The import starts
// manually and reads the sheet's rows.
func mapEvaluationTrigger(c *MapContext) Mapping {
	def := "googleSheets"
	if c.version(4.7) {
		def = "dataTable"
	}
	c.Ignore("notice", "credentials", "authentication")
	if src := c.Literal("source", def); src != "googleSheets" {
		c.Ignore("dataTableId", "limitRows", "maxRows", "filterRows", "filters")
		c.issues = append(c.issues, Issue{Severity: SevError, Param: "source", Value: src,
			Message: "n8n data tables have no mono-agent equivalent: export the dataset to a Google Sheet and read it from there"})
		return Mapping{Unmapped: true}
	}
	read := map[string]interface{}{
		"credential_id":  c.Credential("google_sheets", "googleSheetsOAuth2Api", "googleApi"),
		"operation":      "read_rows",
		"use_header_row": true,
		"spreadsheet_id": c.rlValue("documentId"),
		"sheet_name":     c.rlValue("sheetName"),
	}
	if len(c.paramList("filtersUI.values")) > 0 {
		c.Warn("filtersUI", c.Raw("filtersUI"), "row filters are not supported; every row is read")
	}
	c.Ignore("filtersUI", "combineFilters", "options")
	c.Note("starts manually and passes every dataset row at once (n8n ran the workflow once per row during an evaluation)")
	m := Mapping{Type: "trigger.manual", Config: map[string]interface{}{}, EntrySuffix: " (start)"}
	if c.Bool("limitRows", false) {
		m.Chain = []ChainStep{
			{Type: "service.google_sheets", Suffix: " (read)", Config: read},
			{Type: "core.limit", Config: map[string]interface{}{"max_items": c.Float("maxRows", 10)}},
		}
		return m
	}
	c.Ignore("maxRows")
	m.Chain = []ChainStep{{Type: "service.google_sheets", Config: read}}
	return m
}
