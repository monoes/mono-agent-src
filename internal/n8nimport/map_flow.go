package n8nimport

import (
	"fmt"
	"strings"
)

func init() {
	Register("n8n-nodes-base.splitInBatches", mapSplitInBatches)
	Register("n8n-nodes-base.wait", mapWait)
	Register("n8n-nodes-base.stopAndError", mapStopAndError)
	Register("n8n-nodes-base.noOp", mapNoOp)
	Register("n8n-nodes-base.code", mapCode)
	Register("n8n-nodes-base.aiTransform", mapAITransform)
	Register("n8n-nodes-base.function", mapFunction)
	Register("n8n-nodes-base.functionItem", mapFunctionItem)
	Register("n8n-nodes-base.respondToWebhook", mapRespondToWebhook)
}

func mapSplitInBatches(c *MapContext) Mapping {
	def := 10.0
	if c.version(3) {
		def = 1
	}
	cfg := map[string]interface{}{"batch_size": c.Float("batchSize", def)}
	if c.Bool("options.reset", false) {
		cfg["reset"] = true
	}
	switch {
	case c.version(3):
		return Mapping{Type: "core.split_in_batches", Config: cfg, Outputs: []string{"done", "loop"}}
	case c.version(2):
		return Mapping{Type: "core.split_in_batches", Config: cfg, Outputs: []string{"batch", "done"}}
	}
	return Mapping{Type: "core.split_in_batches", Config: cfg, Outputs: []string{"batch"}}
}

var waitUnitSeconds = map[string]float64{"seconds": 1, "minutes": 60, "hours": 3600, "days": 86400}

func mapWait(c *MapContext) Mapping {
	resume := c.Literal("resume", "timeInterval")
	switch resume {
	case "timeInterval":
		amountDef, unitDef := 1.0, "hours"
		if c.version(1.1) {
			amountDef, unitDef = 5, "seconds"
		}
		amount := c.Float("amount", amountDef)
		unit := c.Literal("unit", unitDef)
		secs := amount * waitUnitSeconds[unit]
		if secs > 3600 {
			c.Warn("amount", fmt.Sprintf("%v %s", amount, unit), "mono-agent waits at most 3600 seconds; the wait was shortened to one hour")
			secs = 3600
		}
		if secs < 1 {
			secs = 1
		}
		return Mapping{Type: "core.wait", Config: map[string]interface{}{"duration": secs}}
	case "webhook", "form":
		c.Ignore("httpMethod", "responseCode", "responseMode", "responseData", "responseBinaryPropertyName",
			"options", "formTitle", "formDescription", "formFields", "authentication", "incomingAuthentication")
		timeout := 0.0
		if c.Bool("limitWaitTime", false) && c.Literal("limitType", "afterTimeInterval") == "afterTimeInterval" {
			timeout = c.Float("resumeAmount", 1) * waitUnitSeconds[c.Literal("resumeUnit", "hours")] / 60
		}
		c.Ignore("limitType", "resumeAmount", "resumeUnit", "maxDateAndTime")
		c.Warn("resume", resume, "became a human review step: approve it with `monoagentcli hil approve` instead of calling a resume URL")
		return Mapping{Type: "core.human_in_loop", Config: map[string]interface{}{"timeout_minutes": timeout}}
	}
	c.Ignore("dateTime")
	c.Warn("resume", resume, "waiting until a date has no mono-agent equivalent; the node waits one second")
	return Mapping{Type: "core.wait", Config: map[string]interface{}{"duration": 1.0}}
}

func mapStopAndError(c *MapContext) Mapping {
	if c.Literal("errorType", "errorMessage") == "errorObject" {
		c.Note("the error object is used as the error message text")
		return Mapping{Type: "core.stop_error", Config: map[string]interface{}{"message": c.Str("errorObject", "{}")}}
	}
	return Mapping{Type: "core.stop_error", Config: map[string]interface{}{"message": c.Str("errorMessage", "")}}
}

// mapNoOp passes items through: core.merge re-emits its input unchanged.
func mapNoOp(_ *MapContext) Mapping {
	return Mapping{Type: "core.merge", Config: map[string]interface{}{}}
}

