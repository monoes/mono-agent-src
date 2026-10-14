package n8nimport

import (
	"fmt"
	"strconv"
	"strings"
)

func init() {
	Register("n8n-nodes-base.manualTrigger", mapManual)
	Register("n8n-nodes-base.start", mapManual)
	Register("n8n-nodes-base.scheduleTrigger", mapScheduleTrigger)
	Register("n8n-nodes-base.cron", mapCron)
	Register("n8n-nodes-base.interval", mapInterval)
	Register("n8n-nodes-base.webhook", mapWebhook)
	Register("n8n-nodes-base.rssFeedReadTrigger", mapRSSTrigger)
	Register("n8n-nodes-base.emailReadImap", mapEmailTrigger)
	Register("@n8n/n8n-nodes-langchain.manualChatTrigger", mapManual)
	Register("@n8n/n8n-nodes-langchain.chatTrigger", mapChatTrigger)
}

func mapManual(_ *MapContext) Mapping {
	return Mapping{Type: "trigger.manual", Config: map[string]interface{}{}}
}

// mapChatTrigger becomes a manual trigger: the chat message is the run's
// input ({"chatInput": "…"}), as in n8n.
func mapChatTrigger(c *MapContext) Mapping {
	c.Ignore("public", "mode", "authentication", "initialMessages", "options", "availableInChat",
		"agentName", "agentDescription", "agentIcon", "suggestedPrompts")
	c.Note("n8n's hosted chat is not available: run the workflow with input {\"chatInput\": \"…\"} (workflow run --input)")
	return Mapping{Type: "trigger.manual", Config: map[string]interface{}{}}
}

// schedules turns trigger steps into a Mapping: the first cron is the
// node, the others become alternate trigger nodes.
func schedules(crons []string) Mapping {
	if len(crons) == 0 {
		crons = []string{"0 0 0 * * *"}
	}
	m := Mapping{Type: "trigger.schedule", Config: map[string]interface{}{"cron": crons[0]}}
	for i, cr := range crons[1:] {
		m.Alternates = append(m.Alternates, ChainStep{Type: "trigger.schedule", Suffix: fmt.Sprintf(" (rule %d)", i+2),
			Config: map[string]interface{}{"cron": cr}})
	}
	return m
}

// every returns the cron step for "every n" (n=1 → "*").
func every(n float64) string {
	if n <= 1 {
		return "*"
	}
	return "*/" + strconv.Itoa(int(n))
}

