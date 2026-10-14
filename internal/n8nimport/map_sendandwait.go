package n8nimport

import "strings"

// n8n's "send and wait for response" operation (sendAndWait) posts a
// message with approve/decline buttons and pauses until the recipient
// answers through n8n's webhook. mono-agent has no inbound reply channel
// for these apps, so the node becomes the app's plain send operation
// followed by a human review step (core.human_in_loop) that someone
// approves or rejects in mono-agent.

// sendAndWaitSend says how a sendAndWait node becomes the app's send op.
type sendAndWaitSend struct {
	resource, operation string
	// text is the send op's parameter that takes sendAndWait's "message"
	// ("" keeps "message"; a dotted path nests it).
	text string
	// set are fixed params of the send op; defaults are applied only when
	// the node leaves the param out (sendAndWait's own defaults differ).
	set, defaults map[string]interface{}
	app           string
}

var sendAndWaitSends = map[string]sendAndWaitSend{
	"n8n-nodes-base.slack":            {resource: "message", operation: "post", text: "text", defaults: map[string]interface{}{"select": "user"}, app: "Slack"},
	"n8n-nodes-base.telegram":         {resource: "message", operation: "sendMessage", text: "text", app: "Telegram"},
	"n8n-nodes-base.discord":          {resource: "message", operation: "send", text: "content", app: "Discord"},
	"n8n-nodes-base.whatsApp":         {resource: "message", operation: "send", text: "textBody", set: map[string]interface{}{"messageType": "text"}, app: "WhatsApp"},
	"n8n-nodes-base.gmail":            {resource: "message", operation: "send", set: map[string]interface{}{"emailType": "text"}, app: "Gmail"},
	"n8n-nodes-base.microsoftOutlook": {resource: "message", operation: "send", text: "bodyContent", set: map[string]interface{}{"additionalFields": map[string]interface{}{"bodyContentType": "Text"}}, app: "Outlook"},
	"n8n-nodes-base.microsoftTeams":   {resource: "chatMessage", operation: "create", set: map[string]interface{}{"contentType": "text"}, app: "Teams"},
	"n8n-nodes-base.googleChat":       {resource: "message", operation: "create", text: "messageUi.text", app: "Google Chat"},
	"n8n-nodes-base.emailSend":        {resource: "email", operation: "send", text: "text", set: map[string]interface{}{"emailFormat": "text"}, app: "email"},
}

// sendAndWaitParams are sendAndWait's own parameters (buttons, response
// form, wait limit), dropped before the send op is mapped.
var sendAndWaitParams = []string{"responseType", "approvalOptions", "options", "defineForm", "formFields", "jsonOutput"}

// sendAndWaitPlan is a sendAndWait node being mapped.
type sendAndWaitPlan struct {
	spec         sendAndWaitSend
	timeout      float64 // minutes; 0 = no limit
	responseType string
	limitType    string
}

// rewriteSendAndWait turns a sendAndWait node's params into the app's send
// op (ctx.Params is replaced by a rewritten copy) and returns the plan for
// the review step, or nil for any other node.
func rewriteSendAndWait(ctx *MapContext) *sendAndWaitPlan {
	spec, ok := sendAndWaitSends[ctx.Type]
	if !ok {
		return nil
	}
	if op, _ := ctx.Params["operation"].(string); op != "sendAndWait" {
		return nil
	}
	plan := &sendAndWaitPlan{spec: spec, responseType: "approval", limitType: "afterTimeInterval"}
	if v, _ := ctx.Params["responseType"].(string); v != "" {
		plan.responseType = v
	}
	if lim, ok := lookupPath(ctx.Params, "options.limitWaitTime.values"); ok {
		if m, ok := lim.(map[string]interface{}); ok {
			if v, _ := m["limitType"].(string); v != "" {
				plan.limitType = v
			}
			if plan.limitType == "afterTimeInterval" {
				amount := 1.0
				if v, ok := m["resumeAmount"].(float64); ok {
					amount = v
				}
				unit, _ := m["resumeUnit"].(string)
				if unit == "" {
					unit = "hours"
				}
				plan.timeout = amount * waitUnitSeconds[unit] / 60
			}
		}
	}

	params := make(map[string]interface{}, len(ctx.Params)+len(spec.set))
	for k, v := range ctx.Params {
		params[k] = v
	}
	for _, k := range sendAndWaitParams {
		delete(params, k)
	}
	params["resource"], params["operation"] = spec.resource, spec.operation
	if spec.text != "" {
		msg := params["message"]
		delete(params, "message")
		setPath(params, spec.text, msg)
	}
	for k, v := range spec.defaults {
		if _, ok := params[k]; !ok {
			params[k] = v
		}
	}
	for k, v := range spec.set {
		params[k] = v
	}
	ctx.Params = params
	return plan
}

// attach adds the review step after the send node and reports the change.
func (p *sendAndWaitPlan) attach(ctx *MapContext, m Mapping) Mapping {
	m.Chain = append(m.Chain, ChainStep{Type: "core.human_in_loop", Config: map[string]interface{}{"timeout_minutes": p.timeout}})
	if m.EntrySuffix == "" && len(m.Chain) == 1 {
		m.EntrySuffix = " (send)"
	}
	ctx.Warn("operation", "sendAndWait",
		"became %s.%s followed by a human review step: approve or reject it in mono-agent (`monoagentcli hil approve`/`hil reject` or the review queue). "+
			"The %s message has no approve/decline buttons and replies there are not read; an approval passes the items on, a rejection stops the run unless the review step's \"rejected\" output is wired",
		p.spec.resource, p.spec.operation, p.spec.app)
	if p.responseType != "approval" {
		ctx.Warn("responseType", p.responseType, "the review step only approves or rejects: the free-text or form answer is not collected")
	}
	if p.limitType != "afterTimeInterval" {
		ctx.Warn("options.limitWaitTime", p.limitType, "waiting until a date is not supported: the review step waits without a limit")
	}
	return m
}

// setPath sets a dotted path in m, creating nested objects.
func setPath(m map[string]interface{}, path string, v interface{}) {
	parts := strings.Split(path, ".")
	for _, k := range parts[:len(parts)-1] {
		next, ok := m[k].(map[string]interface{})
		if !ok {
			next = map[string]interface{}{}
			m[k] = next
		}
		m = next
	}
	m[parts[len(parts)-1]] = v
}
