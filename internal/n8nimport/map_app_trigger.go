package n8nimport

import (
	"fmt"
	"sort"
	"strings"
)

// App triggers. n8n starts workflows from vendor events in two ways: a
// webhook the trigger registers with the vendor when the workflow is
// activated, or a poll that asks the vendor's API for records changed
// since the last poll. Both are rebuilt from mono nodes:
//
//   - webhook triggers (webhookTriggers) become a trigger.webhook on the
//     n8n node's webhookId. Where trigger.webhook has a registrar for the
//     app (Typeform, Calendly built in; the app's API package's webhook
//     block, registerWithApp) activation registers the hook with the
//     vendor; for the others the report says which URL and events to
//     register by hand;
//   - polling triggers (pollTriggers) become a trigger.schedule on the n8n
//     poll times, the app package's list operation and a
//     core.remove_duplicates step that remembers what earlier runs saw, so
//     only new (or changed) records continue.

// hookSpec describes an n8n webhook trigger.
type hookSpec struct {
	// Vendor names the app in report messages.
	Vendor string
	// Where says where the webhook is added in the vendor's app or API.
	Where string
	// Method is the HTTP method the vendor sends (default POST).
	Method string
	// Handshake describes a verification request the vendor sends before
	// it delivers events, which mono-agent webhooks do not answer.
	Handshake string
	// Register, when set, fills trigger.webhook's register_with,
	// register_config and credential_id so activation registers the hook
	// with the vendor; it reads (and so consumes) its n8n parameters.
	Register func(c *MapContext, cfg map[string]interface{})
	// DedupeHeader is a delivery-id header the vendor repeats on retries.
	DedupeHeader string
	// Shape returns a step reshaping the payload the way the n8n trigger
	// did; it reads (and so consumes) its n8n parameters.
	Shape func(c *MapContext) *ChainStep
}

// pollSpec describes an n8n polling trigger.
type pollSpec struct {
	// Plan fills in the list step: resource, operation, params and the
	// value that identifies a record (or a record version). A non-empty
	// result says why this node's settings cannot be rebuilt.
	Plan func(c *MapContext, p *pollPlan) string
}

// pollPlan is the list step of a polling trigger.
type pollPlan struct {
	Resource, Operation string
	Config              map[string]interface{}
	// Dedupe is an n8n expression ("={{ $json.id }}") identifying a
	// record; empty compares whole items.
	Dedupe string
	// Lookback is how many days back a time-windowed list reaches; a few
	// poll intervals, so a missed tick does not lose records.
	Lookback int
}

func init() {
	for t, spec := range webhookTriggers {
		spec := spec
		Register("n8n-nodes-base."+t, func(c *MapContext) Mapping { return mapHookTrigger(c, spec) })
	}
	for t, spec := range pollTriggers {
		spec := spec
		Register("n8n-nodes-base."+t, func(c *MapContext) Mapping { return mapPollTrigger(c, spec) })
	}
}

// TriggerKind says how an n8n app trigger type ("n8n-nodes-base.x" or
// "x") is imported: "webhook" (a trigger.webhook), "poll" (a
// trigger.schedule that lists the app's records and drops those seen
// before) or "" when no trigger table has it.
func TriggerKind(n8nType string) string {
	name := strings.TrimPrefix(n8nType, "n8n-nodes-base.")
	if _, ok := webhookTriggers[name]; ok {
		return "webhook"
	}
	if _, ok := pollTriggers[name]; ok {
		return "poll"
	}
	return ""
}

// triggerParamsIgnored are n8n trigger parameters about n8n's own handling
// (credential choice, UI notices) that say nothing about the vendor setup.
var triggerParamsIgnored = []string{"authentication", "notice"}

// webhookPath is the trigger.webhook path: the n8n webhookId, so the URL
// keeps n8n's id, else a slug of the node name.
func webhookPath(c *MapContext) string {
	if id := strings.Trim(c.WebhookID, "/"); id != "" {
		return id
	}
	return strings.ToLower(strings.Trim(nonIDChars.ReplaceAllString(c.NodeName, "-"), "-"))
}