// mapRespondToWebhook answers the webhook (or form) whose trigger waits
// for it (response mode responseNode).
func mapRespondToWebhook(c *MapContext) Mapping {
	cfg := map[string]interface{}{}
	headers := map[string]interface{}{}
	switch with := c.Literal("respondWith", "firstIncomingItem"); with {
	case "firstIncomingItem", "allIncomingItems", "noData":
		cfg["respond_with"] = with
	case "json", "text":
		cfg["respond_with"] = with
		cfg["response_body"] = c.Value("responseBody")
	case "redirect":
		cfg["respond_with"], cfg["response_code"] = "noData", 307.0
		headers["Location"] = c.Value("redirectURL")
	default:
		cfg["respond_with"] = "firstIncomingItem"
		c.Warn("respondWith", with, "is not supported; the first incoming item's JSON is sent")
		c.Ignore("responseDataSource", "inputFieldName", "payload")
	}
	if c.Has("options.responseCode") {
		cfg["response_code"] = c.Float("options.responseCode", 200)
	}
	for _, h := range c.paramList("options.responseHeaders.entries") {
		if name, _ := h["name"].(string); name != "" {
			headers[name] = c.Translate("options.responseHeaders", h["value"])
		}
	}
	if len(headers) > 0 {
		cfg["response_headers"] = headers
	}
	if c.Has("options.responseKey") {
		c.Unsupported("options.responseKey")
	}
	c.Ignore("enableResponseOutput", "options.enableStreaming")
	return Mapping{Type: "core.respond_webhook", Config: cfg}
}

// importCode prepares n8n JavaScript for core.code and reports what mono's
// code runtime cannot provide.
func importCode(c *MapContext, param, code string) string {
	if strings.HasPrefix(code, "=") {
		c.Warn(param, code, "code set by an expression cannot be imported")
		return "throw new Error(\"the n8n code was set by an expression\");"
	}
	if m := unavailableInCodeNode.FindString(code); m != "" {
		c.Warn(param, nil, "uses %q, which mono-agent code nodes do not provide; it fails when that line runs", strings.TrimSpace(m))
	}
	return code
}

func mapCode(c *MapContext) Mapping {
	lang := c.Literal("language", "javaScript")
	if lang != "javaScript" {
		c.Ignore("pythonCode")
		c.Warn("language", lang, "Python code cannot run in mono-agent (core.code runs JavaScript)")
		return Mapping{Unmapped: true}
	}
	code := importCode(c, "jsCode", fmt.Sprint(c.Raw("jsCode")))
	if c.Literal("mode", "runOnceForAllItems") == "runOnceForEachItem" {
		return Mapping{Type: "core.code", Config: codeConfig(wrapPerItem(code))}
	}
	return Mapping{Type: "core.code", Config: codeConfig(wrapCode(code))}
}

// mapAITransform runs the code n8n generated from the instructions, which
// n8n saves with the node and runs once for all items like the Code node.
func mapAITransform(c *MapContext) Mapping {
	c.Ignore("instructions", "codeGeneratedForPrompt")
	src, _ := c.Raw("jsCode").(string)
	if src == "" {
		c.Warn("jsCode", nil, "n8n saved no generated code: write the transformation in this core.code node (instructions: %s)",
			compactValue(c.Raw("instructions")))
		src = "throw new Error(\"AI Transform: no code was generated in n8n\");"
	}
	return Mapping{Type: "core.code", Config: codeConfig(wrapCode(importCode(c, "jsCode", src)))}
}

func mapFunction(c *MapContext) Mapping {
	code := importCode(c, "functionCode", fmt.Sprint(c.Raw("functionCode")))
	return Mapping{Type: "core.code", Config: codeConfig(wrapCode(code))}
}

// mapFunctionItem runs the code per item with "item" bound to the item's
// JSON; the code returns the new JSON.
func mapFunctionItem(c *MapContext) Mapping {
	code := importCode(c, "functionCode", fmt.Sprint(c.Raw("functionCode")))
	body := "var item = $json;\nvar __r = await (async function () {\n" + indentJS(code) + "})();\nreturn __r === undefined ? item : __r;\n"
	return Mapping{Type: "core.code", Config: codeConfig(wrapPerItem(body))}
}