func num(m map[string]interface{}, key string, def float64) float64 {
	switch v := m[key].(type) {
	case float64:
		return v
	case string:
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

// normalizeCron turns a 5-field cron into mono's 6-field form.
func normalizeCron(expr string) (string, bool) {
	f := strings.Fields(expr)
	switch len(f) {
	case 5:
		return "0 " + strings.Join(f, " "), true
	case 6:
		return strings.Join(f, " "), true
	}
	return "", false
}

func (c *MapContext) cronExpression(param, expr string) string {
	if strings.HasPrefix(expr, "=") {
		c.Warn(param, expr, "a schedule set by an expression cannot be converted; it runs daily at midnight")
		return "0 0 0 * * *"
	}
	cr, ok := normalizeCron(expr)
	if !ok {
		c.Warn(param, expr, "is not a 5- or 6-field cron expression; it runs daily at midnight")
		return "0 0 0 * * *"
	}
	return cr
}

func mapScheduleTrigger(c *MapContext) Mapping {
	rules := c.paramList("rule.interval")
	if !c.Has("rule.interval") {
		rules = []map[string]interface{}{{"field": "days"}}
	}
	var crons []string
	for i, r := range rules {
		field, _ := r["field"].(string)
		if field == "" {
			field = "days"
		}
		min := strconv.Itoa(int(num(r, "triggerAtMinute", 0)))
		hour := strconv.Itoa(int(num(r, "triggerAtHour", 0)))
		var cr string
		switch field {
		case "seconds":
			cr = fmt.Sprintf("%s * * * * *", every(num(r, "secondsInterval", 30)))
		case "minutes":
			cr = fmt.Sprintf("0 %s * * * *", every(num(r, "minutesInterval", 5)))
		case "hours":
			cr = fmt.Sprintf("0 %s %s * * *", min, every(num(r, "hoursInterval", 1)))
		case "days":
			cr = fmt.Sprintf("0 %s %s %s * *", min, hour, every(num(r, "daysInterval", 1)))
		case "weeks":
			if n := num(r, "weeksInterval", 1); n > 1 {
				c.Warn(fmt.Sprintf("rule.interval[%d].weeksInterval", i), n, "every %v weeks cannot be expressed in cron; it runs every week", n)
			}
			days := []string{}
			switch d := r["triggerAtDay"].(type) {
			case []interface{}:
				for _, x := range d {
					days = append(days, strconv.Itoa(int(num(map[string]interface{}{"v": x}, "v", 0))))
				}
			}
			if len(days) == 0 {
				days = []string{"0"}
			}
			cr = fmt.Sprintf("0 %s %s * * %s", min, hour, strings.Join(days, ","))
		case "months":
			cr = fmt.Sprintf("0 %s %s %d %s *", min, hour, int(num(r, "triggerAtDayOfMonth", 1)), every(num(r, "monthsInterval", 1)))
		case "cronExpression":
			expr, _ := r["expression"].(string)
			cr = c.cronExpression(fmt.Sprintf("rule.interval[%d].expression", i), expr)
		default:
			c.Warn(fmt.Sprintf("rule.interval[%d].field", i), field, "unknown schedule unit; it runs daily at midnight")
			cr = "0 0 0 * * *"
		}
		crons = append(crons, cr)
	}
	return schedules(crons)
}

// cronItems converts the legacy Cron node's triggerTimes (also used for
// the poll times of polling triggers).
func (c *MapContext) cronItems(param string) []string {
	var crons []string
	for i, t := range c.paramList(param) {
		mode, _ := t["mode"].(string)
		if mode == "" {
			mode = "everyDay"
		}
		h := strconv.Itoa(int(num(t, "hour", 14)))
		m := strconv.Itoa(int(num(t, "minute", 0)))
		switch mode {
		case "everyMinute":
			crons = append(crons, "0 * * * * *")
		case "everyHour":
			crons = append(crons, "0 "+m+" * * * *")
		case "everyDay":
			crons = append(crons, fmt.Sprintf("0 %s %s * * *", m, h))
		case "everyWeek":
			crons = append(crons, fmt.Sprintf("0 %s %s * * %d", m, h, int(num(t, "weekday", 1))))
		case "everyMonth":
			crons = append(crons, fmt.Sprintf("0 %s %s %d * *", m, h, int(num(t, "dayOfMonth", 1))))
		case "everyX":
			v := num(t, "value", 2)
			if t["unit"] == "minutes" {
				crons = append(crons, fmt.Sprintf("0 %s * * * *", every(v)))
			} else {
				crons = append(crons, fmt.Sprintf("0 0 %s * * *", every(v)))
			}
		case "custom":
			expr, _ := t["cronExpression"].(string)
			crons = append(crons, c.cronExpression(fmt.Sprintf("%s[%d].cronExpression", param, i), expr))
		}
	}
	return crons
}

func mapCron(c *MapContext) Mapping { return schedules(c.cronItems("triggerTimes.item")) }

func mapInterval(c *MapContext) Mapping {
	n := c.Float("interval", 1)
	unit := c.Literal("unit", "seconds")
	var cr string
	switch unit {
	case "minutes":
		cr = "0 " + every(n) + " * * * *"
	case "hours":
		cr = "0 0 " + every(n) + " * * *"
	default:
		cr = every(n) + " * * * * *"
	}
	limit := map[string]float64{"seconds": 60, "minutes": 60, "hours": 24}[unit]
	if n > 1 && int(limit)%int(n) != 0 {
		c.Note("every %v %s does not divide evenly; the cron schedule restarts at each %s boundary", n, unit, strings.TrimSuffix(unit, "s"))
	}
	return schedules([]string{cr})
}

// mapWebhook: mono-agent's webhook item carries the request the way n8n's
// does ({body, query, headers, …}), so expressions such as
// $json.body.email keep working, and its response modes are n8n's.
func mapWebhook(c *MapContext) Mapping {
	path := strings.Trim(c.Literal("path", ""), "/")
	if path == "" {
		path = strings.ToLower(nonIDChars.ReplaceAllString(c.NodeName, "-"))
	}
	if strings.Contains(path, ":") {
		c.Warn("path", path, "path parameters (:name) are not supported; the path is matched literally")
	}
	method := "GET"
	switch v := c.Raw("httpMethod").(type) {
	case string:
		method = v
	case []interface{}:
		if len(v) > 0 {
			method = fmt.Sprint(v[0])
		}
		if len(v) > 1 {
			c.Warn("httpMethod", v, "one trigger listens for one method; only %s is kept", method)
		}
	}
	if method == "HEAD" {
		c.Warn("httpMethod", method, "mono-agent webhooks do not answer HEAD; it listens for GET")
		method = "GET"
	}
	c.Ignore("multipleMethods")
	cfg := map[string]interface{}{"path": path, "method": method}
	switch auth := c.Literal("authentication", "none"); auth {
	case "none":
	case "headerAuth":
		cfg["auth_header"] = ""
		cfg["auth_token"] = "@secret:" + strings.ToLower(nonIDChars.ReplaceAllString(c.NodeName, "_")) + "_token"
		c.Warn("authentication", auth, "set auth_header to the header name of the n8n credential and store its value in the vault secret named in auth_token")
	default:
		c.Warn("authentication", auth, "has no mono-agent equivalent; the webhook accepts any caller until you set auth_header/auth_token")
	}
	c.webhookResponse(cfg)
	if c.Has("options.ipWhitelist") {
		c.Unsupported("options.ipWhitelist")
	}
	for _, p := range []string{"options.ignoreBots", "options.rawBody"} {
		if c.Bool(p, false) {
			c.Unsupported(p)
		}
	}
	if c.Has("options.onlyRunIf") {
		c.Unsupported("options.onlyRunIf")
	}
	// Multipart files arrive as the item's binary data without settings.
	c.Ignore("options.binaryData", "options.binaryPropertyName", "options.oauthClient", "requireExecuteAccess")
	return Mapping{Type: "trigger.webhook", Config: cfg}
}

// webhookResponse carries n8n's Respond setting (responseMode,
// responseCode, responseData) over to the trigger's response options.
func (c *MapContext) webhookResponse(cfg map[string]interface{}) {
	switch mode := c.Literal("responseMode", "onReceived"); mode {
	case "onReceived":
		code := c.Float("responseCode", 200)
		if c.Has("options.responseCode") {
			code = c.Float("options.responseCode.values.responseCode", 200)
			if c.Literal("options.responseCode.values.responseCode", "") == "customCode" {
				code = c.Float("options.responseCode.values.customCode", 200)
			}
		}
		if code < 200 || code > 299 {
			c.Warn("responseCode", code, "mono-agent answers a received request with a 2xx status; 200 is used")
		} else if code != 200 {
			cfg["response_code"] = code
		}
		if c.Has("options.responseData") || c.Bool("options.noResponseBody", false) {
			c.Warn("options.responseData", c.Raw("options.responseData"), "the immediate answer is mono-agent's {execution_id} JSON")
			c.Ignore("options.noResponseBody")
		}
	case "lastNode":
		cfg["response_mode"] = "lastNode"
		switch data := c.Literal("responseData", "firstEntryJson"); data {
		case "firstEntryJson", "allEntries", "noData":
			if data != "firstEntryJson" {
				cfg["response_data"] = data
			}
		default:
			c.Warn("responseData", data, "only firstEntryJson, allEntries and noData answers are supported; the first item's JSON is sent")
			c.Ignore("responseBinaryPropertyName")
		}
		if c.Has("options.responsePropertyName") {
			c.Unsupported("options.responsePropertyName")
		}
	case "responseNode":
		cfg["response_mode"] = "responseNode"
	default:
		c.Warn("responseMode", mode, "is not supported; the request is answered at once")
	}
	c.Ignore("responseCode")
	for _, p := range []string{"options.responseHeaders", "options.responseContentType"} {
		if c.Has(p) {
			c.Warn(p, c.Raw(p), "the trigger's own answer has no custom headers; answer from a Respond to Webhook node (response mode responseNode) instead")
		}
	}
}

// pollTrigger returns the poll schedule of an n8n polling trigger.
func (c *MapContext) pollTrigger() Mapping {
	crons := c.cronItems("pollTimes.item")
	if len(crons) == 0 {
		crons = []string{"0 * * * * *"}
	}
	return schedules(crons)
}

func mapRSSTrigger(c *MapContext) Mapping {
	m := c.pollTrigger()
	m.EntrySuffix = " (poll)"
	c.Note("polls the feed on the n8n schedule; mono-agent returns the latest entries each time, not only new ones")
	m.Chain = []ChainStep{{Type: "system.rss_read", Config: map[string]interface{}{"url": c.Str("feedUrl", "")}}}
	return m
}

func mapEmailTrigger(c *MapContext) Mapping {
	m := Mapping{Type: "trigger.schedule", Config: map[string]interface{}{"cron": "0 * * * * *"}, EntrySuffix: " (poll)"}
	// mono has no IMAP connection platform: the server settings are node
	// fields and the password comes from a vault secret.
	cfg := map[string]interface{}{
		"imap_host":   "",
		"imap_port":   993.0,
		"username":    "",
		"password":    c.Secret("password", "imap"),
		"tls":         true,
		"mailbox":     c.Str("mailbox", "INBOX"),
		"unread_only": true,
	}
	c.Warn("credentials", nil, "set imap_host and username on the read step (they are not in an n8n export)")
	if c.Literal("postProcessAction", "read") == "nothing" {
		c.Note("mono-agent reads unread mail; n8n left messages unread (postProcessAction nothing), so the same mail can be read again")
	}
	if f := c.Literal("format", "simple"); f != "simple" {
		c.Warn("format", f, "mono-agent returns parsed messages only")
	}
	if c.Bool("downloadAttachments", false) {
		c.Unsupported("downloadAttachments")
	}
	c.Ignore("dataPropertyAttachmentsPrefixName", "options.forceReconnect", "options.trackLastMessageId", "options.allowUnauthorizedCerts")
	if v := c.Raw("options.customEmailConfig"); v != nil && v != `["UNSEEN"]` {
		c.Warn("options.customEmailConfig", v, "custom IMAP search criteria are not supported; unread mail is read")
	}
	c.Note("polls the mailbox every minute")
	m.Chain = []ChainStep{{Type: "comm.email_read", Config: cfg}}
	return m
}