func mapHookTrigger(c *MapContext, spec hookSpec) Mapping {
	method := spec.Method
	if method == "" {
		method = "POST"
	}
	path := webhookPath(c)
	cfg := map[string]interface{}{"path": path, "method": method}
	if spec.DedupeHeader != "" {
		cfg["dedupe_header"] = spec.DedupeHeader
	}
	m := Mapping{Type: "trigger.webhook", Config: cfg}
	if spec.Shape != nil {
		if step := spec.Shape(c); step != nil {
			m.EntrySuffix = " (receive)"
			m.Chain = []ChainStep{*step}
		}
	}
	c.Ignore(triggerParamsIgnored...)
	registered := spec.Register != nil
	if registered {
		spec.Register(c, cfg)
	} else {
		registered = registerWithApp(c, cfg)
	}
	if registered {
		c.Warn("webhook", nil, "activation registers https://<public URL>/webhook/%s with %s and removes it on deactivation; "+
			"%s must reach the webhook server from the internet: set the trigger's public_url or MONOAGENT_PUBLIC_URL (e.g. a tunnel to 127.0.0.1:9321)",
			path, spec.Vendor, spec.Vendor)
		if spec.Handshake != "" {
			c.Warn("webhook", nil, "%s verifies the URL first (%s); mono-agent webhooks do not answer that check yet", spec.Vendor, spec.Handshake)
		}
		return m
	}
	where := spec.Where
	if where == "" {
		where = spec.Vendor + "'s webhook settings"
	}
	msg := fmt.Sprintf("register https://<your mono-agent host>/webhook/%s (%s) in %s", path, method, where)
	if setup := c.vendorSetup(); setup != "" {
		msg += " for " + setup
	}
	c.Warn("webhook", nil, "%s; n8n registered this webhook itself when the workflow was activated, mono-agent does not", msg)
	if spec.Handshake != "" {
		c.Warn("webhook", nil, "%s verifies the URL first (%s); mono-agent webhooks do not answer that check yet", spec.Vendor, spec.Handshake)
	}
	return m
}

// vendorSetup lists the node's remaining parameters (events, ids,
// filters: what the n8n trigger subscribed to) as "name = value" text and
// marks them used: they are now the user's registration settings.
func (c *MapContext) vendorSetup() string {
	keys := make([]string, 0, len(c.Params))
	for k := range c.Params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		if c.used[k] || isEmptyValue(c.Params[k]) {
			continue
		}
		parts = append(parts, k+" = "+compactValue(c.Raw(k)))
	}
	return strings.Join(parts, ", ")
}

func mapPollTrigger(c *MapContext, spec pollSpec) Mapping {
	c.Ignore(triggerParamsIgnored...)
	app, ok := lookupApp(c.Type)
	if !ok || (app.NodeType == "" && app.Extends == "") {
		c.issues = append(c.issues, Issue{Severity: SevError,
			Message: fmt.Sprintf("n8n polling trigger %s needs the app's API package, which is not installed", c.Type)})
		return Mapping{Unmapped: true}
	}
	plan := &pollPlan{Config: map[string]interface{}{}, Lookback: pollLookback(c)}
	if reason := spec.Plan(c, plan); reason != "" {
		c.issues = append(c.issues, Issue{Severity: SevError,
			Message: fmt.Sprintf("n8n polling trigger %s cannot be rebuilt: %s", c.Type, reason)})
		return Mapping{Unmapped: true}
	}
	m := c.pollTrigger()
	m.EntrySuffix = " (poll)"
	cfg := plan.Config
	if plan.Resource != "" {
		cfg["resource"] = plan.Resource
	}
	cfg["operation"] = plan.Operation
	if id := app.credential(c); id != "" {
		cfg["credential_id"] = id
	}
	nodeType := app.NodeType
	if nodeType == "" {
		nodeType = app.Extends
	}
	dedupe := map[string]interface{}{"operation": "removeItemsRepeatedWithinPreviousExecutions"}
	if plan.Dedupe != "" {
		dedupe["dedupeValue"] = c.Translate("dedupe", plan.Dedupe)
	}
	what := plan.Operation
	if plan.Resource != "" {
		what = plan.Resource + "." + plan.Operation
	}
	m.Chain = []ChainStep{
		{Type: nodeType, Suffix: " (list)", Config: cfg},
		{Type: "core.remove_duplicates", Config: dedupe},
	}
	c.Note("polls %s %s on the n8n schedule and passes on only records no earlier run saw; the first run passes every listed record (n8n's trigger passed nothing on its first poll)", nodeType, what)
	return m
}

// pollLookback picks the time window of time-filtered lists from the poll
// times: a few intervals, so a missed tick does not drop records.
func pollLookback(c *MapContext) int {
	days := 2
	for _, t := range c.paramList("pollTimes.item") {
		switch t["mode"] {
		case "everyWeek":
			days = max(days, 8)
		case "everyMonth", "custom":
			days = max(days, 32)
		case "everyX":
			if t["unit"] != "minutes" {
				days = max(days, int(num(t, "value", 2)/24)+2)
			}
		}
	}
	return days
}

// sinceExpr is a mono template for the UTC time `days` days ago, in the
// layout the vendor's API wants ("2006-01-02T15:04:05Z"). The layout is a
// raw string, so the template can sit inside JSON text.
func sinceExpr(days int, layout string) string {
	return fmt.Sprintf("{{ ($now.AddDate 0 0 -%d).UTC.Format `%s` }}", days, layout)
}

// copyParams copies n8n parameters into the list step under the op's
// param names (op param → n8n param), translating expressions.
func (p *pollPlan) copyParams(c *MapContext, pairs map[string]string) {
	for dst, src := range pairs {
		if c.Has(src) {
			p.Config[dst] = c.Value(src)
		}
	}
}
